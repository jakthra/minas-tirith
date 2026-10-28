// Package userstoryreview is the first worker template: the TASK-2
// autonomous refinement agent. A commit event that refines a user story
// starts userStoryReviewWorkflow with the commit's payload; the workflow
// validates the payload, analyzes the commit, and for every user story
// the commit adds or modifies it generates implementation tickets and
// opens a refinement merge request carrying them. Comment events on
// those merge requests start mrCommentWorkflow (mrcomment.go), which
// applies the user's feedback to the tickets.
package userstoryreview

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"gopkg.in/yaml.v3"
)

// WorkflowName is the Temporal workflow type name for the user story
// review workflow.
const WorkflowName = "userStoryReviewWorkflow"

// RefinementBranchPrefix prefixes every refinement branch the agent
// creates (see RefinementBranchName). Commits on these branches are the
// agent's own work, never new user stories to refine.
const RefinementBranchPrefix = "refine/"

// Activity type names.
const (
	// AnalyzeCommitName is the Temporal activity type name for the commit
	// analysis activity.
	AnalyzeCommitName = "userStoryReview.analyzeCommit"
	// FetchStoryName is the Temporal activity type name for the activity
	// that fetches a user story's content from the provider.
	FetchStoryName = "userStoryReview.fetchStory"
	// GenerateTicketsName is the Temporal activity type name for the
	// activity that generates implementation tickets from a user story.
	GenerateTicketsName = "userStoryReview.generateTickets"
	// CreateRefinementBranchName is the Temporal activity type name for
	// the activity that creates the refinement branch on the target
	// repository.
	CreateRefinementBranchName = "userStoryReview.createRefinementBranch"
	// CommitTicketsName is the Temporal activity type name for the
	// activity that writes the generated tickets as backlog.md files
	// and commits them to the refinement branch.
	CommitTicketsName = "userStoryReview.commitTickets"
	// OpenMergeRequestName is the Temporal activity type name for the
	// activity that opens the refinement merge request on the provider.
	OpenMergeRequestName = "userStoryReview.openMergeRequest"
)

// Input is the commit event payload the workflow receives: which
// repository and branch the commit landed on, and the commit itself.
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
}

// Validate reports whether the payload identifies a reviewable commit.
func (in Input) Validate() error {
	if strings.TrimSpace(in.SHA) == "" {
		return errors.New("commit sha is required")
	}
	if strings.TrimSpace(in.Repository) == "" {
		return errors.New("repository is required")
	}
	return nil
}

// Analysis is what the workflow learned by inspecting the commit.
type Analysis struct {
	// NeedsRefinement reports whether the commit refines a user story and
	// refinement work should follow.
	NeedsRefinement bool `json:"needs_refinement"`
	// Reason explains the decision (visible in the workflow result and
	// logs).
	Reason string `json:"reason"`
	// StoryPaths lists the user story files (under backlog/tasks/) the
	// commit added or modified, when NeedsRefinement is true.
	StoryPaths []string `json:"story_paths,omitempty"`
}

// Ticket is one implementation ticket generated from a user story.
type Ticket struct {
	// Title is the ticket's short, action-oriented title.
	Title string `json:"title"`
	// Description is the ticket's body: context, what needs to happen,
	// and acceptance criteria, in backlog.md markdown format.
	Description string `json:"description"`
}

// Submission records one refinement merge request the workflow opened:
// the story it refines, the branch carrying the ticket files, and the
// merge request itself. TASK-11 correlates comment events back to the
// refinement run through the MR number.
type Submission struct {
	// StoryPath is the story file the commit added or modified.
	StoryPath string `json:"story_path"`
	// StoryID is the story's backlog id from its frontmatter, e.g.
	// "TASK-001" (empty when the story carries none).
	StoryID string `json:"story_id,omitempty"`
	// StoryTitle is the story's title from its frontmatter (empty when
	// the story carries none).
	StoryTitle string `json:"story_title,omitempty"`
	// Branch is the refinement branch the ticket files were committed
	// to.
	Branch string `json:"branch"`
	// MRNumber is the merge request's number on the provider.
	MRNumber int64 `json:"mr_number"`
	// MRTitle is the merge request's title.
	MRTitle string `json:"mr_title"`
	// MRURL is the merge request's page on the provider.
	MRURL string `json:"mr_url"`
	// TicketFiles lists the ticket files committed to Branch.
	TicketFiles []string `json:"ticket_files"`
	// Questions lists the questions the agent has for the user, as
	// asked in the merge request.
	Questions []string `json:"questions,omitempty"`
}

// Result is the outcome of one user story review run.
type Result struct {
	// Reviewed is true when the workflow completed: the commit was
	// analyzed.
	Reviewed bool `json:"reviewed"`
	// NeedsRefinement mirrors the analysis decision.
	NeedsRefinement bool `json:"needs_refinement"`
	// Status summarizes what the run did.
	Status string `json:"status"`
	// Tickets are the implementation tickets generated from the detected
	// user stories, when NeedsRefinement is true.
	Tickets []Ticket `json:"tickets,omitempty"`
	// Submissions records one merge request per refined story, for the
	// user to review and for TASK-11 to correlate comments against.
	Submissions []Submission `json:"submissions,omitempty"`
}

