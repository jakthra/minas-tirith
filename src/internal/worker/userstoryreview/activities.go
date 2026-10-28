package userstoryreview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.temporal.io/sdk/activity"
	"gopkg.in/yaml.v3"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// storyDir is the repository directory holding user stories and backlog
// tasks in backlog.md markdown format.
const storyDir = "backlog/tasks/"

// Activities holds the dependencies the user story review activities
// need: the provider client for repository access and the LLM client for
// ticket generation. Its methods are registered as Temporal activities,
// keeping every provider and LLM API call inside an activity (Temporal
// determinism preserved).
type Activities struct {
	provider provider.Client
	llm      llm.Client
}

// NewActivities returns the activity set wired to the given provider and
// LLM clients. Nil clients are accepted (the listener registers the
// template without executing activities); the methods that need a client
// fail with a clear error when called without one.
func NewActivities(p provider.Client, l llm.Client) *Activities {
	return &Activities{provider: p, llm: l}
}

// AnalyzeCommit inspects a commit's file changes and reports whether it
// refines a user story: a commit that adds or modifies a file under
// backlog/tasks/ (a backlog.md markdown file) needs refinement.
func (a *Activities) AnalyzeCommit(ctx context.Context, in Input) (Analysis, error) {
	if err := in.Validate(); err != nil {
		return Analysis{}, err
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("analyzing commit for user story refinement",
			"repository", in.Repository, "branch", in.Branch,
			"sha", ShortSHA(in.SHA), "message", FirstLine(in.Message))
	}
	return analyzeCommit(in)
}

// analyzeCommit is the pure logic of AnalyzeCommit, separated so unit
// tests can exercise it without a Temporal activity context.
func analyzeCommit(in Input) (Analysis, error) {
	if err := in.Validate(); err != nil {
		return Analysis{}, err
	}

	// A commit on one of the agent's own refinement branches carries
	// generated ticket files, never a new user story. The trigger
	// declines these commits; this guards manual workflow starts from
	// re-triggering the agent on its own work.
	if strings.HasPrefix(in.Branch, RefinementBranchPrefix) {
		return Analysis{
			NeedsRefinement: false,
			Reason:          "commit is on a " + RefinementBranchPrefix + " refinement branch the agent itself maintains",
		}, nil
	}

	// A story file is added or modified (removed stories are not
	// refinements; they are deletions).
	var storyPaths []string
	seen := make(map[string]bool)
	for _, path := range append(append([]string{}, in.Added...), in.Modified...) {
		if isStoryFile(path) && !seen[path] {
			seen[path] = true
			storyPaths = append(storyPaths, path)
		}
	}

	if len(storyPaths) == 0 {
		return Analysis{
			NeedsRefinement: false,
			Reason:          "commit does not add or modify any file under " + storyDir,
		}, nil
	}

	return Analysis{
		NeedsRefinement: true,
		Reason:          fmt.Sprintf("commit adds or modifies %d user story file(s) under %s", len(storyPaths), storyDir),
		StoryPaths:      storyPaths,
	}, nil
}

// isStoryFile reports whether path is a user story / backlog task file:
// a markdown file directly under backlog/tasks/.
func isStoryFile(path string) bool {
	if !strings.HasPrefix(path, storyDir) {
		return false
	}
	rest := strings.TrimPrefix(path, storyDir)
	// The file must be directly in backlog/tasks/ (no subdirectories)
	// and have a .md extension.
	if rest == "" || strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".md")
}

// FetchStoryInput is the input to the FetchStory activity.
type FetchStoryInput struct {
	// Repository is the provider full repo name, e.g. "root/BeanieGenie".
	Repository string `json:"repository"`
	// Path is the story file's path from the repository root.
	Path string `json:"path"`
	// Ref is the branch or commit SHA to read from; empty means the
	// repository's default branch.
	Ref string `json:"ref"`
}

// FetchStory fetches a user story's content from the provider. It runs as
// a Temporal activity so the provider API call stays out of the workflow
// body.
func (a *Activities) FetchStory(ctx context.Context, in FetchStoryInput) (string, error) {
	if a.provider == nil {
		return "", fmt.Errorf("userstoryreview: no provider client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("fetching user story",
			"repository", in.Repository, "path", in.Path, "ref", in.Ref)
	}

	file, err := a.provider.GetFile(ctx, in.Repository, in.Path, in.Ref)
	if err != nil {
		return "", fmt.Errorf("fetch story %s from %s: %w", in.Path, in.Repository, err)
	}
	return string(file.Content), nil
}

