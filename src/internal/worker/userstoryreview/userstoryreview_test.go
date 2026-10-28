package userstoryreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
)

// newTestEnv registers the template's workflow and activities with a test
// environment, mirroring how the runner registers them with a real
// Temporal worker. The provider and LLM fakes are returned so tests can
// seed state and script responses.
func newTestEnv(t *testing.T) (*testsuite.TestWorkflowEnvironment, *provider.Fake, *llm.Fake) {
	t.Helper()
	pf := provider.NewFake("root/BeanieGenie")
	lf := llm.NewFake()
	return newTestEnvWithClients(t, pf, lf), pf, lf
}

// newTestEnvWithClients registers the template wired to the given
// provider and LLM clients, for workflow tests that script either side
// (a fake, or a fake failing one operation).
func newTestEnvWithClients(t *testing.T, p provider.Client, l llm.Client) *testsuite.TestWorkflowEnvironment {
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

// newTestEnvWithMocks registers the workflow with mocked activities
// (no real activity implementations), for testing the workflow's
// orchestration logic in isolation.
func newTestEnvWithMocks(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(UserStoryReviewWorkflow, workflow.RegisterOptions{Name: WorkflowName})
	return env
}

func validInput() Input {
	return Input{
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: "root/BeanieGenie",
		Branch:     "main",
		SHA:        "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
		Message:    "Refine user story\n\nLong body",
		URL:        "http://localhost:3000/root/BeanieGenie/commit/8ef0de217a18",
		Author:     "root",
	}
}

// --- Story file detection (AC1, AC4) ---

func TestIsStoryFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"backlog/tasks/task-1.md", true},
		{"backlog/tasks/task-42 - Implement feature.md", true},
		{"backlog/tasks/story.md", true},
		{"backlog/tasks/", false},            // directory, not a file
		{"backlog/tasks/sub/file.md", false}, // nested, not direct child
		{"backlog/tasks/README.txt", false},  // not markdown
		{"backlog/stories/task-1.md", false}, // wrong directory
		{"src/main.go", false},
		{"README.md", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isStoryFile(tt.path); got != tt.want {
				t.Errorf("isStoryFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestAnalyzeCommitDetectsStoryFiles(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		added      []string
		modified   []string
		removed    []string
		wantRefine bool
		wantPaths  []string
	}{
		{
			name:       "added story file",
			added:      []string{"backlog/tasks/task-1.md"},
			wantRefine: true,
			wantPaths:  []string{"backlog/tasks/task-1.md"},
		},
		{
			name:       "modified story file",
			modified:   []string{"backlog/tasks/task-1.md"},
			wantRefine: true,
			wantPaths:  []string{"backlog/tasks/task-1.md"},
		},
		{
			name:       "multiple story files",
			added:      []string{"backlog/tasks/task-1.md", "backlog/tasks/task-2.md"},
			wantRefine: true,
			wantPaths:  []string{"backlog/tasks/task-1.md", "backlog/tasks/task-2.md"},
		},
		{
			name:       "duplicate paths deduped",
			added:      []string{"backlog/tasks/task-1.md"},
			modified:   []string{"backlog/tasks/task-1.md"},
			wantRefine: true,
			wantPaths:  []string{"backlog/tasks/task-1.md"},
		},
		{
			name:       "non-story files only",
			added:      []string{"src/main.go", "README.md"},
			modified:   []string{"src/lib/util.go"},
			wantRefine: false,
		},
		{
			name:       "removed story file is not refinement",
			removed:    []string{"backlog/tasks/task-1.md"},
			wantRefine: false,
		},
		{
			name:       "mixed story and non-story",
			added:      []string{"src/main.go", "backlog/tasks/task-1.md"},
			wantRefine: true,
			wantPaths:  []string{"backlog/tasks/task-1.md"},
		},
		{
			name:       "nested path is not a story",
			added:      []string{"backlog/tasks/sub/file.md"},
			wantRefine: false,
		},
		{
			name:       "no files",
			wantRefine: false,
		},
		{
			// Commits on the agent's own refine branches carry its
			// generated ticket files; refining them would loop
			// forever. The trigger declines them; analyzeCommit
			// guards manual workflow starts too.
			name:       "agent commit on a refinement branch",
			branch:     RefinementBranchPrefix + "task-009-track-watering-history-per-plant-8395b2548be1",
			added:      []string{"backlog/tasks/task-010 - Create-watering-history-model.md"},
			wantRefine: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			if tt.branch != "" {
				in.Branch = tt.branch
			}
			in.Added = tt.added
			in.Modified = tt.modified
			in.Removed = tt.removed

			analysis, err := analyzeCommit(in)
			if err != nil {
				t.Fatalf("analyzeCommit: %v", err)
			}
			if analysis.NeedsRefinement != tt.wantRefine {
				t.Errorf("NeedsRefinement = %v, want %v", analysis.NeedsRefinement, tt.wantRefine)
			}
			if tt.wantRefine {
				if len(analysis.StoryPaths) != len(tt.wantPaths) {
					t.Fatalf("StoryPaths = %v, want %v", analysis.StoryPaths, tt.wantPaths)
				}
				for i, p := range tt.wantPaths {
					if analysis.StoryPaths[i] != p {
						t.Errorf("StoryPaths[%d] = %q, want %q", i, analysis.StoryPaths[i], p)
					}
				}
			}
			if analysis.Reason == "" {
				t.Error("Reason is empty")
			}
		})
	}
}

func TestAnalyzeCommitRejectsInvalidInput(t *testing.T) {
	if _, err := analyzeCommit(Input{}); err == nil {
		t.Error("analyzeCommit accepted an invalid payload")
	}
}

// --- FetchStory activity ---