// UserStoryReviewWorkflow reviews a commit that may refine a user story.
// It analyzes the commit for story file changes; for every story detected
// it fetches the story content, generates implementation tickets, and
// opens a refinement merge request: a refine/* branch carrying the tickets
// as backlog.md files, proposed into the branch the commit landed on.
// Comments on that merge request are the user's entrypoint for adjusting
// the refinement; mrCommentWorkflow (mrcomment.go) applies them.
func UserStoryReviewWorkflow(ctx workflow.Context, in Input) (Result, error) {
	if err := in.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid %s input: %w", WorkflowName, err)
	}
	logger := workflow.GetLogger(ctx)
	logger.Info("user story review started",
		"repository", in.Repository, "branch", in.Branch,
		"sha", ShortSHA(in.SHA), "message", FirstLine(in.Message))

	// Fail the workflow on a bad input payload rather than retrying it:
	// retries cannot fix an event that carries no reviewable commit.
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

	var analysis Analysis
	if err := workflow.ExecuteActivity(ctx, AnalyzeCommitName, in).Get(ctx, &analysis); err != nil {
		return Result{}, fmt.Errorf("analyze commit %s: %w", ShortSHA(in.SHA), err)
	}

	if !analysis.NeedsRefinement {
		logger.Info("user story review completed", "sha", ShortSHA(in.SHA), "result", analysis.Reason)
		return Result{
			Reviewed:        true,
			NeedsRefinement: false,
			Status:          analysis.Reason,
		}, nil
	}

	logger.Info("commit needs refinement",
		"sha", ShortSHA(in.SHA), "reason", analysis.Reason,
		"story_paths", analysis.StoryPaths)

	// Fetch each detected story, generate implementation tickets, and
	// open a refinement merge request per story. All provider and LLM
	// API calls run inside activities (AC5).
	var allTickets []Ticket
	var submissions []Submission
	nextID := 0 // threads ticket ids across the stories of one commit
	for _, storyPath := range analysis.StoryPaths {
		var storyContent string
		fetchInput := FetchStoryInput{
			Repository: in.Repository,
			Path:       storyPath,
			Ref:        in.Branch,
		}
		if err := workflow.ExecuteActivity(ctx, FetchStoryName, fetchInput).Get(ctx, &storyContent); err != nil {
			return Result{}, fmt.Errorf("fetch story %s: %w", storyPath, err)
		}

		genInput := GenerateTicketsInput{
			StoryPath:    storyPath,
			StoryContent: storyContent,
		}
		var gen GenerateTicketsOutput
		if err := workflow.ExecuteActivity(ctx, GenerateTicketsName, genInput).Get(ctx, &gen); err != nil {
			return Result{}, fmt.Errorf("generate tickets for %s: %w", storyPath, err)
		}
		allTickets = append(allTickets, gen.Tickets...)

		if len(gen.Tickets) == 0 {
			logger.Info("no tickets generated; skipping merge request", "story_path", storyPath)
			continue
		}

		content := mrContent{
			storyPath: storyPath,
			meta:      parseStoryMeta(storyContent),
			commit:    in,
			tickets:   gen.Tickets,
			questions: gen.Questions,
		}

		// Create the refinement branch, commit the ticket files, and
		// open the merge request — each its own activity, so a retry
		// re-runs only the step that failed.
		branch := RefinementBranchName(storyPath, in.SHA)
		var rb RefinementBranch
		if err := workflow.ExecuteActivity(ctx, CreateRefinementBranchName, CreateRefinementBranchInput{
			Repository: in.Repository,
			Branch:     branch,
			FromRef:    in.Branch,
		}).Get(ctx, &rb); err != nil {
			return Result{}, fmt.Errorf("create refinement branch %s: %w", branch, err)
		}

		var ct CommitTicketsResult
		if err := workflow.ExecuteActivity(ctx, CommitTicketsName, CommitTicketsInput{
			Repository: in.Repository,
			Branch:     rb.Name,
			StoryPath:  storyPath,
			Tickets:    gen.Tickets,
			IDHint:     nextID,
		}).Get(ctx, &ct); err != nil {
			return Result{}, fmt.Errorf("commit tickets for %s: %w", storyPath, err)
		}
		nextID = ct.NextID
		content.ticketFiles = ct.TicketFiles

		var mr MergeRequestInfo
		if err := workflow.ExecuteActivity(ctx, OpenMergeRequestName, OpenMergeRequestInput{
			Repository:  in.Repository,
			Title:       buildMRTitle(content),
			Description: buildMRDescription(content),
			Head:        rb.Name,
			Base:        in.Branch,
		}).Get(ctx, &mr); err != nil {
			return Result{}, fmt.Errorf("open merge request for %s: %w", storyPath, err)
		}

		logger.Info("refinement merge request opened",
			"story_path", storyPath, "branch", rb.Name,
			"mr_number", mr.Number, "mr_url", mr.URL)
		submissions = append(submissions, Submission{
			StoryPath:   storyPath,
			StoryID:     content.meta.ID,
			StoryTitle:  content.meta.Title,
			Branch:      rb.Name,
			MRNumber:    mr.Number,
			MRTitle:     mr.Title,
			MRURL:       mr.URL,
			TicketFiles: ct.TicketFiles,
			Questions:   gen.Questions,
		})
	}

	logger.Info("user story review completed",
		"sha", ShortSHA(in.SHA),
		"stories", len(analysis.StoryPaths),
		"tickets", len(allTickets),
		"merge_requests", len(submissions))
	return Result{
		Reviewed:        true,
		NeedsRefinement: true,
		Status:          analysis.Reason,
		Tickets:         allTickets,
		Submissions:     submissions,
	}, nil
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

