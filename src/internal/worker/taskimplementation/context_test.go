package taskimplementation

import (
	"strconv"
	"strings"
	"testing"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// --- Context request parsing ---

func TestContextRoundFromReply(t *testing.T) {
	tests := []struct {
		name         string
		reply        ImplementationReply
		wantRequests []ContextRequest
		wantImpl     bool
		wantErr      string
	}{
		{
			name: "read request",
			reply: ImplementationReply{
				Outcome: "request",
				Requests: []ContextRequest{
					{Kind: "READ", Paths: []string{" src/a.go ", "src/a.go", "src/b.go", ""}},
				},
			},
			wantRequests: []ContextRequest{{Kind: ContextRequestRead, Paths: []string{"src/a.go", "src/b.go"}}},
		},
		{
			name: "list and search requests",
			reply: ImplementationReply{
				Outcome: "request",
				Requests: []ContextRequest{
					{Kind: "list", Path: "src/worker"},
					{Kind: "search", Query: " slog "},
					{Kind: "list"},
				},
			},
			wantRequests: []ContextRequest{
				{Kind: ContextRequestList, Path: "src/worker"},
				{Kind: ContextRequestSearch, Query: "slog"},
				{Kind: ContextRequestList},
			},
		},
		{
			name:    "request without requests is rejected",
			reply:   ImplementationReply{Outcome: "request"},
			wantErr: "without any requests",
		},
		{
			name:    "unknown request kind is rejected",
			reply:   ImplementationReply{Outcome: "request", Requests: []ContextRequest{{Kind: "grep", Query: "x"}}},
			wantErr: "unknown kind",
		},
		{
			name:    "read request without paths is rejected",
			reply:   ImplementationReply{Outcome: "request", Requests: []ContextRequest{{Kind: "read"}}},
			wantErr: "no paths",
		},
		{
			name:    "read request outside the repository is rejected",
			reply:   ImplementationReply{Outcome: "request", Requests: []ContextRequest{{Kind: "read", Paths: []string{"../x.go"}}}},
			wantErr: "must not contain",
		},
		{
			name:    "search request without a query is rejected",
			reply:   ImplementationReply{Outcome: "request", Requests: []ContextRequest{{Kind: "search"}}},
			wantErr: "no query",
		},
		{
			name: "a proposal instead of requests",
			reply: ImplementationReply{
				Outcome: "complete",
				Changes: []ImplementationChange{{Path: "src/a.go", Content: "package a\n"}},
			},
			wantImpl: true,
		},
		{
			name:    "a partial proposal without a reason is rejected",
			reply:   ImplementationReply{Outcome: "partial"},
			wantErr: "without a reason",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			round, err := contextRoundFromReply("TASK-019", tt.reply)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("contextRoundFromReply: %v", err)
			}
			if tt.wantImpl {
				if round.Implementation == nil || len(round.Requests) != 0 {
					t.Fatalf("round = %+v, want a proposal", round)
				}
				return
			}
			if round.Implementation != nil {
				t.Fatalf("round = %+v, want requests", round)
			}
			if len(round.Requests) != len(tt.wantRequests) {
				t.Fatalf("requests = %+v, want %+v", round.Requests, tt.wantRequests)
			}
			for i, want := range tt.wantRequests {
				got := round.Requests[i]
				if got.Kind != want.Kind || got.Path != want.Path || got.Query != want.Query ||
					strings.Join(got.Paths, ",") != strings.Join(want.Paths, ",") {
					t.Errorf("requests[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestContextRequestString(t *testing.T) {
	tests := []struct {
		request ContextRequest
		want    string
	}{
		{ContextRequest{Kind: ContextRequestList}, "list the repository root"},
		{ContextRequest{Kind: ContextRequestList, Path: "src/worker"}, "list src/worker"},
		{ContextRequest{Kind: ContextRequestSearch, Query: "slog"}, `search for "slog"`},
		{ContextRequest{Kind: ContextRequestRead, Paths: []string{"a.go", "b.go"}}, "read a.go, b.go"},
	}
	for _, tt := range tests {
		if got := tt.request.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.request, got, tt.want)
		}
	}
}

// --- RequestContext activity ---

func contextRoundInput() RequestContextInput {
	return RequestContextInput{
		Repository:  repo,
		Ref:         commitSHA,
		Task:        implementationInput().Task,
		Context:     implementationInput().Context,
		Round:       1,
		MaxRounds:   maxContextRounds,
		BudgetBytes: maxContextBudgetBytes,
	}
}

func TestRequestContextRequests(t *testing.T) {
	// AC1: the activity returns the model's requests, and the prompt
	// tells the model where it is in the bounded loop.
	const reply = `{"outcome":"request","requests":[{"kind":"read","paths":["src/login.go"]},{"kind":"search","query":"login"}]}`
	lf := llm.NewFake(llm.Response{Content: reply, Model: "test-model"})
	acts := NewActivities(provider.NewFake(repo), lf)

	in := contextRoundInput()
	in.History = []llm.Message{{Role: llm.RoleUser, Content: seedPrompt(implementationInput())}}
	round, err := acts.RequestContext(t.Context(), in)
	if err != nil {
		t.Fatalf("RequestContext: %v", err)
	}
	if round.Implementation != nil {
		t.Fatalf("round = %+v, want requests", round)
	}
	if len(round.Requests) != 2 || round.Requests[0].Kind != ContextRequestRead || round.Requests[1].Query != "login" {
		t.Errorf("requests = %+v", round.Requests)
	}
	if round.PromptVersion != ImplementationPromptVersion || round.Model != "test-model" {
		t.Errorf("prompt version/model = %q/%q", round.PromptVersion, round.Model)
	}

	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	if calls[0].System != ImplementationPrompt || !calls[0].JSON {
		t.Error("llm call is not the implementation prompt in JSON mode")
	}
	if len(calls[0].Messages) != 2 {
		t.Fatalf("messages = %d, want the history and this round's turn", len(calls[0].Messages))
	}
	prompt := calls[0].Messages[1].Content
	for _, want := range []string{
		"Context round 1 of " + strconv.Itoa(maxContextRounds),
		"package main", // the seeded context is in the history
		"budget",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("round prompt misses %q:\n%s", want, prompt)
		}
	}
}

func TestRequestContextProposes(t *testing.T) {
	const reply = `{"outcome":"complete","reason":"","open_questions":[],"changes":[{"path":"src/login.go","content":"package login\n"}]}`
	lf := llm.NewFake(llm.Response{Content: reply})
	acts := NewActivities(provider.NewFake(repo), lf)

	round, err := acts.RequestContext(t.Context(), contextRoundInput())
	if err != nil {
		t.Fatalf("RequestContext: %v", err)
	}
	if round.Implementation == nil || !round.Implementation.Complete {
		t.Fatalf("round = %+v, want a complete proposal", round)
	}
	if len(round.Implementation.Changes) != 1 || round.Implementation.Changes[0].Path != "src/login.go" {
		t.Errorf("changes = %+v", round.Implementation.Changes)
	}
}

func TestRequestContextErrors(t *testing.T) {
	tests := []struct {
		name  string
		llm   llm.Client
		input RequestContextInput
		want  string
	}{
		{name: "no llm client", llm: nil, input: contextRoundInput(), want: "no llm client"},
		{
			name:  "missing task id",
			llm:   llm.NewFake(),
			input: RequestContextInput{Repository: repo, Round: 1},
			want:  "task id is required",
		},
		{
			name:  "not a request round",
			llm:   llm.NewFake(),
			input: RequestContextInput{Repository: repo, Task: Task{ID: "TASK-019"}},
			want:  "not a request round",
		},
		{
			name:  "llm failure",
			llm:   &llm.Fake{Err: errString("llm unavailable")},
			input: contextRoundInput(),
			want:  "llm unavailable",
		},
		{
			name:  "unknown request kind",
			llm:   llm.NewFake(llm.Response{Content: `{"outcome":"request","requests":[{"kind":"grep"}]}`}),
			input: contextRoundInput(),
			want:  "unknown kind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewActivities(provider.NewFake(repo), tt.llm).RequestContext(t.Context(), tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// --- Context accounting ---

func TestContextBytes(t *testing.T) {
	c := Context{
		Files:    []FileEntry{{Path: "a.go"}, {Path: "bb.go"}},
		Contents: []ReadFile{{Path: "a.go", Content: "hello"}, {Path: "b.go", Content: "world!"}},
	}
	// 4 + 1 + 5 + 1 for the listing, 5 + 6 for the contents.
	if got := c.Bytes(); got != 22 {
		t.Errorf("Bytes() = %d, want 22", got)
	}
}

func TestContextAddFileReplacesAndSorts(t *testing.T) {
	c := Context{Contents: []ReadFile{{Path: "b.go", Content: "old"}, {Path: "c.go", Content: "c"}}}
	c.addFile(ReadFile{Path: "b.go", Content: "new"})
	c.addFile(ReadFile{Path: "a.go", Content: "a"})
	var paths []string
	for _, f := range c.Contents {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "a.go,b.go,c.go" {
		t.Errorf("paths = %v, want sorted without duplicates", paths)
	}
	if c.Contents[1].Content != "new" {
		t.Errorf("b.go = %q, want the replacement", c.Contents[1].Content)
	}
	if !c.hasContent("a.go") || c.hasContent("z.go") {
		t.Error("hasContent reports the wrong membership")
	}
}

// --- SearchRepository activity ---

func TestSearchRepository(t *testing.T) {
	pf := provider.NewFake(repo)
	pf.SeedFiles(repo, "main", map[string]string{
		"src/worker/implementation.go":      "package worker\n",
		"src/worker/implementation_test.go": "package worker\n",
		"src/llm/client.go":                 "package llm\n",
		"README.md":                         "# readme\n",
	})
	acts := NewActivities(pf, nil)

	result, err := acts.SearchRepository(t.Context(), SearchRepositoryInput{
		Repository: repo,
		Query:      "IMPLEMENTATION",
		Ref:        "main",
	})
	if err != nil {
		t.Fatalf("SearchRepository: %v", err)
	}
	var paths []string
	for _, f := range result.Files {
		paths = append(paths, f.Path)
	}
	want := "src/worker/implementation.go,src/worker/implementation_test.go"
	if strings.Join(paths, ",") != want {
		t.Errorf("matches = %v, want %s", paths, want)
	}
	if result.Total != 2 || result.Truncated {
		t.Errorf("total/truncated = %d/%v, want 2/false", result.Total, result.Truncated)
	}
}

func TestSearchRepositoryErrors(t *testing.T) {
	tests := []struct {
		name  string
		input SearchRepositoryInput
		want  string
	}{
		{
			name:  "empty query",
			input: SearchRepositoryInput{Repository: repo, Ref: "main"},
			want:  "query is required",
		},
		{
			name:  "missing repository",
			input: SearchRepositoryInput{Query: "x", Ref: "main"},
			want:  "repository is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewActivities(provider.NewFake(repo), nil).SearchRepository(t.Context(), tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// --- Prompt ---

func TestImplementationPromptDocumentsContextProtocol(t *testing.T) {
	// AC7: the prompt is v2 and documents the protocol and its bounds.
	if ImplementationPromptVersion != "task-implementation/v2" {
		t.Errorf("ImplementationPromptVersion = %q, want task-implementation/v2", ImplementationPromptVersion)
	}
	for _, want := range []string{
		`"outcome": "request"`,
		`"kind": "list"`,
		`"kind": "search"`,
		`"kind": "read"`,
		"request rounds",
		"propose-only",
		"budget",
		"valid only after you have requested",
	} {
		if !strings.Contains(ImplementationPrompt, want) {
			t.Errorf("prompt misses %q", want)
		}
	}
}

func TestSeedContextPrefersNamedFiles(t *testing.T) {
	// AC4: the seed is the TASK-23 heuristic: the files the task names
	// first, then the top of the listing, bounded by maxContextFiles.
	task := Task{Path: "backlog/tasks/task-19.md", Content: "Change src/worker/workflow.go and src/worker/prompt.go."}
	listing := RepositoryListing{Files: []FileEntry{
		{Path: "README.md", Size: 10},
		{Path: "backlog/tasks/task-19.md", Size: 10},
		{Path: "src/worker/prompt.go", Size: 10},
		{Path: "src/worker/workflow.go", Size: 10},
		{Path: "vendor/big.go", Size: maxContextFileBytes + 1},
	}}
	got := seedContext(task, listing, task.Path)
	want := "src/worker/prompt.go,src/worker/workflow.go,README.md"
	if strings.Join(got, ",") != want {
		t.Errorf("seed = %v, want %s", got, want)
	}
}

func TestSeedContextFitsBudget(t *testing.T) {
	// The seed always fits the aggregate budget: maxContextFiles files of
	// at most maxContextFileBytes each, plus a listing of maxListedFiles
	// paths.
	seed := maxContextFiles*maxContextFileBytes + maxListedFiles*256
	if seed >= maxContextBudgetBytes {
		t.Errorf("seed bound %d >= budget %d", seed, maxContextBudgetBytes)
	}
}