func TestFetchStory(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1\n\nImplement the feature.",
	})
	acts := NewActivities(pf, llm.NewFake())

	content, err := acts.FetchStory(t.Context(), FetchStoryInput{
		Repository: "root/BeanieGenie",
		Path:       "backlog/tasks/task-1.md",
		Ref:        "main",
	})
	if err != nil {
		t.Fatalf("FetchStory: %v", err)
	}
	if !strings.Contains(content, "Implement the feature.") {
		t.Errorf("content = %q, want the seeded story", content)
	}
}

func TestFetchStoryProviderError(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.Err = errors.New("provider unavailable")
	acts := NewActivities(pf, llm.NewFake())

	_, err := acts.FetchStory(t.Context(), FetchStoryInput{
		Repository: "root/BeanieGenie",
		Path:       "backlog/tasks/task-1.md",
		Ref:        "main",
	})
	if err == nil {
		t.Fatal("FetchStory succeeded with provider error, want failure")
	}
	if !strings.Contains(err.Error(), "provider unavailable") {
		t.Errorf("error = %v, want provider error", err)
	}
}

func TestFetchStoryNotFound(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	acts := NewActivities(pf, llm.NewFake())

	_, err := acts.FetchStory(t.Context(), FetchStoryInput{
		Repository: "root/BeanieGenie",
		Path:       "backlog/tasks/missing.md",
		Ref:        "main",
	})
	if err == nil {
		t.Fatal("FetchStory succeeded for missing file, want failure")
	}
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// --- GenerateTickets activity ---

func TestGenerateTickets(t *testing.T) {
	const reply = `{"tickets":[{"title":"Add login endpoint","description":"## Context\nNeed auth.\n\n## What needs to happen\nAdd POST /login.\n\n## Acceptance Criteria\n- [ ] Login returns JWT"},{"title":"Add login tests","description":"## Context\nCoverage.\n\n## What needs to happen\nAdd tests for login.\n\n## Acceptance Criteria\n- [ ] Tests pass"}],"questions":["Should the JWT expire after 24 hours?"]}`
	lf := llm.NewFake(llm.Response{Content: reply, Model: "test-model"})
	acts := NewActivities(provider.NewFake(), lf)

	out, err := acts.GenerateTickets(t.Context(), GenerateTicketsInput{
		StoryPath:    "backlog/tasks/task-1.md",
		StoryContent: "# Task 1\n\nAdd user login.",
	})
	if err != nil {
		t.Fatalf("GenerateTickets: %v", err)
	}
	if len(out.Tickets) != 2 {
		t.Fatalf("tickets = %d, want 2", len(out.Tickets))
	}
	if out.Tickets[0].Title != "Add login endpoint" {
		t.Errorf("tickets[0].Title = %q, want %q", out.Tickets[0].Title, "Add login endpoint")
	}
	if !strings.Contains(out.Tickets[0].Description, "POST /login") {
		t.Errorf("tickets[0].Description = %q, want markdown description", out.Tickets[0].Description)
	}
	if out.Tickets[1].Title != "Add login tests" {
		t.Errorf("tickets[1].Title = %q, want %q", out.Tickets[1].Title, "Add login tests")
	}
	if len(out.Questions) != 1 || out.Questions[0] != "Should the JWT expire after 24 hours?" {
		t.Errorf("questions = %v, want the JWT question", out.Questions)
	}

	// The LLM was called with the story content in the prompt.
	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	if !calls[0].JSON {
		t.Error("llm call was not in JSON mode")
	}
	if len(calls[0].Messages) != 1 || !strings.Contains(calls[0].Messages[0].Content, "Add user login.") {
		t.Errorf("llm prompt = %+v, want the story content", calls[0].Messages)
	}
}

func TestGenerateTicketsLLMError(t *testing.T) {
	lf := llm.NewFake()
	lf.Err = errors.New("llm unavailable")
	acts := NewActivities(provider.NewFake(), lf)

	_, err := acts.GenerateTickets(t.Context(), GenerateTicketsInput{
		StoryPath:    "backlog/tasks/task-1.md",
		StoryContent: "# Task 1",
	})
	if err == nil {
		t.Fatal("GenerateTickets succeeded with llm error, want failure")
	}
	if !strings.Contains(err.Error(), "llm unavailable") {
		t.Errorf("error = %v, want llm error", err)
	}
}

func TestGenerateTicketsInvalidJSON(t *testing.T) {
	lf := llm.NewFake(llm.Response{Content: "not json"})
	acts := NewActivities(provider.NewFake(), lf)

	_, err := acts.GenerateTickets(t.Context(), GenerateTicketsInput{
		StoryPath:    "backlog/tasks/task-1.md",
		StoryContent: "# Task 1",
	})
	if err == nil {
		t.Fatal("GenerateTickets succeeded with invalid JSON, want failure")
	}
	if !errors.Is(err, llm.ErrInvalidJSON) {
		t.Errorf("error = %v, want ErrInvalidJSON", err)
	}
}

// --- CreateRefinementBranch activity (AC1) ---

func TestCreateRefinementBranch(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{"README.md": "hi"})
	acts := NewActivities(pf, llm.NewFake())

	branch, err := acts.CreateRefinementBranch(t.Context(), CreateRefinementBranchInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-1-abc",
		FromRef:    "main",
	})
	if err != nil {
		t.Fatalf("CreateRefinementBranch: %v", err)
	}
	if branch.Name != "refine/task-1-abc" {
		t.Errorf("branch name = %q, want refine/task-1-abc", branch.Name)
	}

	// An existing branch (e.g. from an earlier partial run) is reused,
	// not an error.
	branch, err = acts.CreateRefinementBranch(t.Context(), CreateRefinementBranchInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-1-abc",
		FromRef:    "main",
	})
	if err != nil {
		t.Fatalf("CreateRefinementBranch on existing branch: %v", err)
	}
	if branch.Name != "refine/task-1-abc" {
		t.Errorf("branch name = %q, want refine/task-1-abc", branch.Name)
	}
}

