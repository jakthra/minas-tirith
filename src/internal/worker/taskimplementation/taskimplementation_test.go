package taskimplementation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
	"minas-tirith/internal/worker/backlog"
)

const (
	repo      = "root/BeanieGenie"
	commitSHA = "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0"

	taskPath  = "backlog/tasks/task-19 - Implement login.md"
	storyPath = "backlog/tasks/task-2 - Autonomous refinement agent.md"
)

// newTestEnv registers the template's workflow and activities with a test
// environment, mirroring how the runner registers it with a real Temporal
// worker. The workflow's activities are mocked by the caller: the
// registrations only declare the activity types the environment accepts
// mocks for.
func newTestEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	tmpl := Template(nil, nil)
	for _, wf := range tmpl.Workflows() {
		env.RegisterWorkflowWithOptions(wf.Fn, workflow.RegisterOptions{Name: wf.Name})
	}
	for _, a := range tmpl.Activities() {
		env.RegisterActivityWithOptions(a.Fn, activity.RegisterOptions{Name: a.Name})
	}
	return env
}

// newIntegrationEnv registers the workflow and every activity with a test
// environment, wired to the given provider and LLM fakes, the way the
// runner registers them with a real Temporal worker.
func newIntegrationEnv(t *testing.T, p provider.Client, l llm.Client) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	tmpl := Template(p, l)
	for _, wf := range tmpl.Workflows() {
		env.RegisterWorkflowWithOptions(wf.Fn, workflow.RegisterOptions{Name: wf.Name})
	}
	for _, a := range tmpl.Activities() {
		env.RegisterActivityWithOptions(a.Fn, activity.RegisterOptions{Name: a.Name})
	}
	return env
}

func commitEvent(added, modified []string) event.Event {
	return event.Event{
		Kind:       event.KindCommit,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: repo,
		Commit: &event.Commit{
			SHA:      commitSHA,
			Branch:   "main",
			Message:  "Add backlog files\n\nBody",
			URL:      "http://localhost:3000/" + repo + "/commit/" + commitSHA,
			Author:   event.Person{Username: "root"},
			Added:    added,
			Modified: modified,
		},
	}
}

// route runs the trigger's routing decision for an event and a
// classification, the way the event bridge does: the classification is
// the decoded value the trigger's ClassifyResult factory created.
func route(t *testing.T, ev event.Event, c any) (Input, bool) {
	t.Helper()
	var tr worker.Trigger
	for _, candidate := range Template(nil, nil).Triggers() {
		if candidate.Kind == event.KindCommit {
			tr = candidate
		}
	}
	if !tr.IsActivityTrigger() {
		t.Fatal("the commit trigger is not an activity trigger")
	}
	if tr.ClassifyResult == nil {
		t.Fatal("the commit trigger has no result factory")
	}
	got, ok := tr.Route(ev, c)
	if !ok {
		return Input{}, false
	}
	in, isInput := got.(Input)
	if !isInput {
		t.Fatalf("routed input type = %T, want Input", got)
	}
	return in, true
}

// --- Trigger routing (AC1, AC2, AC3, AC4) ---