// GenerateTicketsInput is the input to the GenerateTickets activity.
type GenerateTicketsInput struct {
	// StoryPath is the story file's path (for context in the prompt).
	StoryPath string `json:"story_path"`
	// StoryContent is the story's markdown content.
	StoryContent string `json:"story_content"`
}

// ticketGenerationSystem is the system prompt for ticket generation.
const ticketGenerationSystem = `You are a technical product manager helping break down user stories into implementation tickets.

Given a user story in backlog.md markdown format, generate 1-5 implementation tickets that together cover the story's scope. Each ticket must be:
- Independently implementable and verifiable
- Focused on a single, cohesive piece of work
- Written in backlog.md markdown format with Context, What needs to happen, and Acceptance Criteria sections

You also raise up to 3 open questions about the story that the user should answer before or during implementation: ambiguities, missing decisions, or edge cases the story does not cover. Raise no questions when the story is fully clear.

Respond with a single JSON object of the form:
{"tickets": [{"title": "...", "description": "..."}], "questions": ["..."]}

Titles must be action-oriented (start with a verb), specific, and at most 12 words. Descriptions must be in markdown format suitable for a backlog.md task file. Questions must be single sentences. Use an empty "questions" array when there is nothing to ask.`

// GenerateTickets uses the LLM to generate implementation tickets (and
// the agent's open questions for the user) from a user story. It runs
// as a Temporal activity so the LLM API call stays out of the workflow
// body.
func (a *Activities) GenerateTickets(ctx context.Context, in GenerateTicketsInput) (GenerateTicketsOutput, error) {
	if a.llm == nil {
		return GenerateTicketsOutput{}, fmt.Errorf("userstoryreview: no llm client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("generating implementation tickets", "story_path", in.StoryPath)
	}

	userPrompt := fmt.Sprintf("User story file: %s\n\n%s", in.StoryPath, in.StoryContent)
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: userPrompt},
	}

	var out GenerateTicketsOutput
	if _, err := a.llm.ChatJSON(ctx, ticketGenerationSystem, messages, &out); err != nil {
		return GenerateTicketsOutput{}, fmt.Errorf("generate tickets for %s: %w", in.StoryPath, err)
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("generated implementation tickets",
			"story_path", in.StoryPath, "count", len(out.Tickets), "questions", len(out.Questions))
	}
	return out, nil
}

// GenerateTicketsOutput is the result of the GenerateTickets activity.
type GenerateTicketsOutput struct {
	// Tickets are the generated implementation tickets.
	Tickets []Ticket `json:"tickets"`
	// Questions are the agent's open questions for the user about the
	// story.
	Questions []string `json:"questions,omitempty"`
}

// --- Refinement branch, ticket files, merge request (TASK-8) ---

// CreateRefinementBranchInput is the input to the CreateRefinementBranch
// activity.
type CreateRefinementBranchInput struct {
	// Repository is the provider full repo name, e.g. "root/BeanieGenie".
	Repository string `json:"repository"`
	// Branch is the refinement branch to create.
	Branch string `json:"branch"`
	// FromRef is the ref to branch from, e.g. "main".
	FromRef string `json:"from_ref"`
}

// RefinementBranch is the result of the CreateRefinementBranch
// activity.
type RefinementBranch struct {
	// Name is the refinement branch name.
	Name string `json:"name"`
	// SHA is the branch's head commit; empty when an existing branch
	// was reused.
	SHA string `json:"sha,omitempty"`
}