func TestCreateRefinementBranchError(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.Err = errors.New("forgejo unreachable")
	acts := NewActivities(pf, llm.NewFake())

	_, err := acts.CreateRefinementBranch(t.Context(), CreateRefinementBranchInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-1-abc",
		FromRef:    "main",
	})
	if err == nil {
		t.Fatal("CreateRefinementBranch succeeded with provider error, want failure")
	}
	if !strings.Contains(err.Error(), "refine/task-1-abc") {
		t.Errorf("error = %v, want the branch name in the error", err)
	}

	// A missing repository surfaces the provider's not-found error.
	acts2 := NewActivities(provider.NewFake("root/BeanieGenie"), llm.NewFake())
	_, err = acts2.CreateRefinementBranch(t.Context(), CreateRefinementBranchInput{
		Repository: "other/repo",
		Branch:     "refine/task-1-abc",
		FromRef:    "main",
	})
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// --- CommitTickets activity (AC2) ---

func TestCommitTickets(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "refine/task-1-abc", map[string]string{
		"backlog/tasks/task-001 - Story.md":   "---\nid: TASK-001\n---\n\nBody",
		"backlog/tasks/task-002 - Another.md": "---\nid: TASK-002\n---\n\nBody",
		"backlog/tasks/README.md":             "not a task",
	})
	acts := NewActivities(pf, llm.NewFake())

	result, err := acts.CommitTickets(t.Context(), CommitTicketsInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-1-abc",
		StoryPath:  "backlog/tasks/task-001 - Story.md",
		Tickets: []Ticket{
			{Title: "Add login endpoint", Description: "## Context\nNeed auth.\n\n## Acceptance Criteria\n- [ ] Works"},
			{Title: "Add login tests", Description: "## Context\nCoverage."},
		},
	})
	if err != nil {
		t.Fatalf("CommitTickets: %v", err)
	}
	want := []string{
		"backlog/tasks/task-003 - Add-login-endpoint.md",
		"backlog/tasks/task-004 - Add-login-tests.md",
	}
	if len(result.TicketFiles) != len(want) {
		t.Fatalf("TicketFiles = %v, want %v", result.TicketFiles, want)
	}
	for i := range want {
		if result.TicketFiles[i] != want[i] {
			t.Errorf("TicketFiles[%d] = %q, want %q", i, result.TicketFiles[i], want[i])
		}
	}
	if result.NextID != 5 {
		t.Errorf("NextID = %d, want 5 (after task-004)", result.NextID)
	}
	if result.CommitSHA == "" {
		t.Error("CommitSHA is empty")
	}

	// The committed file carries the ticket as backlog.md markdown.
	file, err := pf.GetFile(t.Context(), "root/BeanieGenie", want[0], "refine/task-1-abc")
	if err != nil {
		t.Fatalf("ticket file not committed: %v", err)
	}
	for _, want := range []string{
		"id: TASK-003",
		"title: Add login endpoint",
		"status: To Do",
		"ordinal: 3000",
		"## Context",
		"- [ ] Works",
	} {
		if !strings.Contains(string(file.Content), want) {
			t.Errorf("ticket file misses %q:\n%s", want, file.Content)
		}
	}
	// The frontmatter is valid backlog.md metadata for our own parser.
	if meta := parseStoryMeta(string(file.Content)); meta.ID != "TASK-003" || meta.Title != "Add login endpoint" {
		t.Errorf("parseStoryMeta(ticket) = %+v, want TASK-003 / Add login endpoint", meta)
	}
}

func TestCommitTicketsIDHint(t *testing.T) {
	// The workflow's hint wins over the repository's next free id, so a
	// multi-story commit never assigns the same id twice.
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "refine/task-2-abc", map[string]string{
		"backlog/tasks/task-001 - Story.md": "body",
	})
	acts := NewActivities(pf, llm.NewFake())

	result, err := acts.CommitTickets(t.Context(), CommitTicketsInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-2-abc",
		Tickets:    []Ticket{{Title: "Do the thing", Description: "Body"}},
		IDHint:     10,
	})
	if err != nil {
		t.Fatalf("CommitTickets: %v", err)
	}
	if result.TicketFiles[0] != "backlog/tasks/task-010 - Do-the-thing.md" {
		t.Errorf("TicketFiles[0] = %q, want task-010 (the hint)", result.TicketFiles[0])
	}
	if result.NextID != 11 {
		t.Errorf("NextID = %d, want 11", result.NextID)
	}
}

func TestCommitTicketsNoTickets(t *testing.T) {
	// Zero tickets commit nothing and touch nothing.
	pf := provider.NewFake("root/BeanieGenie")
	acts := NewActivities(pf, llm.NewFake())

	result, err := acts.CommitTickets(t.Context(), CommitTicketsInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-1-abc",
		IDHint:     4,
	})
	if err != nil {
		t.Fatalf("CommitTickets: %v", err)
	}
	if len(result.TicketFiles) != 0 || result.NextID != 4 || result.CommitSHA != "" {
		t.Errorf("result = %+v, want empty with NextID 4", result)
	}
}

func TestCommitTicketsError(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.Err = errors.New("forgejo unreachable")
	acts := NewActivities(pf, llm.NewFake())

	_, err := acts.CommitTickets(t.Context(), CommitTicketsInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/task-1-abc",
		Tickets:    []Ticket{{Title: "T", Description: "D"}},
	})
	if err == nil {
		t.Fatal("CommitTickets succeeded with provider error, want failure")
	}
	if !strings.Contains(err.Error(), "forgejo unreachable") {
		t.Errorf("error = %v, want the provider error", err)
	}

	// Committing to a missing branch fails with the commit error.
	acts2 := NewActivities(provider.NewFake("root/BeanieGenie"), llm.NewFake())
	_, err = acts2.CommitTickets(t.Context(), CommitTicketsInput{
		Repository: "root/BeanieGenie",
		Branch:     "refine/missing",
		Tickets:    []Ticket{{Title: "T", Description: "D"}},
	})
	if err == nil {
		t.Fatal("CommitTickets succeeded for a missing branch, want failure")
	}
	if !strings.Contains(err.Error(), "commit tickets") {
		t.Errorf("error = %v, want commit tickets error", err)
	}
}

