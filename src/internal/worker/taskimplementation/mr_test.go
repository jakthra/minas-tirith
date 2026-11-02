package taskimplementation

import (
	"strings"
	"testing"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// --- MR title and description (AC1, AC2, AC3, AC4) ---

func completeImplementation() TaskImplementation {
	return TaskImplementation{
		TaskPath:     taskPath,
		TaskID:       "TASK-019",
		TaskTitle:    "Implement login",
		Complete:     true,
		Branch:       "implement/task-019-implement-login-8ef0de217a18",
		ChangedFiles: []string{"src/login.go", "src/main.go"},
		CommitSHA:    "implsha",
	}
}

func partialImplementation() TaskImplementation {
	return TaskImplementation{
		TaskPath:      taskPath,
		TaskID:        "TASK-019",
		TaskTitle:     "Implement login",
		Complete:      false,
		Reason:        "the task does not say which auth provider to use",
		OpenQuestions: []string{"Which auth provider should login use?", "Should sessions expire?"},
		Branch:        "implement/task-019-implement-login-8ef0de217a18",
		ChangedFiles:  []string{"src/login.go"},
		CommitSHA:     "implsha",
	}
}

func mrContent(impl TaskImplementation) MergeRequestContent {
	return MergeRequestContent{
		Implementation: impl,
		Repository:     repo,
		Branch:         "main",
		CommitSHA:      commitSHA,
		CommitMessage:  "Add backlog task",
		CommitAuthor:   "root",
		ProviderURL:    "http://localhost:3000",
	}
}

func TestBuildMergeRequestTitle(t *testing.T) {
	// AC1/AC2: the ticket number is in the title; a partial
	// implementation is marked as a draft in the title too.
	if got := buildMergeRequestTitle(mrContent(completeImplementation())); got != "Implement TASK-019: Implement login" {
		t.Errorf("complete title = %q", got)
	}
	if got := buildMergeRequestTitle(mrContent(partialImplementation())); got != "Implement (draft) TASK-019: Implement login" {
		t.Errorf("partial title = %q", got)
	}
	// A task without a frontmatter title falls back to the file name.
	impl := completeImplementation()
	impl.TaskTitle = ""
	impl.TaskID = ""
	if got := buildMergeRequestTitle(mrContent(impl)); !strings.Contains(got, "task-19") {
		t.Errorf("title without metadata = %q, want the task file name", got)
	}
}

func TestBuildMergeRequestDescriptionComplete(t *testing.T) {
	// AC1: the description links the implemented task and lists the
	// changes; it carries no reason, questions, or refiner hand-off.
	desc := buildMergeRequestDescription(mrContent(completeImplementation()))
	for _, want := range []string{
		"TASK-019: Implement login",
		"http://localhost:3000/" + repo + "/src/branch/HEAD/backlog/tasks/task-19%20-%20Implement%20login.md",
		"**complete**",
		"`src/login.go`",
		"`src/main.go`",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description misses %q:\n%s", want, desc)
		}
	}
	for _, unwanted := range []string{"draft", "Why it could not be fulfilled", "Open questions", RefinerAgentHandle} {
		if strings.Contains(desc, unwanted) {
			t.Errorf("complete description carries %q:\n%s", unwanted, desc)
		}
	}
}

func TestBuildMergeRequestDescriptionPartial(t *testing.T) {
	// AC2: the description states the reason and the open questions.
	desc := buildMergeRequestDescription(mrContent(partialImplementation()))
	for _, want := range []string{
		"**draft**",
		"## Why it could not be fulfilled",
		"the task does not say which auth provider to use",
		"## Open questions",
		"1. Which auth provider should login use?",
		"2. Should sessions expire?",
		RefinerAgentHandle,
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description misses %q:\n%s", want, desc)
		}
	}
}

func TestBuildRefinerHandOff(t *testing.T) {
	// AC3/AC4: a partial implementation tags the refiner with the open
	// questions; a complete one never involves the refiner.
	if got := buildRefinerHandOff(mrContent(completeImplementation())); got != "" {
		t.Errorf("complete hand-off = %q, want none", got)
	}
	handOff := buildRefinerHandOff(mrContent(partialImplementation()))
	if !strings.HasPrefix(handOff, RefinerAgentHandle) {
		t.Errorf("hand-off does not start with the refiner tag:\n%s", handOff)
	}
	for _, want := range []string{"TASK-019", "auth provider", "Should sessions expire?"} {
		if !strings.Contains(handOff, want) {
			t.Errorf("hand-off misses %q:\n%s", want, handOff)
		}
	}
}

// --- SubmitMergeRequest activity (AC1, AC2, AC3, AC4) ---

