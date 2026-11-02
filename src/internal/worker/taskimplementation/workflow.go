// Package taskimplementation is the worker template for the TASK-3
// autonomous implementation agent. A commit event that adds or modifies a
// backlog task (not a user story) under backlog/tasks/ starts
// taskImplementationWorkflow with the commit's payload and the task files
// the shared backlog classification found. For every task the workflow
// reads the task at the commit, inspects the repository, asks the LLM to
// implement the task (prompt.go), and commits the proposed changes to an
// implement/<task-slug>-<short-sha> branch. The result reports whether
// the implementation is complete or partial; a partial one carries the
// reason and the open questions. The workflow then opens the merge
// request a human sees (mr.go, mrsubmission.go): a regular MR for a
// complete implementation, a draft MR for a partial one that is handed
// to the refinement agent by tagging it on the MR, so the open questions
// start a refinement round instead of leaving the task as a dead end.
package taskimplementation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker/backlog"
)

// WorkflowName is the Temporal workflow type name for the task
// implementation workflow.
const WorkflowName = "taskImplementationWorkflow"

// ImplementationBranchPrefix prefixes every branch the implementation
// agent creates for its merge requests. The agent-branch rules
// themselves live in the shared backlog package (see
// backlog.IsAgentBranch).
const ImplementationBranchPrefix = backlog.ImplementationBranchPrefix

// Activity type names. The repository tooling activities (repository.go)
// are the only way the workflow touches the repository: reading the task,
// listing the repository, reading source files, and committing to the
// implementation branch.
const (
	// ReadTaskName is the Temporal activity type name for the activity
	// that reads the backlog task the agent must implement.
	ReadTaskName = "taskImplementation.readTask"
	// ListRepositoryName is the Temporal activity type name for the
	// activity that lists the repository files on the task branch.
	ListRepositoryName = "taskImplementation.listRepository"
	// ReadFilesName is the Temporal activity type name for the activity
	// that reads source files at a ref.
	ReadFilesName = "taskImplementation.readFiles"
	// GenerateImplementationName is the Temporal activity type name for
	// the activity that asks the LLM to implement the task against the
	// repository context.
	GenerateImplementationName = "taskImplementation.generateImplementation"
	// CreateImplementationBranchName is the Temporal activity type name
	// for the activity that creates the implementation branch from the
	// task commit.
	CreateImplementationBranchName = "taskImplementation.createImplementationBranch"
	// CommitImplementationName is the Temporal activity type name for the
	// activity that commits the agent-produced changes to the
	// implementation branch.
	CommitImplementationName = "taskImplementation.commitImplementation"
	// SubmitMergeRequestName is the Temporal activity type name for the
	// activity that opens the merge request carrying an implementation —
	// a regular MR for a complete one, a draft MR with a refiner
	// hand-off for a partial one.
	SubmitMergeRequestName = "taskImplementation.submitMergeRequest"
)

// Input is the commit event payload the workflow receives: which
// repository and branch the commit landed on, the commit itself, and the
// backlog task files the shared backlog classification found in it.
type Input struct {
	Provider   string   `json:"provider"`   // event source provider, e.g. "forgejo"
	Project    string   `json:"project"`    // watched project name, e.g. "BeanieGenie"
	Repository string   `json:"repository"` // provider full repo name, e.g. "root/BeanieGenie"
	Branch     string   `json:"branch"`     // short branch name, e.g. "main"
	SHA        string   `json:"sha"`        // commit hash
	Message    string   `json:"message"`    // commit message
	URL        string   `json:"url"`        // commit URL on the provider
	Author     string   `json:"author"`     // commit author (username, name, or email)
	Added      []string `json:"added,omitempty"`
	Removed    []string `json:"removed,omitempty"`
	Modified   []string `json:"modified,omitempty"`
	// TaskPaths lists the backlog task files the commit added or
	// modified, as classified by the shared backlog classification
	// before the workflow started. The workflow re-verifies them (agent
	// branches, backlog paths) before acting on them.
	TaskPaths []string `json:"task_paths"`
	// ProviderURL is the provider's web base URL (e.g.
	// "http://localhost:3000"), used to link the task file in the merge
	// request description. Optional; the description names the task path
	// without a link when it is empty.
	ProviderURL string `json:"provider_url,omitempty"`
}

// Validate reports whether the payload identifies an implementable
// commit: a repository, a commit SHA, and at least one task file.
func (in Input) Validate() error {
	if strings.TrimSpace(in.SHA) == "" {
		return errors.New("commit sha is required")
	}
	if strings.TrimSpace(in.Repository) == "" {
		return errors.New("repository is required")
	}
	if len(in.TaskPaths) == 0 {
		return errors.New("task paths are required")
	}
	return nil
}

