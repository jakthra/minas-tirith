package taskimplementation

import (
	"fmt"
	"strings"
	"testing"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// newContextFake returns a provider fake with a small repository and the
// task commit, so the context loop can read and search real files.
func newContextFake(t *testing.T) (*provider.Fake, string) {
	t.Helper()
	pf := provider.NewFake(repo)
	pf.SeedFiles(repo, "main", map[string]string{
		"src/main.go":              "package main\n\nfunc main() {}\n",
		"src/worker/login.go":      "package worker\n\nfunc Login() {}\n",
		"src/worker/session.go":    "package worker\n\nfunc Session() {}\n",
		"src/worker/login_test.go": "package worker\n\nimport \"testing\"\n\nfunc TestLogin(t *testing.T) {}\n",
		"README.md":                "# BeanieGenie\n",
	})
	commit, err := pf.Commit(t.Context(), repo, "main", "Add task", []provider.FileChange{
		{Path: taskPath, Content: []byte(taskFile("TASK-019", "Implement login"))},
	})
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return pf, commit.SHA
}

const completeReply = `{"outcome":"complete","reason":"","open_questions":[],"changes":[{"path":"src/worker/login.go","content":"package worker\n\nfunc Login() {}\n"}]}`

// requestReply renders a request round's reply.
func requestReply(requests ...string) llm.Response {
	return llm.Response{Content: `{"outcome":"request","requests":[` + strings.Join(requests, ",") + `]}`}
}

func readRequest(paths ...string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, `"`+p+`"`)
	}
	return `{"kind":"read","paths":[` + strings.Join(quoted, ",") + `]}`
}

func runImplementationWorkflow(t *testing.T, pf *provider.Fake, lf llm.Client, sha string) Result {
	t.Helper()
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
	return result
}

// --- AC1, AC2, AC6: the model requests context across several rounds ---

func TestWorkflowModelRequestsContextAcrossRounds(t *testing.T) {
	// AC1/AC2/AC6: the model lists, searches, and reads files over two
	// request rounds, receives the results, and only then proposes. The
	// requests run through the repository tooling activities.
	pf, sha := newContextFake(t)
	lf := llm.NewFake(
		requestReply(`{"kind":"list","path":"src/worker"}`),
		requestReply(`{"kind":"search","query":"session"}`, readRequest("src/worker/login_test.go")),
		llm.Response{Content: completeReply, Model: "test-model"},
	)

	result := runImplementationWorkflow(t, pf, lf, sha)
	impl := result.Implementations[0]
	if !impl.Complete {
		t.Fatalf("Complete = false, want true (reason %q)", impl.Reason)
	}
	if impl.ContextRounds != 2 {
		t.Errorf("ContextRounds = %d, want 2", impl.ContextRounds)
	}
	if len(impl.ContextRefusals) != 0 {
		t.Errorf("refusals = %v, want none", impl.ContextRefusals)
	}
	if strings.Join(impl.ChangedFiles, ",") != "src/worker/login.go" {
		t.Errorf("changed files = %v", impl.ChangedFiles)
	}

	calls := lf.Calls()
	if len(calls) != 3 {
		t.Fatalf("llm calls = %d, want 2 request rounds and the proposal", len(calls))
	}

	// The first round sees the seed prompt: the task and the seeded
	// files.
	seed := calls[0].Messages[0].Content
	for _, want := range []string{"# Task TASK-019", "package main"} {
		if !strings.Contains(seed, want) {
			t.Errorf("seed prompt misses %q:\n%s", want, seed)
		}
	}

	// The second round replays the conversation: its own request, the
	// results of the first (the listing of src/worker), and the context
	// as it now stands.
	second := calls[1].Messages
	joined := ""
	for _, m := range second {
		joined += m.Content + "\n"
	}
	for _, want := range []string{
		`"kind":"list"`,         // the model's own request
		"src/worker/login.go",   // the listing result
		"src/worker/session.go", // the listing result
		"Context round 2 of",    // the round's place in the loop
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("second round misses %q:\n%s", want, joined)
		}
	}

	// The final round is propose-only and carries everything requested:
	// the search result and the file read in the second round.
	final := calls[2].Messages
	joined = ""
	for _, m := range final {
		joined += m.Content + "\n"
	}
	for _, want := range []string{
		"cannot issue requests",
		"src/worker/session.go",        // search match
		"func Session() {}",            // read content
		"func TestLogin(t *testing.T)", // read content
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("final round misses %q:\n%s", want, joined)
		}
	}
}