// --- OpenMergeRequest activity (AC3, AC5) ---

func TestOpenMergeRequest(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{"README.md": "hi"})
	acts := NewActivities(pf, llm.NewFake())

	mr, err := acts.OpenMergeRequest(t.Context(), OpenMergeRequestInput{
		Repository:  "root/BeanieGenie",
		Title:       "Refine TASK-001: Add user login",
		Description: "Refines user story TASK-001.",
		Head:        "refine/task-1-abc",
		Base:        "main",
	})
	if err != nil {
		t.Fatalf("OpenMergeRequest: %v", err)
	}
	if mr.Number != 1 {
		t.Errorf("mr.Number = %d, want 1", mr.Number)
	}
	if mr.URL == "" {
		t.Error("mr.URL is empty")
	}
	if mr.Title != "Refine TASK-001: Add user login" {
		t.Errorf("mr.Title = %q", mr.Title)
	}

	// The provider recorded the merge request.
	if _, err := pf.GetMergeRequest(t.Context(), "root/BeanieGenie", 1); err != nil {
		t.Errorf("merge request not recorded by the provider: %v", err)
	}
}

func TestOpenMergeRequestError(t *testing.T) {
	pf := provider.NewFake("root/BeanieGenie")
	pf.Err = errors.New("forgejo unreachable")
	acts := NewActivities(pf, llm.NewFake())

	_, err := acts.OpenMergeRequest(t.Context(), OpenMergeRequestInput{
		Repository: "root/BeanieGenie",
		Head:       "refine/task-1-abc",
		Base:       "main",
	})
	if err == nil {
		t.Fatal("OpenMergeRequest succeeded with provider error, want failure")
	}
	if !strings.Contains(err.Error(), "open merge request") {
		t.Errorf("error = %v, want open merge request error", err)
	}
}

// --- Ticket file rendering ---

func TestTicketFilePath(t *testing.T) {
	tests := []struct {
		id    int
		title string
		want  string
	}{
		{9, "Add watering history API", "backlog/tasks/task-009 - Add-watering-history-API.md"},
		{123, "Do it", "backlog/tasks/task-123 - Do-it.md"},
		{1, "  Trim   and  collapse  ", "backlog/tasks/task-001 - Trim-and-collapse.md"},
		{1, "Use/slashes and  spaces", "backlog/tasks/task-001 - Use-slashes-and-spaces.md"},
	}
	for _, tt := range tests {
		if got := ticketFilePath(tt.id, tt.title); got != tt.want {
			t.Errorf("ticketFilePath(%d, %q) = %q, want %q", tt.id, tt.title, got, tt.want)
		}
	}
}

func TestRenderTicketFile(t *testing.T) {
	content := renderTicketFile(9, Ticket{
		Title:       "Add login: with a colon",
		Description: "## Context\nNeed auth.\n\n## Acceptance Criteria\n- [ ] Works",
	}, "2026-10-28 21:05")
	for _, want := range []string{
		"id: TASK-009",
		"status: To Do",
		"created_date: '2026-10-28 21:05'",
		"updated_date: '2026-10-28 21:05'",
		"type: task",
		"ordinal: 9000",
		"## Context",
		"- [ ] Works",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("ticket file misses %q:\n%s", want, content)
		}
	}
	// The frontmatter is valid backlog.md metadata for our own parser,
	// including the title that needs YAML quoting.
	meta := parseStoryMeta(content)
	if meta.ID != "TASK-009" || meta.Title != "Add login: with a colon" {
		t.Errorf("parseStoryMeta(ticket) = %+v, want TASK-009 / Add login: with a colon", meta)
	}
}

