package taskimplementation

import (
	"errors"
	"strings"
	"testing"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// --- Implementation prompt (AC1, AC2) ---

func TestImplementationPromptIsVersioned(t *testing.T) {
	// AC1: the prompt is a reviewable artifact with a version, and the
	// version travels with it.
	if ImplementationPromptVersion == "" {
		t.Fatal("ImplementationPromptVersion is empty")
	}
	if !strings.Contains(ImplementationPrompt, ImplementationPromptVersion) {
		t.Errorf("prompt does not carry its version %q", ImplementationPromptVersion)
	}
	for _, want := range []string{
		"acceptance criterion", // respects the task's acceptance criteria
		"existing repository",  // implements against the real code
		`"complete"`,           // AC2: the complete outcome
		`"partial"`,            // AC2: the cannot-fulfill outcome
		`"reason"`,             // AC2: with a reason
		`"open_questions"`,     // AC2: and the open questions
		"JSON",                 // the reply format
	} {
		if !strings.Contains(ImplementationPrompt, want) {
			t.Errorf("prompt misses %q", want)
		}
	}
}

// --- implementationFromReply ---

func TestImplementationFromReply(t *testing.T) {
	tests := []struct {
		name          string
		reply         ImplementationReply
		wantComplete  bool
		wantReason    string
		wantQuestions []string
		wantChanges   []string
		wantErr       string
	}{
		{
			name: "complete implementation",
			reply: ImplementationReply{
				Outcome: "complete",
				Changes: []ImplementationChange{
					{Path: "src/login.go", Content: "package login\n"},
					{Path: "src/login_test.go", Content: "package login\n"},
				},
			},
			wantComplete: true,
			wantChanges:  []string{"src/login.go", "src/login_test.go"},
		},
		{
			name: "partial implementation with reason and questions",
			reply: ImplementationReply{
				Outcome:       "PARTIAL",
				Reason:        "  need the auth provider  ",
				OpenQuestions: []string{"Which provider?", "  ", "Sessions?"},
			},
			wantComplete:  false,
			wantReason:    "need the auth provider",
			wantQuestions: []string{"Which provider?", "Sessions?"},
		},
		{
			name:    "partial without a reason is rejected",
			reply:   ImplementationReply{Outcome: "partial"},
			wantErr: "without a reason",
		},
		{
			name:    "unknown outcome is rejected",
			reply:   ImplementationReply{Outcome: "maybe"},
			wantErr: "unknown outcome",
		},
		{
			name:    "missing outcome is rejected",
			reply:   ImplementationReply{},
			wantErr: "unknown outcome",
		},
		{
			name: "a change outside the repository is rejected",
			reply: ImplementationReply{
				Outcome: "complete",
				Changes: []ImplementationChange{{Path: "../escape.go"}},
			},
			wantErr: "invalid change",
		},
		{
			name: "the same path twice is rejected",
			reply: ImplementationReply{
				Outcome: "complete",
				Changes: []ImplementationChange{{Path: "src/a.go"}, {Path: "src/a.go"}},
			},
			wantErr: "twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			impl, err := implementationFromReply("TASK-019", tt.reply)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("implementationFromReply: %v", err)
			}
			if impl.TaskID != "TASK-019" {
				t.Errorf("task id = %q", impl.TaskID)
			}
			if impl.Complete != tt.wantComplete {
				t.Errorf("Complete = %v, want %v", impl.Complete, tt.wantComplete)
			}
			if impl.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", impl.Reason, tt.wantReason)
			}
			if strings.Join(impl.OpenQuestions, "|") != strings.Join(tt.wantQuestions, "|") {
				t.Errorf("OpenQuestions = %v, want %v", impl.OpenQuestions, tt.wantQuestions)
			}
			var paths []string
			for _, c := range impl.Changes {
				paths = append(paths, c.Path)
			}
			if strings.Join(paths, "|") != strings.Join(tt.wantChanges, "|") {
				t.Errorf("changes = %v, want %v", paths, tt.wantChanges)
			}
		})
	}
}

// --- GenerateImplementation activity (AC3) ---

func implementationInput() GenerateImplementationInput {
	return GenerateImplementationInput{
		Repository: repo,
		Ref:        commitSHA,
		Task: Task{
			Path:               taskPath,
			ID:                 "TASK-019",
			Title:              "Implement login",
			Content:            "---\nid: TASK-019\n---\n\n## Description\n\nAdd login to src/main.go.\n",
			AcceptanceCriteria: []string{"Login works"},
		},
		Files:    []FileEntry{{Path: "src/main.go", Size: 13}, {Path: "README.md", Size: 10}},
		Contents: []ReadFile{{Path: "src/main.go", Content: "package main\n"}},
	}
}