func TestRouteFromClassification(t *testing.T) {
	tests := []struct {
		name           string
		event          event.Event
		classification any
		want           bool
		wantTasks      []string
	}{
		{
			name:           "added task is routed to the implementation agent",
			event:          commitEvent([]string{taskPath}, nil),
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           true,
			wantTasks:      []string{taskPath},
		},
		{
			name:           "modified task is routed to the implementation agent",
			event:          commitEvent(nil, []string{taskPath}),
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           true,
			wantTasks:      []string{taskPath},
		},
		{
			name:           "story-only commit is not routed here",
			event:          commitEvent([]string{storyPath}, nil),
			classification: &backlog.Classification{StoryPaths: []string{storyPath}},
			want:           false,
		},
		{
			name:           "mixed commit is routed with only the task files",
			event:          commitEvent([]string{storyPath, taskPath}, nil),
			classification: &backlog.Classification{TaskPaths: []string{taskPath}, StoryPaths: []string{storyPath}},
			want:           true,
			wantTasks:      []string{taskPath},
		},
		{
			name:           "non-backlog commit is not routed here",
			event:          commitEvent([]string{"src/main.go"}, nil),
			classification: &backlog.Classification{Reason: "no backlog files"},
			want:           false,
		},
		{
			name: "commit on the agent's implementation branch is declined",
			event: func() event.Event {
				ev := commitEvent([]string{taskPath}, nil)
				ev.Commit.Branch = ImplementationBranchPrefix + "task-019-8ef0de217a18"
				return ev
			}(),
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           false,
		},
		{
			name: "commit on the review agent's refinement branch is declined too",
			event: func() event.Event {
				ev := commitEvent([]string{taskPath}, nil)
				ev.Commit.Branch = backlog.RefinementBranchPrefix + "task-009-track-watering-history-per-plant-8395b2548be1"
				return ev
			}(),
			// Any agent branch (refine/* or implement/*) carries
			// agent-generated files: neither agent may start from it.
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           false,
		},
		{
			name: "missing sha is declined",
			event: func() event.Event {
				ev := commitEvent([]string{taskPath}, nil)
				ev.Commit.SHA = ""
				return ev
			}(),
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           false,
		},
		{
			name:           "nil commit is declined",
			event:          event.Event{Kind: event.KindCommit, Repository: repo},
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           false,
		},
		{
			name:           "comment event is declined",
			event:          event.Event{Kind: event.KindComment, Repository: repo, Commit: commitEvent([]string{taskPath}, nil).Commit},
			classification: &backlog.Classification{TaskPaths: []string{taskPath}},
			want:           false,
		},
		{
			// The classification arrives as the decoded pointer the
			// trigger's ClassifyResult factory declared; anything else
			// is a wiring bug and is declined.
			name:           "unexpected classification type is declined",
			event:          commitEvent([]string{taskPath}, nil),
			classification: backlog.Classification{},
			want:           false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, ok := route(t, tt.event, tt.classification)
			if ok != tt.want {
				t.Fatalf("route ok = %v, want %v", ok, tt.want)
			}
			if !ok {
				return
			}
			// The input carries everything needed to act on the task.
			if in.Repository != repo || in.Branch != tt.event.Commit.Branch || in.SHA != commitSHA {
				t.Errorf("input context = (%q, %q, %q)", in.Repository, in.Branch, in.SHA)
			}
			if strings.Join(in.TaskPaths, ",") != strings.Join(tt.wantTasks, ",") {
				t.Errorf("task paths = %v, want %v", in.TaskPaths, tt.wantTasks)
			}
			if err := in.Validate(); err != nil {
				t.Errorf("Validate() = %v", err)
			}
		})
	}
}

func TestRouteDeclinesMismatchedClassification(t *testing.T) {
	// A classification that is not the shared one yields no input.
	ev := commitEvent([]string{taskPath}, nil)
	tr := Template(nil, nil).Triggers()[0]
	if _, ok := tr.Route(ev, "not a classification"); ok {
		t.Error("route accepted a foreign classification")
	}
}

func TestTemplateRegistration(t *testing.T) {
	tmpl := Template(nil, nil)
	if tmpl.Name() != "taskimplementation" {
		t.Errorf("Name() = %q", tmpl.Name())
	}
	acts := tmpl.Activities()
	wantNames := []string{
		CheckCommitName,
		ReadTaskName,
		ListRepositoryName,
		ReadFilesName,
		SearchRepositoryName,
		RequestContextName,
		GenerateImplementationName,
		CreateImplementationBranchName,
		CommitImplementationName,
		SubmitMergeRequestName,
	}
	if len(acts) != len(wantNames) {
		t.Fatalf("activities = %d, want %d (the routing check, the repository tooling, and the LLM steps)", len(acts), len(wantNames))
	}
	for i, want := range wantNames {
		if acts[i].Name != want || acts[i].Fn == nil {
			t.Errorf("activities[%d] = %+v, want %q with a function", i, acts[i], want)
		}
	}

	// The template registers through the existing mechanism: a starter
	// accepts it and exposes its commit trigger, which routes on the
	// shared backlog classification.
	starter := worker.NewStarter(nil, "minas-tirith")
	if err := starter.Register(tmpl); err != nil {
		t.Fatalf("Register: %v", err)
	}
	triggers := starter.Triggers()
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	if triggers[0].Kind != event.KindCommit || triggers[0].Workflow != WorkflowName {
		t.Errorf("trigger = %+v, want commit -> %s", triggers[0], WorkflowName)
	}
	if !triggers[0].IsActivityTrigger() || triggers[0].Classify != backlog.ClassifyCommitActivity {
		t.Errorf("trigger = %+v, want an activity trigger on %s", triggers[0], backlog.ClassifyCommitActivity)
	}

	// Registering the same template twice is rejected, like any template.
	if err := starter.Register(tmpl); err == nil {
		t.Error("second Register: want error, got nil")
	}
}