// CreateRefinementBranch creates the refinement branch on the target
// repository. It runs as a Temporal activity so the provider API call
// stays out of the workflow body. A branch that already exists (e.g.
// from an earlier, partially completed run) is reused: the desired
// state exists, so the refinement continues instead of failing.
func (a *Activities) CreateRefinementBranch(ctx context.Context, in CreateRefinementBranchInput) (RefinementBranch, error) {
	if a.provider == nil {
		return RefinementBranch{}, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("creating refinement branch",
			"repository", in.Repository, "branch", in.Branch, "from", in.FromRef)
	}

	branch, err := a.provider.CreateBranch(ctx, in.Repository, in.Branch, in.FromRef)
	if err != nil {
		var apiErr *provider.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			if activity.IsActivity(ctx) {
				logger := activity.GetLogger(ctx)
				logger.Info("refinement branch already exists; reusing it", "branch", in.Branch)
			}
			return RefinementBranch{Name: in.Branch}, nil
		}
		return RefinementBranch{}, fmt.Errorf("create refinement branch %s in %s: %w", in.Branch, in.Repository, err)
	}
	return RefinementBranch{Name: branch.Name, SHA: branch.SHA}, nil
}

// CommitTicketsInput is the input to the CommitTickets activity.
type CommitTicketsInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Branch is the refinement branch the ticket files are committed
	// to.
	Branch string `json:"branch"`
	// StoryPath is the story file the tickets were refined from (for
	// the commit message).
	StoryPath string `json:"story_path"`
	// Tickets are the generated implementation tickets to write.
	Tickets []Ticket `json:"tickets"`
	// IDHint is the next task id the workflow suggests, so a commit
	// refining several stories does not assign the same ids twice. The
	// activity never assigns below the repository's next free id.
	IDHint int `json:"id_hint,omitempty"`
}

// CommitTicketsResult is the result of the CommitTickets activity.
type CommitTicketsResult struct {
	// TicketFiles lists the ticket files written, in ticket order.
	TicketFiles []string `json:"ticket_files"`
	// NextID is the next free task id after this commit.
	NextID int `json:"next_id"`
	// CommitSHA is the ticket commit's hash.
	CommitSHA string `json:"commit_sha,omitempty"`
}

// CommitTickets writes the generated tickets as backlog.md-format
// markdown files under backlog/tasks/ and commits them to the
// refinement branch. It runs as a Temporal activity so the provider API
// calls stay out of the workflow body.
func (a *Activities) CommitTickets(ctx context.Context, in CommitTicketsInput) (CommitTicketsResult, error) {
	if a.provider == nil {
		return CommitTicketsResult{}, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if len(in.Tickets) == 0 {
		return CommitTicketsResult{NextID: in.IDHint}, nil
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("committing implementation tickets",
			"repository", in.Repository, "branch", in.Branch, "tickets", len(in.Tickets))
	}

	// Ticket ids continue after the repository's highest existing task
	// id, and never below the workflow's hint (which threads ids across
	// the stories of one commit).
	next := in.IDHint
	files, err := a.provider.ListFiles(ctx, in.Repository, storyDir, in.Branch)
	if err != nil && !errors.Is(err, provider.ErrNotFound) {
		return CommitTicketsResult{}, fmt.Errorf("list %s on %s in %s: %w", storyDir, in.Branch, in.Repository, err)
	}
	if maxID := maxTaskID(files); maxID >= next {
		next = maxID + 1
	}

	now := time.Now().Format("2006-01-02 15:04")
	changes := make([]provider.FileChange, 0, len(in.Tickets))
	paths := make([]string, 0, len(in.Tickets))
	for i, t := range in.Tickets {
		id := next + i
		path := ticketFilePath(id, t.Title)
		changes = append(changes, provider.FileChange{
			Path:    path,
			Content: []byte(renderTicketFile(id, t, now)),
		})
		paths = append(paths, path)
	}

	message := fmt.Sprintf("Add implementation tickets refined from %s", in.StoryPath)
	commit, err := a.provider.Commit(ctx, in.Repository, in.Branch, message, changes)
	if err != nil {
		return CommitTicketsResult{}, fmt.Errorf("commit tickets to %s in %s: %w", in.Branch, in.Repository, err)
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("committed implementation tickets",
			"branch", in.Branch, "commit", ShortSHA(commit.SHA), "files", len(paths))
	}
	return CommitTicketsResult{
		TicketFiles: paths,
		NextID:      next + len(in.Tickets),
		CommitSHA:   commit.SHA,
	}, nil
}

// OpenMergeRequestInput is the input to the OpenMergeRequest activity.
type OpenMergeRequestInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Title is the merge request title.
	Title string `json:"title"`
	// Description is the merge request body (markdown).
	Description string `json:"description"`
	// Head is the source branch of the merge request.
	Head string `json:"head"`
	// Base is the target branch of the merge request.
	Base string `json:"base"`
}