func TestTaskFileID(t *testing.T) {
	tests := []struct {
		path string
		id   int
		ok   bool
	}{
		{"backlog/tasks/task-001 - Story.md", 1, true},
		{"backlog/tasks/task-42.md", 42, true},
		{"backlog/tasks/task-007 - Foo-bar.md", 7, true},
		{"backlog/tasks/task-9x - Odd.md", 9, true}, // digits up to the first non-digit
		{"backlog/tasks/README.md", 0, false},
		{"backlog/tasks/sub/task-1.md", 0, false}, // nested, not a direct child
		{"src/task-1.md", 0, false},
		{"backlog/tasks/task-.md", 0, false},
		{"backlog/tasks/task-abc.md", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		id, ok := taskFileID(tt.path)
		if id != tt.id || ok != tt.ok {
			t.Errorf("taskFileID(%q) = (%d, %v), want (%d, %v)", tt.path, id, ok, tt.id, tt.ok)
		}
	}
}

func TestMaxTaskID(t *testing.T) {
	files := []provider.FileInfo{
		{Path: "backlog/tasks/task-001 - A.md"},
		{Path: "backlog/tasks/task-008 - B.md"},
		{Path: "backlog/tasks/README.md"},
		{Path: "backlog/tasks/sub/task-99.md"},
	}
	if id := maxTaskID(files); id != 8 {
		t.Errorf("maxTaskID = %d, want 8 (ignoring non-task and nested files)", id)
	}
	if id := maxTaskID(nil); id != 0 {
		t.Errorf("maxTaskID(nil) = %d, want 0", id)
	}
}

// --- Workflow tests ---

func TestWorkflowNoStoryFiles(t *testing.T) {
	// AC4: commits that do not touch story files complete with
	// NeedsRefinement: false and no tickets generated.
	env, _, _ := newTestEnv(t)
	in := validInput()
	in.Added = []string{"src/main.go", "README.md"}
	in.Modified = []string{"src/lib/util.go"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Reviewed {
		t.Error("result.Reviewed = false, want true")
	}
	if result.NeedsRefinement {
		t.Error("result.NeedsRefinement = true, want false for non-story commit")
	}
	if len(result.Tickets) != 0 {
		t.Errorf("result.Tickets = %v, want none", result.Tickets)
	}
	if result.Status == "" {
		t.Error("result.Status is empty")
	}
}

func TestWorkflowWithStoryFile(t *testing.T) {
	// TASK-7 AC1-3: a commit that adds a story file is detected, the
	// story is fetched, and tickets are generated.
	// TASK-8 AC1-4: the workflow commits the tickets to a refinement
	// branch and opens an MR, recorded in the Result.
	pf := provider.NewFake("root/BeanieGenie")
	const story = `---
id: TASK-001
title: Add user login
status: To Do
assignee: []
created_date: '2026-10-28 20:00'
updated_date: '2026-10-28 20:00'
labels: []
dependencies: []
type: feature
ordinal: 1000
---

## Description

Add user login with JWT.
`
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": story,
	})

	// Script the LLM to generate two tickets and ask one question.
	const reply = `{"tickets":[{"title":"Add login endpoint","description":"## Context\nNeed auth.\n\n## What needs to happen\nAdd POST /login returning a JWT.\n\n## Acceptance Criteria\n- [ ] POST /login returns 200 with JWT"},{"title":"Add login tests","description":"## Context\nCoverage for login.\n\n## What needs to happen\nAdd unit and integration tests.\n\n## Acceptance Criteria\n- [ ] All tests pass"}],"questions":["How long should the JWT be valid?"]}`
	lf := llm.NewFake(llm.Response{Content: reply, Model: "test-model"})

	env := newTestEnvWithClients(t, pf, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Reviewed {
		t.Error("result.Reviewed = false, want true")
	}
	if !result.NeedsRefinement {
		t.Error("result.NeedsRefinement = false, want true for story commit")
	}
	if len(result.Tickets) != 2 {
		t.Fatalf("result.Tickets = %d tickets, want 2", len(result.Tickets))
	}
	if result.Tickets[0].Title != "Add login endpoint" {
		t.Errorf("tickets[0].Title = %q, want %q", result.Tickets[0].Title, "Add login endpoint")
	}
	if result.Tickets[0].Description == "" {
		t.Error("tickets[0].Description is empty")
	}
	if result.Status == "" {
		t.Error("result.Status is empty")
	}

	// One submission with the MR the workflow opened.
	if len(result.Submissions) != 1 {
		t.Fatalf("result.Submissions = %d, want 1", len(result.Submissions))
	}
	sub := result.Submissions[0]
	if sub.StoryID != "TASK-001" || sub.StoryTitle != "Add user login" {
		t.Errorf("submission story = %q/%q, want TASK-001/Add user login", sub.StoryID, sub.StoryTitle)
	}
	branch := RefinementBranchName("backlog/tasks/task-1.md", in.SHA)
	if sub.Branch != branch {
		t.Errorf("submission branch = %q, want %q", sub.Branch, branch)
	}
	if sub.MRNumber != 1 {
		t.Errorf("submission MRNumber = %d, want 1", sub.MRNumber)
	}
	if sub.MRTitle != "Refine TASK-001: Add user login" {
		t.Errorf("submission MRTitle = %q, want %q", sub.MRTitle, "Refine TASK-001: Add user login")
	}
	if sub.MRURL == "" {
		t.Error("submission MRURL is empty")
	}
	if len(sub.Questions) != 1 || sub.Questions[0] != "How long should the JWT be valid?" {
		t.Errorf("submission questions = %v, want the JWT question", sub.Questions)
	}

	// The ticket files were committed to the refinement branch. The
	// existing task-1.md keeps id 1, so the tickets take 2 and 3.
	wantFiles := []string{
		"backlog/tasks/task-002 - Add-login-endpoint.md",
		"backlog/tasks/task-003 - Add-login-tests.md",
	}
	if len(sub.TicketFiles) != len(wantFiles) {
		t.Fatalf("submission ticket files = %v, want %v", sub.TicketFiles, wantFiles)
	}
	for i, want := range wantFiles {
		if sub.TicketFiles[i] != want {
			t.Errorf("ticket file[%d] = %q, want %q", i, sub.TicketFiles[i], want)
		}
		file, err := pf.GetFile(t.Context(), "root/BeanieGenie", want, branch)
		if err != nil {
			t.Fatalf("ticket file %s not committed: %v", want, err)
		}
		if !strings.Contains(string(file.Content), "id: TASK-") {
			t.Errorf("ticket file %s has no task id frontmatter:\n%s", want, file.Content)
		}
	}

	// The MR was opened on the provider, targeting the commit's branch.
	mr, err := pf.GetMergeRequest(t.Context(), "root/BeanieGenie", 1)
	if err != nil {
		t.Fatalf("merge request not opened: %v", err)
	}
	if mr.Head != branch || mr.Base != "main" {
		t.Errorf("mr head/base = %q/%q, want %q/main", mr.Head, mr.Base, branch)
	}
	for _, want := range []string{
		"TASK-001: Add user login",
		"**Add login endpoint**",
		"**Add login tests**",
		"task-002 - Add-login-endpoint.md",
		"## Questions for you",
		"How long should the JWT be valid?",
	} {
		if !strings.Contains(mr.Description, want) {
			t.Errorf("mr description misses %q:\n%s", want, mr.Description)
		}
	}
}

func TestWorkflowMultipleStoryFiles(t *testing.T) {
	// A commit that adds multiple story files generates tickets for
	// each, opens one MR per story, and threads the ticket ids so no
	// two MRs claim the same id.
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1\n\nFirst story.",
		"backlog/tasks/task-2.md": "# Task 2\n\nSecond story.",
	})
	const reply = `{"tickets":[{"title":"Implement feature","description":"## Context\nStory.\n\n## What needs to happen\nImplement it.\n\n## Acceptance Criteria\n- [ ] Done"}],"questions":[]}`
	lf := llm.NewFake(
		llm.Response{Content: reply, Model: "test-model"},
		llm.Response{Content: reply, Model: "test-model"},
	)

	env := newTestEnvWithClients(t, pf, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md", "backlog/tasks/task-2.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.NeedsRefinement {
		t.Error("result.NeedsRefinement = false, want true")
	}
	if len(result.Tickets) != 2 {
		t.Errorf("result.Tickets = %d tickets, want 2 (one per story)", len(result.Tickets))
	}
	if len(result.Submissions) != 2 {
		t.Fatalf("result.Submissions = %d, want 2 (one MR per story)", len(result.Submissions))
	}
	if result.Submissions[0].MRNumber != 1 || result.Submissions[1].MRNumber != 2 {
		t.Errorf("MR numbers = %d/%d, want 1/2",
			result.Submissions[0].MRNumber, result.Submissions[1].MRNumber)
	}
	if result.Submissions[0].TicketFiles[0] != "backlog/tasks/task-003 - Implement-feature.md" {
		t.Errorf("first story ticket file = %q, want task-003", result.Submissions[0].TicketFiles[0])
	}
	if result.Submissions[1].TicketFiles[0] != "backlog/tasks/task-004 - Implement-feature.md" {
		t.Errorf("second story ticket file = %q, want task-004 (threaded id)", result.Submissions[1].TicketFiles[0])
	}
}