func TestWorkflowSeedNeedsNoRequestRound(t *testing.T) {
	// AC4: a task that names its files needs no extra rounds: the seed
	// carries them and the model proposes in the first round.
	pf, sha := newContextFake(t)
	lf := llm.NewFake(llm.Response{Content: completeReply})

	result := runImplementationWorkflow(t, pf, lf, sha)
	impl := result.Implementations[0]
	if !impl.Complete {
		t.Fatalf("Complete = false, want true (reason %q)", impl.Reason)
	}
	if impl.ContextRounds != 0 {
		t.Errorf("ContextRounds = %d, want 0", impl.ContextRounds)
	}
	if calls := lf.Calls(); len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
}

// --- AC3, AC5, AC6: the round bound ---

func TestWorkflowContextRoundBound(t *testing.T) {
	// AC3/AC5/AC6: a model that keeps requesting context is stopped at
	// maxContextRounds, the run does not fail, the final round is
	// propose-only, and the partial outcome names the bound.
	pf, sha := newContextFake(t)
	responses := make([]llm.Response, 0, maxContextRounds+1)
	for i := 0; i < maxContextRounds; i++ {
		responses = append(responses, requestReply(readRequest("src/worker/session.go")))
	}
	responses = append(responses, llm.Response{Content: `{"outcome":"partial","reason":"I still need to see the session code","open_questions":["What does the session code do?"],"changes":[]}`})
	lf := llm.NewFake(responses...)

	result := runImplementationWorkflow(t, pf, lf, sha)
	impl := result.Implementations[0]
	if impl.Complete {
		t.Fatal("Complete = true, want a partial outcome")
	}
	if impl.ContextRounds != maxContextRounds {
		t.Errorf("ContextRounds = %d, want %d", impl.ContextRounds, maxContextRounds)
	}
	if !strings.Contains(impl.Reason, "round bound") || !strings.Contains(impl.Reason, fmt.Sprint(maxContextRounds)) {
		t.Errorf("reason = %q, want the round bound named", impl.Reason)
	}
	if !strings.Contains(impl.Reason, "I still need to see the session code") {
		t.Errorf("reason = %q, want the model's reason kept", impl.Reason)
	}
	if impl.Branch == "" {
		t.Error("branch is empty, want the implementation branch")
	}

	calls := lf.Calls()
	if len(calls) != maxContextRounds+1 {
		t.Fatalf("llm calls = %d, want %d", len(calls), maxContextRounds+1)
	}
	final := calls[len(calls)-1].Messages
	joined := ""
	for _, m := range final {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "propose only") || !strings.Contains(joined, "cannot issue requests") {
		t.Errorf("final round is not propose-only:\n%s", joined)
	}
	// The last request round is told it is the final chance to request.
	lastRequestRound := calls[len(calls)-2].Messages
	last := lastRequestRound[len(lastRequestRound)-1].Content
	if !strings.Contains(last, "last context round") {
		t.Errorf("last request round prompt = %q, want the final-chance notice", last)
	}
}

// --- AC3, AC6: the budget bound ---