// --- Workflow execution (AC1, AC2) ---

func TestWorkflowImplementsTask(t *testing.T) {
	// TASK-23 AC4/AC5: the workflow reads the task, gathers repository
	// context, generates the implementation, and commits it to an
	// implement/* branch, reporting the branch and the changed files.
	env := newTestEnv(t)
	env.OnActivity(ReadTaskName, mock.Anything, ReadTaskInput{Repository: repo, Path: taskPath, Ref: commitSHA}).
		Return(Task{Path: taskPath, Ref: commitSHA, ID: "TASK-019", Title: "Implement login", Content: "# Task"}, nil)
	env.OnActivity(ListRepositoryName, mock.Anything, mock.Anything).
		Return(RepositoryListing{Files: []FileEntry{{Path: "src/main.go", Size: 10}}}, nil)
	env.OnActivity(ReadFilesName, mock.Anything, mock.Anything).
		Return([]ReadFile{{Path: "src/main.go", Content: "package main\n"}}, nil)
	env.OnActivity(RequestContextName, mock.Anything, mock.Anything).
		Return(ContextRound{}, nil)
	env.OnActivity(GenerateImplementationName, mock.Anything, mock.Anything).
		Return(Implementation{
			TaskID:        "TASK-019",
			Complete:      true,
			Changes:       []ImplementationChange{{Path: "src/login.go", Content: "package login\n"}},
			PromptVersion: ImplementationPromptVersion,
		}, nil)
	env.OnActivity(CreateImplementationBranchName, mock.Anything, mock.Anything).
		Return(ImplementationBranch{Name: "implement/task-019-implement-login-" + ShortSHA(commitSHA)}, nil)
	env.OnActivity(CommitImplementationName, mock.Anything, mock.Anything).
		Return(CommitImplementationResult{SHA: "commitsha", Files: []string{"src/login.go"}}, nil)
	env.OnActivity(SubmitMergeRequestName, mock.Anything, mock.Anything).
		Return(SubmitMergeRequestResult{Number: 1, Title: "mr", URL: "http://fake/mr/1"}, nil)

	env.ExecuteWorkflow(WorkflowName, Input{
		Repository: repo,
		Branch:     "main",
		SHA:        commitSHA,
		TaskPaths:  []string{taskPath},
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if !result.Implemented {
		t.Error("Implemented = false, want true")
	}
	if len(result.TaskPaths) != 1 || result.TaskPaths[0] != taskPath {
		t.Errorf("task paths = %v, want [%s]", result.TaskPaths, taskPath)
	}
	if len(result.Implementations) != 1 {
		t.Fatalf("implementations = %d, want 1", len(result.Implementations))
	}
	impl := result.Implementations[0]
	if !impl.Complete {
		t.Errorf("Complete = false, want true (reason %q)", impl.Reason)
	}
	if impl.TaskID != "TASK-019" || impl.TaskPath != taskPath {
		t.Errorf("task = %q/%q", impl.TaskID, impl.TaskPath)
	}
	if impl.Branch != "implement/task-019-implement-login-"+ShortSHA(commitSHA) {
		t.Errorf("branch = %q", impl.Branch)
	}
	if len(impl.ChangedFiles) != 1 || impl.ChangedFiles[0] != "src/login.go" {
		t.Errorf("changed files = %v", impl.ChangedFiles)
	}
	if impl.CommitSHA != "commitsha" {
		t.Errorf("commit sha = %q", impl.CommitSHA)
	}
	if result.MergeRequestURL != "http://fake/mr/1" {
		t.Errorf("merge request URL = %q, want the opened MR", result.MergeRequestURL)
	}
	if impl.MergeRequest == nil || impl.MergeRequest.Number != 1 {
		t.Errorf("implementation merge request = %+v, want the opened MR", impl.MergeRequest)
	}
	if strings.Contains(result.Status, "not implemented yet") {
		t.Errorf("status = %q, want the placeholder replaced", result.Status)
	}
}

func TestWorkflowReportsPartialImplementation(t *testing.T) {
	// AC2/AC5: a model that cannot fulfill the task reports a partial
	// outcome; the workflow reports it with the reason and the open
	// questions, and still commits the sound changes it produced.
	env := newTestEnv(t)
	env.OnActivity(ReadTaskName, mock.Anything, mock.Anything).
		Return(Task{Path: taskPath, ID: "TASK-019", Title: "Implement login"}, nil)
	env.OnActivity(ListRepositoryName, mock.Anything, mock.Anything).
		Return(RepositoryListing{}, nil)
	env.OnActivity(ReadFilesName, mock.Anything, mock.Anything).
		Return([]ReadFile{}, nil)
	env.OnActivity(RequestContextName, mock.Anything, mock.Anything).
		Return(ContextRound{}, nil)
	env.OnActivity(GenerateImplementationName, mock.Anything, mock.Anything).
		Return(Implementation{
			TaskID:        "TASK-019",
			Complete:      false,
			Reason:        "the task does not say which auth provider to use",
			OpenQuestions: []string{"Which auth provider?", "Should sessions expire?"},
			Changes:       []ImplementationChange{{Path: "src/login.go", Content: "package login\n"}},
			PromptVersion: ImplementationPromptVersion,
		}, nil)
	env.OnActivity(CreateImplementationBranchName, mock.Anything, mock.Anything).
		Return(ImplementationBranch{Name: "implement/task-019-implement-login-" + ShortSHA(commitSHA)}, nil)
	env.OnActivity(CommitImplementationName, mock.Anything, mock.Anything).
		Return(CommitImplementationResult{SHA: "commitsha", Files: []string{"src/login.go"}}, nil)
	env.OnActivity(SubmitMergeRequestName, mock.Anything, mock.Anything).
		Return(SubmitMergeRequestResult{Number: 1, Title: "mr", URL: "http://fake/mr/1"}, nil)

	env.ExecuteWorkflow(WorkflowName, Input{
		Repository: repo, Branch: "main", SHA: commitSHA, TaskPaths: []string{taskPath},
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if len(result.Implementations) != 1 {
		t.Fatalf("implementations = %d, want 1", len(result.Implementations))
	}
	impl := result.Implementations[0]
	if impl.Complete {
		t.Error("Complete = true, want a partial implementation")
	}
	if !strings.Contains(impl.Reason, "auth provider") {
		t.Errorf("reason = %q, want the model's reason", impl.Reason)
	}
	if len(impl.OpenQuestions) != 2 {
		t.Errorf("open questions = %v, want 2", impl.OpenQuestions)
	}
	if impl.Branch == "" || len(impl.ChangedFiles) != 1 {
		t.Errorf("partial implementation = %+v, want the branch and the sound changes", impl)
	}
	if !strings.Contains(result.Status, "0 of 1") {
		t.Errorf("status = %q, want the partial count", result.Status)
	}
}

func TestWorkflowPartialWithoutChangesKeepsBranch(t *testing.T) {
	// A partial outcome with no safe change still creates the branch, so
	// the draft MR has somewhere to point; nothing is committed.
	env := newTestEnv(t)
	env.OnActivity(ReadTaskName, mock.Anything, mock.Anything).
		Return(Task{Path: taskPath, ID: "TASK-019", Title: "Implement login"}, nil)
	env.OnActivity(ListRepositoryName, mock.Anything, mock.Anything).
		Return(RepositoryListing{}, nil)
	env.OnActivity(ReadFilesName, mock.Anything, mock.Anything).
		Return([]ReadFile{}, nil)
	env.OnActivity(RequestContextName, mock.Anything, mock.Anything).
		Return(ContextRound{}, nil)
	env.OnActivity(GenerateImplementationName, mock.Anything, mock.Anything).
		Return(Implementation{TaskID: "TASK-019", Reason: "the task contradicts the code"}, nil)
	env.OnActivity(CreateImplementationBranchName, mock.Anything, mock.Anything).
		Return(ImplementationBranch{Name: "implement/task-019-implement-login-" + ShortSHA(commitSHA)}, nil)
	env.OnActivity(SubmitMergeRequestName, mock.Anything, mock.Anything).
		Return(SubmitMergeRequestResult{Number: 1, Title: "mr", URL: "http://fake/mr/1", Draft: true, RefinerCommentID: 7}, nil)

	env.ExecuteWorkflow(WorkflowName, Input{
		Repository: repo, Branch: "main", SHA: commitSHA, TaskPaths: []string{taskPath},
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	impl := result.Implementations[0]
	if impl.Complete || impl.Reason == "" {
		t.Errorf("implementation = %+v, want a partial outcome with a reason", impl)
	}
	if impl.Branch == "" || len(impl.ChangedFiles) != 0 || impl.CommitSHA != "" {
		t.Errorf("implementation = %+v, want the branch and no commit", impl)
	}
	env.AssertNotCalled(t, CommitImplementationName, mock.Anything, mock.Anything)
}

func TestWorkflowKeepsOnlyBacklogTaskPaths(t *testing.T) {
	// The workflow re-verifies the routed paths: non-backlog paths are
	// dropped, duplicates are reported once.
	env := newTestEnv(t)
	env.OnActivity(ReadTaskName, mock.Anything, mock.Anything).
		Return(Task{Path: taskPath, ID: "TASK-019", Title: "Implement login"}, nil)
	env.OnActivity(ListRepositoryName, mock.Anything, mock.Anything).Return(RepositoryListing{}, nil)
	env.OnActivity(ReadFilesName, mock.Anything, mock.Anything).Return([]ReadFile{}, nil)
	env.OnActivity(RequestContextName, mock.Anything, mock.Anything).
		Return(ContextRound{}, nil)
	env.OnActivity(GenerateImplementationName, mock.Anything, mock.Anything).
		Return(Implementation{TaskID: "TASK-019", Complete: true}, nil)
	env.OnActivity(CreateImplementationBranchName, mock.Anything, mock.Anything).
		Return(ImplementationBranch{Name: "implement/task-019-" + ShortSHA(commitSHA)}, nil)
	env.OnActivity(SubmitMergeRequestName, mock.Anything, mock.Anything).
		Return(SubmitMergeRequestResult{Number: 1, Title: "mr", URL: "http://fake/mr/1"}, nil)
	env.ExecuteWorkflow(WorkflowName, Input{
		Repository: repo,
		Branch:     "main",
		SHA:        commitSHA,
		TaskPaths:  []string{"src/main.go", taskPath, taskPath},
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if len(result.TaskPaths) != 1 || result.TaskPaths[0] != taskPath {
		t.Errorf("task paths = %v, want [%s]", result.TaskPaths, taskPath)
	}
}

func TestWorkflowOnOwnBranchCompletesWithoutWork(t *testing.T) {
	// A manual start on the agent's own branch does not re-trigger it.
	env := newTestEnv(t)
	env.ExecuteWorkflow(WorkflowName, Input{
		Repository: repo,
		Branch:     ImplementationBranchPrefix + "task-019-8ef0de217a18",
		SHA:        commitSHA,
		TaskPaths:  []string{taskPath},
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if len(result.TaskPaths) != 0 {
		t.Errorf("task paths = %v, want none", result.TaskPaths)
	}
	if !strings.Contains(result.Status, ImplementationBranchPrefix) {
		t.Errorf("status = %q, want the agent-branch reason", result.Status)
	}
}

func TestWorkflowRejectsInvalidInput(t *testing.T) {
	env := newTestEnv(t)
	env.ExecuteWorkflow(WorkflowName, Input{Repository: repo, Branch: "main"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("workflow with no SHA and no task paths: want error, got nil")
	}
}

// --- Routing check activity (AC4) ---

func TestCheckCommit(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		paths  []string
		want   bool
	}{
		{
			name:  "task file is accepted",
			paths: []string{taskPath},
			want:  true,
		},
		{
			name:  "non-backlog paths are declined",
			paths: []string{"src/main.go"},
		},
		{
			name:   "the agent's own branch is declined",
			branch: ImplementationBranchPrefix + "task-019-8ef0de217a18",
			paths:  []string{taskPath},
		},
		{
			name:   "the review agent's refinement branch is declined",
			branch: backlog.RefinementBranchPrefix + "task-009-track-watering-history-per-plant-8395b2548be1",
			paths:  []string{taskPath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewActivities(nil, nil)
			got, err := a.CheckCommit(t.Context(), Input{
				Repository: repo,
				Branch:     tt.branch,
				SHA:        commitSHA,
				TaskPaths:  tt.paths,
			})
			if err != nil {
				t.Fatalf("CheckCommit: %v", err)
			}
			if got != tt.want {
				t.Errorf("CheckCommit = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckCommitRejectsInvalidInput(t *testing.T) {
	a := NewActivities(nil, nil)
	if _, err := a.CheckCommit(t.Context(), Input{Repository: repo}); err == nil {
		t.Error("CheckCommit accepted an input without a sha or task paths")
	}
}