func TestWorkflowProviderAPIFailure(t *testing.T) {
	// AC6: error handling for provider API failures.
	pf := provider.NewFake("root/BeanieGenie")
	pf.Err = errors.New("forgejo unreachable")
	lf := llm.NewFake()

	env := newTestEnvWithClients(t, pf, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("workflow succeeded with provider error, want failure")
	}
	if !strings.Contains(err.Error(), "fetch story") {
		t.Errorf("error = %v, want fetch story error", err)
	}
}

func TestWorkflowLLMFailure(t *testing.T) {
	// Error handling for LLM API failures.
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1\n\nAdd login.",
	})
	lf := llm.NewFake()
	lf.Err = errors.New("llm rate limited")

	env := newTestEnvWithClients(t, pf, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("workflow succeeded with llm error, want failure")
	}
	if !strings.Contains(err.Error(), "generate tickets") {
		t.Errorf("error = %v, want generate tickets error", err)
	}
}

// failBranchClient and failMRClient wrap a provider fake failing one
// operation, to test the workflow's error handling during MR
// submission: the story flow (fetch, generate) succeeds, the
// submission step fails.
type failBranchClient struct {
	provider.Client
}

func (failBranchClient) CreateBranch(context.Context, string, string, string) (provider.Branch, error) {
	return provider.Branch{}, errors.New("forgejo refused branch creation")
}

type failMRClient struct {
	provider.Client
}

func (failMRClient) CreateMergeRequest(context.Context, string, string, string, string, string) (provider.MergeRequest, error) {
	return provider.MergeRequest{}, errors.New("forgejo refused the merge request")
}

func TestWorkflowRefinementBranchFailure(t *testing.T) {
	// TASK-8 AC6-style error handling: a provider failure while
	// creating the refinement branch fails the workflow clearly.
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1\n\nAdd login.",
	})
	lf := llm.NewFake(llm.Response{
		Content: `{"tickets":[{"title":"Add login","description":"## Context\nNeed auth."}],"questions":[]}`,
		Model:   "test-model",
	})

	env := newTestEnvWithClients(t, failBranchClient{Client: pf}, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("workflow succeeded with branch failure, want failure")
	}
	if !strings.Contains(err.Error(), "create refinement branch") {
		t.Errorf("error = %v, want create refinement branch error", err)
	}
}

func TestWorkflowMergeRequestFailure(t *testing.T) {
	// TASK-8 AC6: if MR creation fails, the workflow fails with a
	// clear error.
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1\n\nAdd login.",
	})
	lf := llm.NewFake(llm.Response{
		Content: `{"tickets":[{"title":"Add login","description":"## Context\nNeed auth."}],"questions":[]}`,
		Model:   "test-model",
	})

	env := newTestEnvWithClients(t, failMRClient{Client: pf}, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("workflow succeeded with MR failure, want failure")
	}
	if !strings.Contains(err.Error(), "open merge request") {
		t.Errorf("error = %v, want open merge request error", err)
	}
}

func TestWorkflowZeroTicketsSkipsMR(t *testing.T) {
	// A story the LLM does not break into tickets opens no MR.
	pf := provider.NewFake("root/BeanieGenie")
	pf.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1\n\nEmpty story.",
	})
	lf := llm.NewFake(llm.Response{Content: `{"tickets":[],"questions":[]}`})

	env := newTestEnvWithClients(t, pf, lf)

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}

	env.ExecuteWorkflow(UserStoryReviewWorkflow, in)

	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.NeedsRefinement {
		t.Error("result.NeedsRefinement = false, want true (the story was processed)")
	}
	if len(result.Submissions) != 0 {
		t.Errorf("result.Submissions = %v, want none", result.Submissions)
	}
	if _, err := pf.GetMergeRequest(t.Context(), "root/BeanieGenie", 1); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("merge request lookup error = %v, want ErrNotFound (no MR opened)", err)
	}
}

func TestWorkflowRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		in   Input
	}{
		{name: "missing sha", in: Input{Repository: "root/BeanieGenie"}},
		{name: "missing repository", in: Input{SHA: "deadbeef"}},
		{name: "blank sha", in: Input{SHA: "  ", Repository: "root/BeanieGenie"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newTestEnv(t)
			env.ExecuteWorkflow(UserStoryReviewWorkflow, tt.in)

			err := env.GetWorkflowError()
			if err == nil {
				t.Fatal("workflow succeeded with invalid input, want failure")
			}
			if !strings.Contains(err.Error(), "invalid userStoryReviewWorkflow input") {
				t.Errorf("error = %v, want invalid input error", err)
			}
		})
	}
}