func TestWorkflowContextBudgetBound(t *testing.T) {
	// AC3/AC6: a request that would exceed the aggregate context budget
	// is refused, the run does not fail, and the partial outcome names
	// the refused requests so it is not misreported as missing
	// information.
	pf, _ := newContextFake(t)
	big := strings.Repeat("x", maxContextBudgetBytes+1)
	if _, err := pf.Commit(t.Context(), repo, "main", "Add a huge file", []provider.FileChange{
		{Path: "vendor/huge.go", Content: []byte("package vendor\n" + big)},
	}); err != nil {
		t.Fatalf("seed huge file: %v", err)
	}
	// Commit the task again so the workflow reads at a ref carrying the
	// huge file.
	commit, err := pf.Commit(t.Context(), repo, "main", "Add task", []provider.FileChange{
		{Path: taskPath, Content: []byte(taskFile("TASK-019", "Implement login"))},
	})
	if err != nil {
		t.Fatalf("seed task commit: %v", err)
	}

	lf := llm.NewFake(
		requestReply(readRequest("vendor/huge.go")),
		llm.Response{Content: `{"outcome":"partial","reason":"the vendor code is missing","open_questions":[],"changes":[]}`},
	)

	result := runImplementationWorkflow(t, pf, lf, commit.SHA)
	impl := result.Implementations[0]
	if impl.Complete {
		t.Fatal("Complete = true, want a partial outcome")
	}
	if len(impl.ContextRefusals) != 1 {
		t.Fatalf("refusals = %+v, want the refused read", impl.ContextRefusals)
	}
	if got := impl.ContextRefusals[0].Request; got.Kind != ContextRequestRead || strings.Join(got.Paths, ",") != "vendor/huge.go" {
		t.Errorf("refused request = %+v", got)
	}
	if !strings.Contains(impl.Reason, "context budget") || !strings.Contains(impl.Reason, "vendor/huge.go") {
		t.Errorf("reason = %q, want the budget bound and the refused request named", impl.Reason)
	}
	if !strings.Contains(impl.Reason, "the vendor code is missing") {
		t.Errorf("reason = %q, want the model's reason kept", impl.Reason)
	}

	// The round's results are told which requests were refused, so the
	// model can propose with what it has.
	calls := lf.Calls()
	if len(calls) != 2 {
		t.Fatalf("llm calls = %d, want the request round and the proposal", len(calls))
	}
	results := calls[len(calls)-1].Messages
	joined := ""
	for _, m := range results {
		joined += m.Content + "\n"
	}
	for _, want := range []string{"## Results of your 1 context request(s)", "Refused requests", "vendor/huge.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("results turn misses %q:\n%s", want, joined)
		}
	}
	// The refused file's content never reaches the model.
	for _, c := range calls {
		for _, m := range c.Messages {
			if strings.Contains(m.Content, big) {
				t.Fatal("the refused file's content was sent to the model")
			}
		}
	}
}

func TestWorkflowContextBudgetRefusalKeepsCompleteOutcome(t *testing.T) {
	// AC3: a refused request does not fail the run, and a complete
	// outcome stays complete: the bound is only folded into a partial
	// reason.
	pf, _ := newContextFake(t)
	if _, err := pf.Commit(t.Context(), repo, "main", "Add a huge file", []provider.FileChange{
		{Path: "vendor/huge.go", Content: []byte("package vendor\n" + strings.Repeat("x", maxContextBudgetBytes+1))},
	}); err != nil {
		t.Fatalf("seed huge file: %v", err)
	}
	commit, err := pf.Commit(t.Context(), repo, "main", "Add task", []provider.FileChange{
		{Path: taskPath, Content: []byte(taskFile("TASK-019", "Implement login"))},
	})
	if err != nil {
		t.Fatalf("seed task commit: %v", err)
	}
	lf := llm.NewFake(
		requestReply(readRequest("vendor/huge.go")),
		llm.Response{Content: completeReply},
	)

	result := runImplementationWorkflow(t, pf, lf, commit.SHA)
	impl := result.Implementations[0]
	if !impl.Complete {
		t.Fatalf("Complete = false, want true (reason %q)", impl.Reason)
	}
	if len(impl.ContextRefusals) != 1 {
		t.Errorf("refusals = %+v, want the refused read recorded", impl.ContextRefusals)
	}
	if impl.Reason != "" {
		t.Errorf("reason = %q, want empty for a complete implementation", impl.Reason)
	}
}

func TestContextBoundReason(t *testing.T) {
	refusals := []ContextRefusal{{
		Request: ContextRequest{Kind: ContextRequestRead, Paths: []string{"a.go"}},
		Reason:  "too big",
	}}
	tests := []struct {
		name       string
		reason     string
		rounds     bool
		want       []string
		notWant    string
		noRefusals bool
	}{
		{
			name:       "no bound",
			reason:     "ambiguous task",
			want:       []string{"ambiguous task"},
			notWant:    "context bound",
			noRefusals: true,
		},
		{
			name:   "round bound only",
			reason: "ambiguous task",
			rounds: true,
			want:   []string{"ambiguous task", "round bound", "context bound"},
		},
		{
			name:   "budget bound only",
			reason: "",
			want:   []string{"context budget", "read a.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rs []ContextRefusal
			if !tt.noRefusals {
				rs = refusals
			}
			got := contextBoundReason(tt.reason, rs, tt.rounds)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("reason = %q, want %q", got, want)
				}
			}
			if tt.notWant != "" && strings.Contains(got, tt.notWant) {
				t.Errorf("reason = %q, want no %q", got, tt.notWant)
			}
		})
	}
}