// storyStem returns the story file's base name without directory or
// .md extension, e.g. "task-001 - Track-watering-history-per-plant".
func storyStem(storyPath string) string {
	stem := storyPath
	if i := strings.LastIndex(stem, "/"); i >= 0 {
		stem = stem[i+1:]
	}
	return strings.TrimSuffix(stem, ".md")
}

// RefinementBranchName returns the refinement branch name for one story
// commit: refine/<story-slug>-<short-sha>.
func RefinementBranchName(storyPath, sha string) string {
	return RefinementBranchPrefix + branchSlug(storyStem(storyPath)) + "-" + ShortSHA(sha)
}

// branchSlug turns a story stem into a lowercase, branch-friendly slug:
// anything but letters and digits collapses to single dashes.
func branchSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// storyMeta identifies a story for the merge request: the id and title
// from its backlog.md frontmatter.
type storyMeta struct {
	ID    string
	Title string
}

// parseStoryMeta extracts the story's id and title from the backlog.md
// frontmatter of its content. A story without parseable frontmatter
// yields empty fields; callers fall back to the file name.
func parseStoryMeta(content string) storyMeta {
	var meta storyMeta
	block, ok := frontmatterBlock(content)
	if !ok {
		return meta
	}
	var fm struct {
		ID    string `yaml:"id"`
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return meta
	}
	meta.ID = strings.TrimSpace(fm.ID)
	meta.Title = strings.TrimSpace(fm.Title)
	return meta
}

// frontmatterBlock returns the YAML frontmatter of a backlog.md file:
// the lines between the opening and closing --- lines. It reports
// false when the content has no frontmatter.
func frontmatterBlock(content string) (string, bool) {
	s := strings.TrimLeft(content, " \t\r\n")
	lines := strings.Split(s, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], "\r") != "---" {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			return strings.Join(lines[1:i], "\n"), true
		}
	}
	return "", false
}

// mrContent carries everything the refinement merge request title and
// description summarize: the story, its source commit, and the
// refinement the agent produced for it.
type mrContent struct {
	storyPath   string
	meta        storyMeta
	commit      Input
	tickets     []Ticket
	ticketFiles []string
	questions   []string
}

// buildMRTitle returns the merge request title: the refined story's id
// and title, falling back to the story file name when the story carries
// no frontmatter. Every refinement MR is titled "Refine ..." — the
// pattern the comment trigger filters on (RefinementTitlePrefix).
func buildMRTitle(c mrContent) string {
	title := c.meta.Title
	if title == "" {
		title = storyStem(c.storyPath)
	}
	if c.meta.ID != "" {
		return RefinementTitlePrefix + c.meta.ID + ": " + title
	}
	return RefinementTitlePrefix + "user story: " + title
}

// buildMRDescription returns the merge request body: a summary of the
// refinement, the tickets with their file paths, and the questions the
// agent has for the user.
func buildMRDescription(c mrContent) string {
	title := c.meta.Title
	if title == "" {
		title = storyStem(c.storyPath)
	}

	var b strings.Builder
	if c.meta.ID != "" {
		fmt.Fprintf(&b, "Refines user story **%s: %s** (`%s`).\n\n", c.meta.ID, title, c.storyPath)
	} else {
		fmt.Fprintf(&b, "Refines the user story **%s** (`%s`).\n\n", title, c.storyPath)
	}
	fmt.Fprintf(&b, "Triggered by commit `%s` \"%s\" by %s on branch `%s`.\n\n",
		ShortSHA(c.commit.SHA), FirstLine(c.commit.Message), c.commit.Author, c.commit.Branch)
	fmt.Fprintf(&b, "The refinement agent broke the story into %d implementation ticket(s):\n\n",
		len(c.tickets))
	for i, t := range c.tickets {
		file := ""
		if i < len(c.ticketFiles) {
			file = " — `" + c.ticketFiles[i] + "`"
		}
		fmt.Fprintf(&b, "- **%s**%s\n", t.Title, file)
	}
	fmt.Fprintf(&b, "\nReview the tickets, adjust them as needed, and merge this MR to accept them. "+
		"Comment here with feedback; the agent will respond and update the tickets.\n")
	if len(c.questions) > 0 {
		b.WriteString("\n## Questions for you\n\n")
		for i, q := range c.questions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, q)
		}
	}
	return b.String()
}