// Result is the outcome of one task implementation run.
type Result struct {
	// Implemented is true when the workflow completed: the commit was
	// inspected for task files.
	Implemented bool `json:"implemented"`
	// Status summarizes what the run did.
	Status string `json:"status"`
	// TaskPaths lists the backlog task files the commit added and the
	// workflow will implement.
	TaskPaths []string `json:"task_paths,omitempty"`
	// Implementations records one implementation per task the workflow
	// attempted, in task order. Each complete one becomes a regular MR
	// and each partial one a draft MR with a refiner hand-off.
	Implementations []TaskImplementation `json:"implementations,omitempty"`
	// MergeRequestURL is the merge request carrying the first
	// implementation the workflow opened (empty when it opened none).
	// Every implementation also reports its own MR in
	// TaskImplementation.MergeRequest.
	MergeRequestURL string `json:"merge_request_url,omitempty"`
}

// TaskImplementation is the outcome of implementing one backlog task.
type TaskImplementation struct {
	// TaskPath is the task file the implementation was produced for.
	TaskPath string `json:"task_path"`
	// TaskID is the task's backlog id, e.g. "TASK-23".
	TaskID string `json:"task_id"`
	// TaskTitle is the task's title.
	TaskTitle string `json:"task_title"`
	// Complete reports whether the implementation fulfills the task's
	// acceptance criteria. When false, Reason and OpenQuestions explain
	// why, and the merge request step opens a draft MR instead.
	Complete bool `json:"complete"`
	// Reason explains a partial implementation; empty for a complete
	// one.
	Reason string `json:"reason,omitempty"`
	// OpenQuestions lists the questions blocking a complete
	// implementation; empty for a complete one.
	OpenQuestions []string `json:"open_questions,omitempty"`
	// Branch is the implement/* branch carrying the changes.
	Branch string `json:"branch"`
	// ChangedFiles lists the files the implementation changed, in
	// commit order.
	ChangedFiles []string `json:"changed_files,omitempty"`
	// CommitSHA is the implementation commit's hash.
	CommitSHA string `json:"commit_sha,omitempty"`
	// PromptVersion is the implementation prompt version that produced
	// the changes.
	PromptVersion string `json:"prompt_version,omitempty"`
	// MergeRequest is the merge request carrying this implementation: a
	// regular MR for a complete one, a draft MR with a refiner hand-off
	// for a partial one. Nil when the MR could not be opened.
	MergeRequest *MergeRequestSubmission `json:"merge_request,omitempty"`
}

// MergeRequestSubmission identifies the merge request carrying one
// implementation.
type MergeRequestSubmission struct {
	// Number identifies the merge request within the repository.
	Number int64 `json:"number"`
	// Title is the merge request title.
	Title string `json:"title"`
	// URL is the merge request's page on the provider.
	URL string `json:"url"`
	// Draft reports whether the merge request is a draft: true for a
	// partial implementation.
	Draft bool `json:"draft"`
	// RefinerCommentID is the refiner hand-off comment posted on a draft
	// MR; zero for a complete implementation, which never involves the
	// refiner.
	RefinerCommentID int64 `json:"refiner_comment_id,omitempty"`
}