// MergeRequestInfo identifies the merge request the OpenMergeRequest
// activity opened.
type MergeRequestInfo struct {
	// Number identifies the merge request within the repository.
	Number int64 `json:"number"`
	// Title is the merge request title.
	Title string `json:"title"`
	// URL is the merge request's page on the provider.
	URL string `json:"url"`
}

// OpenMergeRequest opens the refinement merge request on the provider.
// It runs as a Temporal activity so the provider API call stays out of
// the workflow body.
func (a *Activities) OpenMergeRequest(ctx context.Context, in OpenMergeRequestInput) (MergeRequestInfo, error) {
	if a.provider == nil {
		return MergeRequestInfo{}, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("opening refinement merge request",
			"repository", in.Repository, "head", in.Head, "base", in.Base)
	}

	mr, err := a.provider.CreateMergeRequest(ctx, in.Repository, in.Title, in.Description, in.Head, in.Base)
	if err != nil {
		return MergeRequestInfo{}, fmt.Errorf("open merge request %s -> %s in %s: %w", in.Head, in.Base, in.Repository, err)
	}
	return MergeRequestInfo{Number: mr.Number, Title: mr.Title, URL: mr.URL}, nil
}

// --- ticket file rendering ---

// ticketFilePath returns the backlog.md file path for ticket number id:
// backlog/tasks/task-<id> - <title-slug>.md, matching the backlog CLI's
// task file naming.
func ticketFilePath(id int, title string) string {
	return fmt.Sprintf("%stask-%03d - %s.md", storyDir, id, titleSlug(title))
}

// titleSlug turns a ticket title into a file-name slug: runs of
// whitespace and path separators collapse to single dashes.
func titleSlug(title string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '/' {
			return '-'
		}
		return r
	}, title)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// renderTicketFile renders one generated ticket as a backlog.md-format
// markdown file: frontmatter (id, title, status, dates, ordinal) plus
// the ticket description as the body. The field set follows the backlog
// CLI's task format.
func renderTicketFile(id int, t Ticket, now string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: TASK-%03d\n", id)
	fmt.Fprintf(&b, "title: %s\n", yamlScalar(t.Title))
	b.WriteString("status: To Do\n")
	b.WriteString("assignee: []\n")
	fmt.Fprintf(&b, "created_date: '%s'\n", now)
	fmt.Fprintf(&b, "updated_date: '%s'\n", now)
	b.WriteString("labels: []\n")
	b.WriteString("dependencies: []\n")
	b.WriteString("type: task\n")
	fmt.Fprintf(&b, "ordinal: %d\n", id*1000)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(t.Description))
	b.WriteString("\n")
	return b.String()
}

// yamlScalar renders s as a YAML scalar value, quoting only when the
// plain form would be ambiguous (e.g. a title containing ": ").
func yamlScalar(s string) string {
	enc, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimRight(string(enc), "\n")
}

// maxTaskID returns the highest task id used by the backlog task files
// in files, 0 when none exist.
func maxTaskID(files []provider.FileInfo) int {
	maxID := 0
	for _, f := range files {
		if id, ok := taskFileID(f.Path); ok && id > maxID {
			maxID = id
		}
	}
	return maxID
}

// taskFileID parses the numeric task id from a backlog task file path
// like "backlog/tasks/task-012 - Add login.md", reporting whether the
// path is a task file with an id at all.
func taskFileID(path string) (int, bool) {
	if !strings.HasPrefix(path, storyDir) {
		return 0, false
	}
	name := strings.TrimPrefix(path, storyDir)
	if strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") {
		return 0, false
	}
	name = strings.TrimSuffix(name, ".md")
	const taskPrefix = "task-"
	if !strings.HasPrefix(name, taskPrefix) {
		return 0, false
	}
	digits := name[len(taskPrefix):]
	if i := strings.IndexFunc(digits, notDigit); i >= 0 {
		digits = digits[:i]
	}
	if digits == "" {
		return 0, false
	}
	id, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return id, true
}

// notDigit reports whether r is not an ASCII digit.
func notDigit(r rune) bool { return r < '0' || r > '9' }
