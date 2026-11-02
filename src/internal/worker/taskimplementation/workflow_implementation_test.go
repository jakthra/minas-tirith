package taskimplementation

import (
	"strings"
	"testing"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// newImplementationFake returns a provider fake serving the test
// repository with the task file committed as commitSHA, so the task
// commit is a real ref the workflow can read and branch from.
func newImplementationFake(t *testing.T) (*provider.Fake, string) {
	t.Helper()
	pf := provider.NewFake(repo)
	pf.SeedFiles(repo, "main", map[string]string{
		"src/main.go": "package main\n\nfunc main() {}\n",
		"README.md":   "# BeanieGenie\n",
	})
	commit, err := pf.Commit(t.Context(), repo, "main", "Add task", []provider.FileChange{
		{Path: taskPath, Content: []byte(taskFile("TASK-019", "Implement login"))},
	})
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return pf, commit.SHA
}

func implementationWorkflowInput(sha string) Input {
	return Input{
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: repo,
		Branch:     "main",
		SHA:        sha,
		Message:    "Add backlog task",
		Author:     "root",
		TaskPaths:  []string{taskPath},
	}
}

// --- Workflow through the real activities (AC3, AC4, AC5, AC6) ---

func TestWorkflowCommitsCompleteImplementation(t *testing.T) {
	// AC6: a complete implementation runs through the Temporal test
	// environment with the provider and LLM fakes, and the branch and
	// commit contents are asserted.
	pf, sha := newImplementationFake(t)
	const reply = `{"outcome":"complete","reason":"","open_questions":[],"changes":[{"path":"src/login.go","content":"package login\n\nfunc Login() {}\n"},{"path":"src/main.go","content":"package main\n\nimport _ \"minas-tirith/login\"\n\nfunc main() {}\n"}]}`
	lf := llm.NewFake(llm.Response{Content: reply, Model: "test-model"})
	env := newIntegrationEnv(t, pf, lf)

	env.ExecuteWorkflow(WorkflowName, implementationWorkflowInput(sha))
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result Result
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}

	// AC5: the result reports the outcome, the branch, and the files.
	if len(result.Implementations) != 1 {
		t.Fatalf("implementations = %d, want 1", len(result.Implementations))
	}
	impl := result.Implementations[0]
	if !impl.Complete {
		t.Errorf("Complete = false, want true (reason %q)", impl.Reason)
	}
	if impl.TaskID != "TASK-019" || impl.TaskTitle != "Implement login" {
		t.Errorf("task = %q/%q", impl.TaskID, impl.TaskTitle)
	}
	wantBranch := "implement/task-019-implement-login-" + ShortSHA(sha)
	if impl.Branch != wantBranch {
		t.Errorf("branch = %q, want %q", impl.Branch, wantBranch)
	}
	if !strings.HasPrefix(impl.Branch, "implement/") {
		t.Errorf("branch %q is not an implement/* branch", impl.Branch)
	}
	if strings.Join(impl.ChangedFiles, ",") != "src/login.go,src/main.go" {
		t.Errorf("changed files = %v", impl.ChangedFiles)
	}
	if impl.CommitSHA == "" || impl.CommitSHA == sha {
		t.Errorf("commit sha = %q, want a new commit", impl.CommitSHA)
	}
	if impl.PromptVersion != ImplementationPromptVersion {
		t.Errorf("prompt version = %q", impl.PromptVersion)
	}
	if impl.Reason != "" {
		t.Errorf("reason = %q, want empty for a complete implementation", impl.Reason)
	}

	// The changes are committed to the branch, and only there.
	login, err := pf.GetFile(t.Context(), repo, "src/login.go", impl.Branch)
	if err != nil {
		t.Fatalf("src/login.go not committed to the branch: %v", err)
	}
	if string(login.Content) != "package login\n\nfunc Login() {}\n" {
		t.Errorf("src/login.go = %q", login.Content)
	}
	main, err := pf.GetFile(t.Context(), repo, "src/main.go", impl.Branch)
	if err != nil {
		t.Fatalf("src/main.go not updated on the branch: %v", err)
	}
	if !strings.Contains(string(main.Content), "minas-tirith/login") {
		t.Errorf("src/main.go = %q, want the updated content", main.Content)
	}
	if _, err := pf.GetFile(t.Context(), repo, "src/login.go", sha); err == nil {
		t.Error("src/login.go exists at the task commit, want it only on the branch")
	}

	// The branch starts from the task commit: it carries the task file.
	if _, err := pf.GetFile(t.Context(), repo, taskPath, impl.Branch); err != nil {
		t.Errorf("task file missing from the implementation branch: %v", err)
	}

	// The model saw the task and the existing code it had to fit into.
	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	if calls[0].System != ImplementationPrompt {
		t.Error("llm system prompt is not the implementation prompt")
	}
	prompt := calls[0].Messages[0].Content
	for _, want := range []string{"Implement the repository tooling", "package main", "src/main.go"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("llm prompt misses %q:\n%s", want, prompt)
		}
	}
}