// TaskImplementationWorkflow implements the backlog tasks a commit adds.
//
// It validates the input, keeps the task files the shared backlog
// classification assigned to it, and for every task: reads the task at
// the commit, lists the repository, reads the files the task is likely to
// touch, asks the LLM to implement it, and commits the proposed changes
// to an implement/<task-slug>-<short-sha> branch. It then opens the merge
// request carrying each implementation — a regular MR for a complete one,
// a draft MR for a partial one, handed to the refiner agent on the MR.
//
// Every provider and LLM call runs in an activity; the workflow body only
// sequences them, so it stays deterministic.
func TaskImplementationWorkflow(ctx workflow.Context, in Input) (Result, error) {
	if err := in.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid %s input: %w", WorkflowName, err)
	}
	logger := workflow.GetLogger(ctx)
	logger.Info("task implementation started",
		"repository", in.Repository, "branch", in.Branch,
		"sha", ShortSHA(in.SHA), "task_paths", in.TaskPaths)

	// Re-check the routing decision the workflow started with: the shared
	// backlog classification supplies the commit's task files, and this
	// step keeps only the ones an implementation may act on. A commit on
	// any agent branch (refine/* or implement/*) carries agent-generated
	// work, never a new task (the trigger declines those; this guards
	// manual starts).
	if backlog.IsAgentBranch(in.Branch) {
		reason := "commit is on the agent branch " + in.Branch + " the agents themselves maintain"
		logger.Info("task implementation completed", "sha", ShortSHA(in.SHA), "result", reason)
		return Result{Implemented: true, Status: reason}, nil
	}
	taskPaths := taskPaths(in)
	if len(taskPaths) == 0 {
		reason := "commit adds no backlog task file under " + backlog.Dir
		logger.Info("task implementation completed", "sha", ShortSHA(in.SHA), "result", reason)
		return Result{Implemented: true, Status: reason}, nil
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    10 * time.Minute,
		ScheduleToCloseTimeout: 30 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Minute,
			MaximumAttempts:    5,
		},
	})

	var implementations []TaskImplementation
	for _, path := range taskPaths {
		impl, err := implementTask(ctx, in, path)
		if err != nil {
			return Result{}, err
		}
		implementations = append(implementations, impl)
	}

	// Turn each outcome into the merge request a human sees: a regular
	// MR for a complete implementation, a draft MR with a refiner
	// hand-off for a partial one.
	var mergeRequestURL string
	complete := 0
	for i := range implementations {
		impl := &implementations[i]
		if impl.Complete {
			complete++
		}
		submission, err := submitImplementation(ctx, in, *impl)
		if err != nil {
			return Result{}, err
		}
		impl.MergeRequest = &submission
		if mergeRequestURL == "" {
			mergeRequestURL = submission.URL
		}
		outcome := "complete"
		if !impl.Complete {
			outcome = "partial"
		}
		logger.Info("implementation merge request submitted",
			"task", impl.TaskID, "outcome", outcome,
			"draft", submission.Draft, "mr_number", submission.Number,
			"mr_url", submission.URL, "refiner_comment", submission.RefinerCommentID)
	}

	status := fmt.Sprintf("implemented %d of %d backlog task(s) completely", complete, len(implementations))
	logger.Info("task implementation completed",
		"sha", ShortSHA(in.SHA), "tasks", len(implementations),
		"complete", complete, "partial", len(implementations)-complete,
		"merge_requests", len(implementations))
	return Result{
		Implemented:     true,
		Status:          status,
		TaskPaths:       taskPaths,
		Implementations: implementations,
		MergeRequestURL: mergeRequestURL,
	}, nil
}

// submitImplementation opens the merge request carrying one
// implementation, based on the branch the task commit landed on, and
// hands a partial implementation to the refiner agent.
func submitImplementation(ctx workflow.Context, in Input, impl TaskImplementation) (MergeRequestSubmission, error) {
	var submitted SubmitMergeRequestResult
	if err := workflow.ExecuteActivity(ctx, SubmitMergeRequestName, SubmitMergeRequestInput{
		Repository:     in.Repository,
		Implementation: impl,
		Base:           in.Branch,
		CommitSHA:      in.SHA,
		CommitMessage:  in.Message,
		CommitAuthor:   in.Author,
		ProviderURL:    in.ProviderURL,
	}).Get(ctx, &submitted); err != nil {
		return MergeRequestSubmission{}, fmt.Errorf("submit merge request for %s: %w", impl.TaskID, err)
	}
	return MergeRequestSubmission{
		Number:           submitted.Number,
		Title:            submitted.Title,
		URL:              submitted.URL,
		Draft:            submitted.Draft,
		RefinerCommentID: submitted.RefinerCommentID,
	}, nil
}