func TestSubmitMergeRequestComplete(t *testing.T) {
	pf := provider.NewFake(repo)
	pf.SeedFiles(repo, "main", map[string]string{"src/main.go": "package main\n"})
	pf.SeedFiles(repo, completeImplementation().Branch, map[string]string{"src/main.go": "package main\n"})
	a := NewActivities(pf, nil)

	got, err := a.SubmitMergeRequest(t.Context(), SubmitMergeRequestInput{
		Repository:     repo,
		Implementation: completeImplementation(),
		Base:           "main",
		CommitSHA:      commitSHA,
		CommitMessage:  "Add backlog task",
		CommitAuthor:   "root",
	})
	if err != nil {
		t.Fatalf("SubmitMergeRequest: %v", err)
	}
	if got.Draft {
		t.Error("Draft = true, want a regular MR for a complete implementation")
	}
	if got.RefinerCommentID != 0 {
		t.Errorf("refiner comment = %d, want none", got.RefinerCommentID)
	}
	mr, err := pf.GetMergeRequest(t.Context(), repo, got.Number)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if mr.Draft || mr.Head != completeImplementation().Branch || mr.Base != "main" {
		t.Errorf("mr = %+v, want a non-draft MR from the implementation branch to main", mr)
	}
	if !strings.Contains(mr.Title, "TASK-019") {
		t.Errorf("title = %q, want the ticket number", mr.Title)
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), repo, mr.Number)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("comments = %v, want none for a complete implementation", comments)
	}
}

func TestSubmitMergeRequestPartial(t *testing.T) {
	pf := provider.NewFake(repo)
	pf.SeedFiles(repo, "main", map[string]string{"src/main.go": "package main\n"})
	a := NewActivities(pf, nil)

	got, err := a.SubmitMergeRequest(t.Context(), SubmitMergeRequestInput{
		Repository:     repo,
		Implementation: partialImplementation(),
		Base:           "main",
		CommitSHA:      commitSHA,
	})
	if err != nil {
		t.Fatalf("SubmitMergeRequest: %v", err)
	}
	if !got.Draft {
		t.Error("Draft = false, want a draft MR for a partial implementation")
	}
	mr, err := pf.GetMergeRequest(t.Context(), repo, got.Number)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if !mr.Draft {
		t.Error("provider MR is not a draft")
	}
	if !strings.Contains(mr.Description, "Why it could not be fulfilled") ||
		!strings.Contains(mr.Description, "Should sessions expire?") {
		t.Errorf("description misses the reason or questions:\n%s", mr.Description)
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), repo, mr.Number)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 || !strings.HasPrefix(comments[0].Body, RefinerAgentHandle) {
		t.Fatalf("comments = %v, want one refiner hand-off", comments)
	}
	if got.RefinerCommentID != comments[0].ID {
		t.Errorf("RefinerCommentID = %d, want %d", got.RefinerCommentID, comments[0].ID)
	}
}

func TestSubmitMergeRequestValidatesInput(t *testing.T) {
	a := NewActivities(provider.NewFake(repo), nil)
	impl := completeImplementation()
	tests := []struct {
		name string
		in   SubmitMergeRequestInput
	}{
		{"no repository", SubmitMergeRequestInput{Implementation: impl, Base: "main"}},
		{"no base", SubmitMergeRequestInput{Repository: repo, Implementation: impl}},
		{"branch outside the agent namespace", SubmitMergeRequestInput{
			Repository: repo, Base: "main",
			Implementation: func() TaskImplementation { i := impl; i.Branch = "main"; return i }(),
		}},
		{"base on an agent branch", SubmitMergeRequestInput{
			Repository: repo, Base: "refine/story-1",
			Implementation: impl,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := a.SubmitMergeRequest(t.Context(), tt.in); err == nil {
				t.Error("SubmitMergeRequest accepted invalid input")
			}
		})
	}
}

// --- Workflow-level MR submission (AC1–AC6) ---