// --- Input validation and event mapping ---

func TestInputValidate(t *testing.T) {
	if err := validInput().Validate(); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}
	if err := (Input{}).Validate(); err == nil {
		t.Error("empty input accepted")
	}
}

func TestInputFromEvent(t *testing.T) {
	// Commit events become workflow input with the commit payload,
	// including the Added/Removed/Modified file lists.
	ev := event.Event{
		Kind:       event.KindCommit,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: "root/BeanieGenie",
		Commit: &event.Commit{
			SHA:      "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
			Branch:   "main",
			Message:  "Refine user story",
			URL:      "http://localhost:3000/commit/8ef0de",
			Author:   event.Person{Name: "Webhook Test", Email: "test@example.com"},
			Added:    []string{"backlog/tasks/task-1.md"},
			Modified: []string{"src/main.go"},
		},
	}
	got, ok := InputFromEvent(ev)
	if !ok {
		t.Fatal("InputFromEvent declined a commit event")
	}
	in, ok := got.(Input)
	if !ok {
		t.Fatalf("input has type %T, want Input", got)
	}
	want := Input{
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: "root/BeanieGenie",
		Branch:     "main",
		SHA:        "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
		Message:    "Refine user story",
		URL:        "http://localhost:3000/commit/8ef0de",
		Author:     "Webhook Test", // username empty: falls back to name
		Added:      []string{"backlog/tasks/task-1.md"},
		Modified:   []string{"src/main.go"},
	}
	if in.Provider != want.Provider || in.SHA != want.SHA || in.Author != want.Author {
		t.Errorf("input = %+v, want %+v", in, want)
	}
	if len(in.Added) != 1 || in.Added[0] != "backlog/tasks/task-1.md" {
		t.Errorf("input.Added = %v, want [backlog/tasks/task-1.md]", in.Added)
	}
	if len(in.Modified) != 1 || in.Modified[0] != "src/main.go" {
		t.Errorf("input.Modified = %v, want [src/main.go]", in.Modified)
	}
}