// implementTask implements one backlog task: read it, gather the
// repository context, generate the changes, and commit them to the task's
// implementation branch. It returns the task's outcome, including a
// partial one, and an error only when a step failed (so Temporal retries
// that step).
func implementTask(ctx workflow.Context, in Input, path string) (TaskImplementation, error) {
	logger := workflow.GetLogger(ctx)

	var task Task
	if err := workflow.ExecuteActivity(ctx, ReadTaskName, ReadTaskInput{
		Repository: in.Repository,
		Path:       path,
		Ref:        in.SHA,
	}).Get(ctx, &task); err != nil {
		return TaskImplementation{}, fmt.Errorf("read task %s: %w", path, err)
	}

	var listing RepositoryListing
	if err := workflow.ExecuteActivity(ctx, ListRepositoryName, ListRepositoryInput{
		Repository: in.Repository,
		Ref:        in.SHA,
	}).Get(ctx, &listing); err != nil {
		return TaskImplementation{}, fmt.Errorf("list repository for %s: %w", path, err)
	}

	// Read the files the task is most likely to touch: the files it
	// names, then the files near its own path. The model sees the
	// listing for everything else and reports a partial outcome when it
	// needs a file that was not read.
	relevant := relevantFiles(task, listing.Files, path)
	var contents []ReadFile
	if len(relevant) > 0 {
		if err := workflow.ExecuteActivity(ctx, ReadFilesName, ReadFilesInput{
			Repository: in.Repository,
			Paths:      relevant,
			Ref:        in.SHA,
		}).Get(ctx, &contents); err != nil {
			return TaskImplementation{}, fmt.Errorf("read files for %s: %w", path, err)
		}
	}

	var impl Implementation
	if err := workflow.ExecuteActivity(ctx, GenerateImplementationName, GenerateImplementationInput{
		Repository: in.Repository,
		Ref:        in.SHA,
		Task:       task,
		Files:      listing.Files,
		Contents:   contents,
		Truncated:  listing.Truncated,
	}).Get(ctx, &impl); err != nil {
		return TaskImplementation{}, fmt.Errorf("generate implementation for %s: %w", path, err)
	}

	result := TaskImplementation{
		TaskPath:      path,
		TaskID:        task.ID,
		TaskTitle:     task.Title,
		Complete:      impl.Complete,
		Reason:        impl.Reason,
		OpenQuestions: impl.OpenQuestions,
		PromptVersion: impl.PromptVersion,
	}

	// Create the implementation branch from the task commit, then commit
	// the proposed changes to it — each its own activity, so a retry
	// re-runs only the step that failed.
	var branch ImplementationBranch
	if err := workflow.ExecuteActivity(ctx, CreateImplementationBranchName, CreateImplementationBranchInput{
		Repository: in.Repository,
		TaskID:     task.ID,
		TaskTitle:  task.Title,
		FromRef:    in.SHA,
	}).Get(ctx, &branch); err != nil {
		return TaskImplementation{}, fmt.Errorf("create implementation branch for %s: %w", path, err)
	}
	result.Branch = branch.Name

	if len(impl.Changes) == 0 {
		// The model could not propose a safe change (typically a partial
		// outcome). The branch still carries the task, so the draft MR
		// can be opened against it and the refiner has somewhere to
		// hand the work back to.
		logger.Info("implementation proposes no changes", "task", task.ID, "branch", branch.Name)
		return result, nil
	}

	changes := make([]provider.FileChange, 0, len(impl.Changes))
	for _, change := range impl.Changes {
		changes = append(changes, provider.FileChange{Path: change.Path, Content: []byte(change.Content)})
	}
	var commit CommitImplementationResult
	if err := workflow.ExecuteActivity(ctx, CommitImplementationName, CommitImplementationInput{
		Repository: in.Repository,
		Branch:     branch.Name,
		Message:    commitMessage(task, impl),
		Files:      changes,
	}).Get(ctx, &commit); err != nil {
		return TaskImplementation{}, fmt.Errorf("commit implementation for %s: %w", path, err)
	}
	result.ChangedFiles = commit.Files
	result.CommitSHA = commit.SHA

	logger.Info("task implemented",
		"task", task.ID, "complete", impl.Complete,
		"branch", branch.Name, "commit", ShortSHA(commit.SHA),
		"files", len(commit.Files))
	return result, nil
}

// commitMessage is the implementation commit's message: the task id and
// title, and a note when the implementation is partial.
func commitMessage(task Task, impl Implementation) string {
	message := "Implement " + task.ID
	if task.Title != "" {
		message += ": " + task.Title
	}
	if !impl.Complete {
		message += "\n\nPartial implementation: " + impl.Reason
	}
	return message
}

// relevantFiles picks the files to read for a task: the files the task
// content names, then the files near the task's own directory, bounded by
// maxContextFiles and by maxContextFileBytes per file. It never includes
// the task file itself, and it is deterministic (the listing is sorted),
// so a replayed workflow sends the model the same context.
func relevantFiles(task Task, files []FileEntry, taskPath string) []string {
	named := make(map[string]bool)
	for _, f := range files {
		if f.Path != taskPath && strings.Contains(task.Content, f.Path) {
			named[f.Path] = true
		}
	}
	var paths []string
	appendFile := func(f FileEntry) {
		if len(paths) >= maxContextFiles || f.Path == taskPath || f.Size > maxContextFileBytes {
			return
		}
		for _, p := range paths {
			if p == f.Path {
				return
			}
		}
		paths = append(paths, f.Path)
	}
	for _, f := range files {
		if named[f.Path] {
			appendFile(f)
		}
	}
	for _, f := range files {
		appendFile(f)
	}
	return paths
}

// ShortSHA shortens a commit hash for logging.
func ShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// FirstLine returns the first line of a commit message for logging.
func FirstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