func TestGenerateImplementation(t *testing.T) {
	// AC3: the activity sends the task content, the relevant repository
	// files, and the prompt to the LLM, and returns the proposed file
	// changes plus the structured outcome.
	const reply = `{"outcome":"complete","reason":"","open_questions":[],"changes":[{"path":"src/login.go","content":"package login\n"},{"path":"src/main.go","content":"package main\n\nimport _ \"minas-tirith/login\"\n"}]}`
	lf := llm.NewFake(llm.Response{Content: reply, Model: "test-model"})
	acts := NewActivities(provider.NewFake(repo), lf)

	impl, err := acts.GenerateImplementation(t.Context(), implementationInput())
	if err != nil {
		t.Fatalf("GenerateImplementation: %v", err)
	}
	if !impl.Complete {
		t.Errorf("Complete = false, want true (reason %q)", impl.Reason)
	}
	if impl.TaskID != "TASK-019" {
		t.Errorf("TaskID = %q", impl.TaskID)
	}
	if impl.PromptVersion != ImplementationPromptVersion || impl.Model != "test-model" {
		t.Errorf("prompt version/model = %q/%q", impl.PromptVersion, impl.Model)
	}
	if len(impl.Changes) != 2 || impl.Changes[0].Path != "src/login.go" {
		t.Fatalf("changes = %+v, want the two proposed files", impl.Changes)
	}
	if impl.Changes[1].Content != "package main\n\nimport _ \"minas-tirith/login\"\n" {
		t.Errorf("changes[1].Content = %q, want the complete new content", impl.Changes[1].Content)
	}

	// The LLM saw the implementation prompt and a user turn carrying the
	// task, the listing, and the file contents.
	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	if calls[0].System != ImplementationPrompt {
		t.Error("llm system prompt is not the implementation prompt")
	}
	if !calls[0].JSON {
		t.Error("llm call was not in JSON mode")
	}
	if len(calls[0].Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(calls[0].Messages))
	}
	prompt := calls[0].Messages[0].Content
	for _, want := range []string{
		"# Task TASK-019: Implement login",
		"Add login to src/main.go.",
		"src/main.go",
		"README.md",
		"package main",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("user prompt misses %q:\n%s", want, prompt)
		}
	}
}

func TestGenerateImplementationPartial(t *testing.T) {
	const reply = `{"outcome":"partial","reason":"the task does not say which auth provider to use","open_questions":["Which provider?"],"changes":[]}`
	lf := llm.NewFake(llm.Response{Content: reply})
	acts := NewActivities(provider.NewFake(repo), lf)

	impl, err := acts.GenerateImplementation(t.Context(), implementationInput())
	if err != nil {
		t.Fatalf("GenerateImplementation: %v", err)
	}
	if impl.Complete {
		t.Error("Complete = true, want a partial outcome")
	}
	if !strings.Contains(impl.Reason, "auth provider") {
		t.Errorf("Reason = %q", impl.Reason)
	}
	if len(impl.OpenQuestions) != 1 || impl.OpenQuestions[0] != "Which provider?" {
		t.Errorf("OpenQuestions = %v", impl.OpenQuestions)
	}
	if len(impl.Changes) != 0 {
		t.Errorf("Changes = %v, want none", impl.Changes)
	}
}

func TestGenerateImplementationErrors(t *testing.T) {
	tests := []struct {
		name  string
		llm   llm.Client
		input GenerateImplementationInput
		want  string
	}{
		{
			name:  "no llm client",
			llm:   nil,
			input: implementationInput(),
			want:  "no llm client",
		},
		{
			name:  "missing task id",
			llm:   llm.NewFake(),
			input: GenerateImplementationInput{Repository: repo},
			want:  "task id is required",
		},
		{
			name:  "llm failure",
			llm:   &llm.Fake{Err: errors.New("llm unavailable")},
			input: implementationInput(),
			want:  "llm unavailable",
		},
		{
			name:  "invalid json",
			llm:   llm.NewFake(llm.Response{Content: "not json"}),
			input: implementationInput(),
			want:  "not valid json",
		},
		{
			name:  "partial without reason",
			llm:   llm.NewFake(llm.Response{Content: `{"outcome":"partial"}`}),
			input: implementationInput(),
			want:  "without a reason",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewActivities(provider.NewFake(repo), tt.llm).GenerateImplementation(t.Context(), tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// --- Prompt rendering ---

func TestImplementationUserPromptTruncatesLargeFiles(t *testing.T) {
	big := strings.Repeat("x", maxContextFileBytes+10)
	in := implementationInput()
	in.Contents = []ReadFile{{Path: "big.go", Content: big}}
	prompt := implementationUserPrompt(in)
	if !strings.Contains(prompt, "file truncated") {
		t.Error("prompt does not mark the truncated file")
	}
	if len(prompt) > maxContextFileBytes+4096 {
		t.Errorf("prompt length = %d, want the file bounded", len(prompt))
	}
}

func TestImplementationUserPromptWithoutContents(t *testing.T) {
	in := implementationInput()
	in.Contents = nil
	prompt := implementationUserPrompt(in)
	if !strings.Contains(prompt, "report a partial outcome") {
		t.Errorf("prompt = %q, want the instruction for missing context", prompt)
	}
}