func TestInputFromEventDeclines(t *testing.T) {
	tests := []struct {
		name string
		ev   event.Event
	}{
		{
			name: "comment event",
			ev:   event.Event{Kind: event.KindComment, Comment: &event.Comment{ID: 1}},
		},
		{
			name: "commit event without payload",
			ev:   event.Event{Kind: event.KindCommit},
		},
		{
			name: "commit without sha",
			ev:   event.Event{Kind: event.KindCommit, Commit: &event.Commit{Branch: "main"}},
		},
		{
			name: "commit on the agent's refinement branch",
			ev: event.Event{Kind: event.KindCommit, Commit: &event.Commit{
				Branch: "refine/task-009-track-watering-history-per-plant-8395b2548be1",
				SHA:    "e9fdc70598d1ebc72cf03313a4a5940c6104745e",
				Added:  []string{"backlog/tasks/task-010 - Create-watering-history-model.md"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := InputFromEvent(tt.ev); ok {
				t.Error("InputFromEvent accepted an unreviewable event")
			}
		})
	}
}

// --- Refinement naming, story metadata, MR content (TASK-8) ---

func TestRefinementBranchName(t *testing.T) {
	tests := []struct {
		storyPath, sha, want string
	}{
		{
			"backlog/tasks/task-001 - Track-watering-history-per-plant.md",
			"8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
			"refine/task-001-track-watering-history-per-plant-8ef0de217a18",
		},
		{"backlog/tasks/task-1.md", "deadbeef", "refine/task-1-deadbeef"},
		{"backlog/tasks/Story With Spaces.md", "abc123", "refine/story-with-spaces-abc123"},
		{"backlog/tasks/UPPER-case.md", "abc123", "refine/upper-case-abc123"},
	}
	for _, tt := range tests {
		if got := RefinementBranchName(tt.storyPath, tt.sha); got != tt.want {
			t.Errorf("RefinementBranchName(%q, %q) = %q, want %q",
				tt.storyPath, tt.sha, got, tt.want)
		}
	}
}

func TestParseStoryMeta(t *testing.T) {
	// Full backlog.md frontmatter, with a folded multi-line title.
	const withFrontmatter = `---
id: TASK-042
title: >-
  Implement a multi-line user story title
status: To Do
---

## Description

Body`
	meta := parseStoryMeta(withFrontmatter)
	if meta.ID != "TASK-042" || meta.Title != "Implement a multi-line user story title" {
		t.Errorf("meta = %+v, want TASK-042 / Implement a multi-line user story title", meta)
	}

	// No frontmatter: empty meta, callers fall back to the file name.
	if meta := parseStoryMeta("# Just a heading\n\nBody"); meta != (storyMeta{}) {
		t.Errorf("meta = %+v, want empty for no frontmatter", meta)
	}

	// Unterminated frontmatter: treated as absent.
	if meta := parseStoryMeta("---\nid: TASK-1\n"); meta != (storyMeta{}) {
		t.Errorf("meta = %+v, want empty for unterminated frontmatter", meta)
	}

	// Frontmatter without id/title: empty fields.
	if meta := parseStoryMeta("---\nstatus: To Do\n---\n"); meta != (storyMeta{}) {
		t.Errorf("meta = %+v, want empty fields", meta)
	}
}

func TestBuildMRTitle(t *testing.T) {
	c := mrContent{
		storyPath: "backlog/tasks/task-1.md",
		meta:      storyMeta{ID: "TASK-001", Title: "Add user login"},
	}
	if got := buildMRTitle(c); got != "Refine TASK-001: Add user login" {
		t.Errorf("buildMRTitle = %q, want %q", got, "Refine TASK-001: Add user login")
	}

	// Without frontmatter, the title falls back to the file name.
	c = mrContent{storyPath: "backlog/tasks/task-1 - Add-login.md"}
	if got := buildMRTitle(c); got != "Refine user story: task-1 - Add-login" {
		t.Errorf("buildMRTitle fallback = %q, want %q", got, "Refine user story: task-1 - Add-login")
	}
}

func TestBuildMRDescription(t *testing.T) {
	c := mrContent{
		storyPath: "backlog/tasks/task-001 - Add-user-login.md",
		meta:      storyMeta{ID: "TASK-001", Title: "Add user login"},
		commit: Input{
			SHA:     "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
			Message: "Refine user story\n\nLong body",
			Author:  "root",
			Branch:  "main",
		},
		tickets: []Ticket{
			{Title: "Add login endpoint", Description: "## Context\nNeed auth."},
			{Title: "Add login tests", Description: "## Context\nCoverage."},
		},
		ticketFiles: []string{
			"backlog/tasks/task-002 - Add-login-endpoint.md",
			"backlog/tasks/task-003 - Add-login-tests.md",
		},
		questions: []string{"How long should the JWT be valid?"},
	}
	d := buildMRDescription(c)
	for _, want := range []string{
		"TASK-001: Add user login",
		"backlog/tasks/task-001 - Add-user-login.md",
		"8ef0de217a18",
		"\"Refine user story\"", // the commit's first line
		"root",
		"`main`",
		"**Add login endpoint**",
		"task-002 - Add-login-endpoint.md",
		"**Add login tests**",
		"## Questions for you",
		"How long should the JWT be valid?",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("description misses %q:\n%s", want, d)
		}
	}

	// Without questions, the section is omitted entirely.
	c.questions = nil
	if d := buildMRDescription(c); strings.Contains(d, "Questions") {
		t.Errorf("description asks questions without any:\n%s", d)
	}
}

// --- Template wiring ---

func TestTemplate(t *testing.T) {
	tmpl := Template(provider.NewFake(), llm.NewFake())
	if tmpl.Name() == "" {
		t.Error("template name is empty")
	}

	workflows := tmpl.Workflows()
	if len(workflows) != 3 {
		t.Fatalf("workflows = %+v, want three", workflows)
	}
	wantWorkflows := []struct {
		name string
		fn   any
	}{
		{WorkflowName, UserStoryReviewWorkflow},
		{MrCommentWorkflowName, MrCommentWorkflow},
		{MrCommentProcessorWorkflowName, MrCommentProcessorWorkflow},
	}
	for i, want := range wantWorkflows {
		if workflows[i].Name != want.name || workflows[i].Fn == nil {
			t.Errorf("workflows[%d] = %+v, want %q with a function", i, workflows[i], want.name)
		}
	}
	activities := tmpl.Activities()
	if len(activities) != 11 {
		t.Fatalf("activities = %d, want 11 (analyzeCommit, fetchStory, generateTickets, createRefinementBranch, commitTickets, openMergeRequest, findRefinement, collectTickets, interpretComment, applyTicketUpdates, postResponseComment)", len(activities))
	}
	wantNames := []string{
		AnalyzeCommitName,
		FetchStoryName,
		GenerateTicketsName,
		CreateRefinementBranchName,
		CommitTicketsName,
		OpenMergeRequestName,
		FindRefinementName,
		CollectTicketsName,
		InterpretCommentName,
		ApplyTicketUpdatesName,
		PostResponseCommentName,
	}
	for i, want := range wantNames {
		if activities[i].Name != want {
			t.Errorf("activities[%d].Name = %q, want %q", i, activities[i].Name, want)
		}
		if activities[i].Fn == nil {
			t.Errorf("activities[%d].Fn is nil", i)
		}
	}

	// The commit trigger starts the review workflow; the comment
	// trigger starts the mr comment workflow.
	triggers := tmpl.Triggers()
	if len(triggers) != 2 {
		t.Fatalf("triggers = %+v, want two", triggers)
	}
	if triggers[0].Kind != event.KindCommit || triggers[0].Workflow != WorkflowName || triggers[0].Input == nil {
		t.Errorf("triggers[0] = %+v, want %q on %q", triggers[0], event.KindCommit, WorkflowName)
	}
	if triggers[1].Kind != event.KindComment || triggers[1].Workflow != MrCommentWorkflowName || triggers[1].Input == nil {
		t.Errorf("triggers[1] = %+v, want %q on %q", triggers[1], event.KindComment, MrCommentWorkflowName)
	}
}

// The template must satisfy worker.Template so the runner can register it.
var _ worker.Template = Template(nil, nil)

// --- Activity environment tests ---

func TestAnalyzeCommitActivityEnv(t *testing.T) {
	// The activity runs through the SDK's activity test environment.
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	acts := NewActivities(provider.NewFake(), llm.NewFake())
	env.RegisterActivityWithOptions(acts.AnalyzeCommit, activity.RegisterOptions{Name: AnalyzeCommitName})

	in := validInput()
	in.Added = []string{"backlog/tasks/task-1.md"}
	encoded, err := env.ExecuteActivity(acts.AnalyzeCommit, in)
	if err != nil {
		t.Fatalf("AnalyzeCommit failed: %v", err)
	}
	var analysis Analysis
	if err := encoded.Get(&analysis); err != nil {
		t.Fatalf("decoding activity result: %v", err)
	}
	if !analysis.NeedsRefinement {
		t.Error("NeedsRefinement = false, want true for story file")
	}
	if len(analysis.StoryPaths) != 1 || analysis.StoryPaths[0] != "backlog/tasks/task-1.md" {
		t.Errorf("StoryPaths = %v, want [backlog/tasks/task-1.md]", analysis.StoryPaths)
	}

	if _, err := env.ExecuteActivity(acts.AnalyzeCommit, Input{}); err == nil {
		t.Error("AnalyzeCommit accepted an invalid payload")
	}
}