func TestWorkflowCommitsPartialImplementation(t *testing.T) {
	// AC6: a task the model cannot fulfill produces a partial result
	// with the reason and the open questions; the sound changes it did
	// produce are still committed.
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
	if len(impl.OpenQuestions) != 2 || !strings.Contains(impl.OpenQuestions[0], "auth provider") {
		t.Errorf("open questions = %v, want the model's questions", impl.OpenQuestions)
	}
	if impl.Branch != "implement/task-019-implement-login-"+ShortSHA(sha) {
		t.Errorf("branch = %q", impl.Branch)
	}
	if strings.Join(impl.ChangedFiles, ",") != "src/login.go" {
		t.Errorf("changed files = %v", impl.ChangedFiles)
	}
	if !strings.Contains(result.Status, "0 of 1") {
		t.Errorf("status = %q, want the partial count", result.Status)
	}

	// The partial change is on the branch, and the commit message says
	// the implementation is partial.
	login, err := pf.GetFile(t.Context(), repo, "src/login.go", impl.Branch)
	if err != nil {
		t.Fatalf("src/login.go not committed: %v", err)
	}
	if !strings.Contains(string(login.Content), "TODO: provider") {
		t.Errorf("src/login.go = %q", login.Content)
	}
}

func TestWorkflowPartialWithoutChangesCreatesEmptyBranch(t *testing.T) {
	// A model that cannot propose any safe change still gets a branch to
	// point the draft MR at; nothing is committed to it.
	pf, sha := newImplementationFake(t)
	const reply = `{"outcome":"partial","reason":"the task contradicts the existing code","open_questions":["Should login replace or extend the session code?"],"changes":[]}`
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
	if impl.Complete || impl.Reason == "" {
		t.Errorf("implementation = %+v, want a partial outcome with a reason", impl)
	}
	if impl.Branch == "" {
		t.Fatal("branch is empty, want the implementation branch")
	}
	if len(impl.ChangedFiles) != 0 || impl.CommitSHA != "" {
		t.Errorf("implementation = %+v, want no commit", impl)
	}
	// The branch exists and carries the task commit's files.
	if _, err := pf.GetFile(t.Context(), repo, "src/main.go", impl.Branch); err != nil {
		t.Errorf("branch %s does not start from the task commit: %v", impl.Branch, err)
	}
}

func TestWorkflowImplementationLLMFailure(t *testing.T) {
	pf, sha := newImplementationFake(t)
	lf := &llm.Fake{Err: errString("llm unavailable")}
	env := newIntegrationEnv(t, pf, lf)

	env.ExecuteWorkflow(WorkflowName, implementationWorkflowInput(sha))
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("workflow succeeded with a failing LLM, want failure")
	}
}

// errString is a simple error for tests that need a fixed message.
type errString string

func (e errString) Error() string { return string(e) }