func TestWorkflowCompleteImplementationOpensRegularMR(t *testing.T) {
	// AC1, AC4, AC5, AC6: a complete implementation opens a non-draft MR
	// from the implement/* branch to the task commit's branch, titled
	// with the ticket number and linking the task, and never involves
	// the refiner.
	pf, sha := newImplementationFake(t)
	const reply = `{"outcome":"complete","reason":"","open_questions":[],"changes":[{"path":"src/login.go","content":"package login\n\nfunc Login() {}\n"}]}`
	lf := llm.NewFake(llm.Response{Content: reply})
	env := newIntegrationEnv(t, pf, lf)

	env.ExecuteWorkflow(WorkflowName, implementationWorkflowInput(sha))
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	impl := result.Implementations[0]
	if impl.MergeRequest == nil {
		t.Fatal("implementation has no merge request")
	}
	if result.MergeRequestURL != impl.MergeRequest.URL {
		t.Errorf("result MR URL = %q, want %q", result.MergeRequestURL, impl.MergeRequest.URL)
	}

	mr, err := pf.GetMergeRequest(t.Context(), repo, impl.MergeRequest.Number)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if mr.Draft {
		t.Error("MR is a draft, want a regular MR for a complete implementation")
	}
	if mr.Head != impl.Branch || mr.Base != "main" {
		t.Errorf("MR %s -> %s, want %s -> main", mr.Head, mr.Base, impl.Branch)
	}
	if !strings.Contains(mr.Title, "TASK-019") || !strings.HasPrefix(mr.Title, "Implement ") {
		t.Errorf("title = %q, want the ticket number and the implement prefix", mr.Title)
	}
	if !strings.Contains(mr.Description, taskPath) || !strings.Contains(mr.Description, "TASK-019") {
		t.Errorf("description does not link the implemented task:\n%s", mr.Description)
	}
	if strings.Contains(mr.Description, "Open questions") || strings.Contains(mr.Description, RefinerAgentHandle) {
		t.Errorf("complete description involves the refiner:\n%s", mr.Description)
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), repo, mr.Number)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("comments = %v, want none for a complete implementation", comments)
	}
	if impl.MergeRequest.Draft || impl.MergeRequest.RefinerCommentID != 0 {
		t.Errorf("submission = %+v, want a regular MR with no refiner hand-off", impl.MergeRequest)
	}
}

func TestWorkflowPartialImplementationOpensDraftMRWithRefiner(t *testing.T) {
	// AC2, AC3, AC5, AC6: a partial implementation opens a draft MR
	// carrying the work done so far, with the reason and open questions
	// in the description, and hands the task to the refiner by tagging
	// it on the MR.
	pf, sha := newImplementationFake(t)
	const reply = `{"outcome":"partial","reason":"the task does not say which auth provider to use","open_questions":["Which auth provider should login use?","Should sessions expire?"],"changes":[{"path":"src/login.go","content":"package login\n\n// TODO: provider\n"}]}`
	lf := llm.NewFake(llm.Response{Content: reply})
	env := newIntegrationEnv(t, pf, lf)

	env.ExecuteWorkflow(WorkflowName, implementationWorkflowInput(sha))
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	impl := result.Implementations[0]
	if impl.MergeRequest == nil {
		t.Fatal("implementation has no merge request")
	}
	if result.MergeRequestURL != impl.MergeRequest.URL || result.MergeRequestURL == "" {
		t.Errorf("result MR URL = %q, want the draft MR", result.MergeRequestURL)
	}

	mr, err := pf.GetMergeRequest(t.Context(), repo, impl.MergeRequest.Number)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if !mr.Draft {
		t.Error("MR is not a draft, want a draft for a partial implementation")
	}
	if mr.Head != impl.Branch || mr.Base != "main" {
		t.Errorf("MR %s -> %s, want %s -> main", mr.Head, mr.Base, impl.Branch)
	}
	if !strings.Contains(mr.Title, "TASK-019") {
		t.Errorf("title = %q, want the ticket number", mr.Title)
	}
	for _, want := range []string{
		"the task does not say which auth provider to use",
		"Which auth provider should login use?",
		"Should sessions expire?",
	} {
		if !strings.Contains(mr.Description, want) {
			t.Errorf("description misses %q:\n%s", want, mr.Description)
		}
	}

	// The work done so far is on the MR's branch.
	login, err := pf.GetFile(t.Context(), repo, "src/login.go", mr.Head)
	if err != nil {
		t.Fatalf("partial work missing from the MR branch: %v", err)
	}
	if !strings.Contains(string(login.Content), "TODO: provider") {
		t.Errorf("src/login.go = %q", login.Content)
	}

	// The refiner is tagged on the draft MR, with the open questions.
	comments, err := pf.ListMergeRequestComments(t.Context(), repo, mr.Number)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want one refiner hand-off", len(comments))
	}
	if !strings.HasPrefix(comments[0].Body, RefinerAgentHandle) {
		t.Errorf("hand-off does not tag the refiner:\n%s", comments[0].Body)
	}
	if !strings.Contains(comments[0].Body, "Should sessions expire?") {
		t.Errorf("hand-off misses the open questions:\n%s", comments[0].Body)
	}
	if impl.MergeRequest.RefinerCommentID != comments[0].ID {
		t.Errorf("RefinerCommentID = %d, want %d", impl.MergeRequest.RefinerCommentID, comments[0].ID)
	}
}
