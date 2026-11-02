package taskimplementation

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker/backlog"
)

// taskFile returns a backlog.md task file in the format the backlog CLI
// writes, with the section markers and criterion numbering the tests
// parse.
func taskFile(id, title string) string {
	return `---
id: ` + id + `
title: ` + title + `
status: To Do
assignee: []
created_date: '2026-11-02 13:44'
labels: []
type: task
ordinal: 21000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Implement the repository tooling the agent needs to read and write code.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The agent can read the task it must implement
- [x] #2 The agent can list the repository
- [ ] #3 A missing file is reported distinctly from a provider failure
<!-- AC:END -->
`
}

// newFake returns a provider fake serving the test repository with a task
// file on main, committed as commitSHA so the task commit is a real ref.
func newFake(t *testing.T) (*provider.Fake, string) {
	t.Helper()
	pf := provider.NewFake(repo)
	pf.SeedFiles(repo, "main", map[string]string{
		taskPath:      taskFile("TASK-019", "Implement login"),
		"src/main.go": "package main\n",
		"src/app.go":  "package app\n",
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

// --- ReadTask (AC1) ---

func TestReadTask(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())

	task, err := acts.ReadTask(t.Context(), ReadTaskInput{
		Repository: repo,
		Path:       taskPath,
		Ref:        sha,
	})
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	if task.ID != "TASK-019" || task.Title != "Implement login" || task.Status != "To Do" {
		t.Errorf("frontmatter = (%q, %q, %q)", task.ID, task.Title, task.Status)
	}
	if task.Path != taskPath || task.Ref != sha {
		t.Errorf("path/ref = (%q, %q)", task.Path, task.Ref)
	}
	if !strings.Contains(task.Description, "repository tooling") {
		t.Errorf("description = %q, want the description section body", task.Description)
	}
	if strings.Contains(task.Description, "SECTION:") {
		t.Errorf("description = %q, want the section markers stripped", task.Description)
	}
	wantCriteria := []string{
		"The agent can read the task it must implement",
		"The agent can list the repository",
		"A missing file is reported distinctly from a provider failure",
	}
	if fmt.Sprint(task.AcceptanceCriteria) != fmt.Sprint(wantCriteria) {
		t.Errorf("acceptance criteria = %q, want %q", task.AcceptanceCriteria, wantCriteria)
	}
	if !strings.Contains(task.Content, "## Acceptance Criteria") {
		t.Errorf("content = %q, want the complete file", task.Content)
	}
}

func TestReadTaskReadsTheTaskCommit(t *testing.T) {
	// The task is read at the commit the workflow started on, not at the
	// branch head that moved since.
	pf, sha := newFake(t)
	if _, err := pf.Commit(t.Context(), repo, "main", "Rewrite task", []provider.FileChange{
		{Path: taskPath, Content: []byte(taskFile("TASK-019", "Rewritten later"))},
	}); err != nil {
		t.Fatalf("second commit: %v", err)
	}

	acts := NewActivities(pf, llm.NewFake())
	task, err := acts.ReadTask(t.Context(), ReadTaskInput{Repository: repo, Path: taskPath, Ref: sha})
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	if task.Title != "Implement login" {
		t.Errorf("title = %q, want the task commit's title", task.Title)
	}
}

func TestReadTaskMissing(t *testing.T) {
	pf, sha := newFake(t)
	_, err := NewActivities(pf, llm.NewFake()).ReadTask(t.Context(), ReadTaskInput{
		Repository: repo,
		Path:       "backlog/tasks/task-99 - Missing.md",
		Ref:        sha,
	})
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestReadTaskProviderError(t *testing.T) {
	pf, sha := newFake(t)
	pf.Err = errors.New("provider unavailable")
	_, err := NewActivities(pf, llm.NewFake()).ReadTask(t.Context(), ReadTaskInput{Repository: repo, Path: taskPath, Ref: sha})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("error = %v, want the provider error", err)
	}
}

func TestReadTaskRejectsNonTaskPath(t *testing.T) {
	pf, sha := newFake(t)
	for _, path := range []string{"src/main.go", "backlog/tasks/sub/task-1.md", ""} {
		if _, err := NewActivities(pf, llm.NewFake()).ReadTask(t.Context(), ReadTaskInput{Repository: repo, Path: path, Ref: sha}); err == nil {
			t.Errorf("ReadTask(%q): want error, got nil", path)
		}
	}
}

func TestReadTaskWithoutProvider(t *testing.T) {
	if _, err := NewActivities(nil, llm.NewFake()).ReadTask(t.Context(), ReadTaskInput{Repository: repo, Path: taskPath}); err == nil {
		t.Error("ReadTask without a provider: want error, got nil")
	}
}

func TestParseTask(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		wantID   string
		wantDesc string
		wantAC   int
	}{
		{
			name:     "full task file",
			content:  taskFile("TASK-1", "Title"),
			wantID:   "TASK-1",
			wantDesc: "Implement the repository tooling the agent needs to read and write code.",
			wantAC:   3,
		},
		{
			name:    "no frontmatter",
			content: "## Description\n\nNo frontmatter.\n",
			wantErr: true,
		},
		{
			name:    "frontmatter without id",
			content: "---\ntitle: No id\ntype: task\n---\n\n## Description\n\nBody.\n",
			wantErr: true,
		},
		{
			name:    "unparseable frontmatter",
			content: "---\nid: [unclosed\n---\n\n## Description\n\nBody.\n",
			wantErr: true,
		},
		{
			name:     "no sections",
			content:  "---\nid: TASK-2\ntitle: Bare\n---\n\nJust text.\n",
			wantID:   "TASK-2",
			wantDesc: "",
			wantAC:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, err := parseTask(taskPath, "main", tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTask: %v", err)
			}
			if task.ID != tt.wantID {
				t.Errorf("id = %q, want %q", task.ID, tt.wantID)
			}
			if task.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", task.Description, tt.wantDesc)
			}
			if len(task.AcceptanceCriteria) != tt.wantAC {
				t.Errorf("criteria = %q, want %d", task.AcceptanceCriteria, tt.wantAC)
			}
		})
	}
}

func TestParseCriteria(t *testing.T) {
	got := parseCriteria("- [ ] #1 First\n- [x] Second\n\nSome prose\n- [ ] #12 Twelfth\n- [ ]\n")
	want := []string{"First", "Second", "Twelfth"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("criteria = %q, want %q", got, want)
	}
}

// --- ListRepository (AC2) ---

func TestListRepository(t *testing.T) {
	pf, sha := newFake(t)
	listing, err := NewActivities(pf, llm.NewFake()).ListRepository(t.Context(), ListRepositoryInput{Repository: repo, Ref: sha})
	if err != nil {
		t.Fatalf("ListRepository: %v", err)
	}
	if listing.Total != 4 || len(listing.Files) != 4 || listing.Truncated {
		t.Fatalf("listing = %+v, want all four files", listing)
	}
	for i := 1; i < len(listing.Files); i++ {
		if listing.Files[i-1].Path >= listing.Files[i].Path {
			t.Errorf("files not sorted by path: %q before %q", listing.Files[i-1].Path, listing.Files[i].Path)
		}
	}

	// A path narrows the listing to one directory.
	listing, err = NewActivities(pf, llm.NewFake()).ListRepository(t.Context(), ListRepositoryInput{Repository: repo, Path: "src", Ref: sha})
	if err != nil {
		t.Fatalf("ListRepository(src): %v", err)
	}
	if listing.Total != 2 || listing.Files[0].Path != "src/app.go" {
		t.Errorf("listing(src) = %+v, want the two source files", listing)
	}
}

func TestListRepositoryIsBounded(t *testing.T) {
	pf := provider.NewFake(repo)
	files := make(map[string]string, maxListedFiles+5)
	for i := 0; i < maxListedFiles+5; i++ {
		files[fmt.Sprintf("src/file-%05d.go", i)] = "package src\n"
	}
	pf.SeedFiles(repo, "main", files)

	listing, err := NewActivities(pf, llm.NewFake()).ListRepository(t.Context(), ListRepositoryInput{Repository: repo, Ref: "main"})
	if err != nil {
		t.Fatalf("ListRepository: %v", err)
	}
	if len(listing.Files) != maxListedFiles {
		t.Errorf("files = %d, want the bound %d", len(listing.Files), maxListedFiles)
	}
	if listing.Total != maxListedFiles+5 || !listing.Truncated {
		t.Errorf("total/truncated = %d/%v, want %d/true", listing.Total, listing.Truncated, maxListedFiles+5)
	}
}

func TestListRepositoryErrors(t *testing.T) {
	pf, sha := newFake(t)
	if _, err := NewActivities(pf, llm.NewFake()).ListRepository(t.Context(), ListRepositoryInput{Repository: "root/missing", Ref: sha}); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("missing repository: error = %v, want ErrNotFound", err)
	}
	pf.Err = errors.New("provider unavailable")
	if _, err := NewActivities(pf, llm.NewFake()).ListRepository(t.Context(), ListRepositoryInput{Repository: repo, Ref: sha}); err == nil {
		t.Error("provider error: want failure, got nil")
	}
	if _, err := NewActivities(nil, llm.NewFake()).ListRepository(t.Context(), ListRepositoryInput{Repository: repo}); err == nil {
		t.Error("no provider: want failure, got nil")
	}
}

// --- ReadFiles (AC3) ---

func TestReadFiles(t *testing.T) {
	pf, sha := newFake(t)
	files, err := NewActivities(pf, llm.NewFake()).ReadFiles(t.Context(), ReadFilesInput{
		Repository: repo,
		Paths:      []string{"src/main.go", "src/app.go"},
		Ref:        sha,
	})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	if len(files) != 2 || files[0].Path != "src/main.go" || files[0].Content != "package main\n" {
		t.Errorf("files = %+v, want the two seeded files in order", files)
	}
	if files[0].Size != int64(len("package main\n")) {
		t.Errorf("size = %d, want %d", files[0].Size, len("package main\n"))
	}
}

func TestReadFilesMissingFileIsDistinct(t *testing.T) {
	// A missing file is skipped, not an error: the agent must be able to
	// tell "this file is new" from a provider failure.
	pf, sha := newFake(t)
	files, err := NewActivities(pf, llm.NewFake()).ReadFiles(t.Context(), ReadFilesInput{
		Repository: repo,
		Paths:      []string{"src/main.go", "src/new.go"},
		Ref:        sha,
	})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "src/main.go" {
		t.Errorf("files = %+v, want only the existing file", files)
	}
}

func TestReadFilesProviderError(t *testing.T) {
	pf, sha := newFake(t)
	pf.Err = errors.New("provider unavailable")
	_, err := NewActivities(pf, llm.NewFake()).ReadFiles(t.Context(), ReadFilesInput{Repository: repo, Paths: []string{"src/main.go"}, Ref: sha})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("error = %v, want the provider error (not treated as missing)", err)
	}
	if errors.Is(err, provider.ErrNotFound) {
		t.Error("provider failure reported as a missing file")
	}
}

func TestReadFilesRejectsPaths(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	for _, path := range []string{"", "/etc/passwd", "../outside.go", "src/../escape.go", "src/"} {
		if _, err := acts.ReadFiles(t.Context(), ReadFilesInput{Repository: repo, Paths: []string{"src/main.go", path}, Ref: sha}); err == nil {
			t.Errorf("ReadFiles(%q): want error, got nil", path)
		}
	}
	if _, err := acts.ReadFiles(t.Context(), ReadFilesInput{Repository: repo, Ref: sha}); err == nil {
		t.Error("ReadFiles with no paths: want error, got nil")
	}
	if _, err := NewActivities(nil, llm.NewFake()).ReadFiles(t.Context(), ReadFilesInput{Repository: repo, Paths: []string{"src/main.go"}}); err == nil {
		t.Error("ReadFiles without a provider: want error, got nil")
	}
}

// --- CreateImplementationBranch and CommitImplementation (AC4) ---

func TestCreateImplementationBranch(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())

	branch, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{
		Repository: repo,
		TaskID:     "TASK-019",
		TaskTitle:  "Implement login",
		FromRef:    sha,
	})
	if err != nil {
		t.Fatalf("CreateImplementationBranch: %v", err)
	}
	if want := "implement/task-019-implement-login-" + ShortSHA(sha); branch.Name != want {
		t.Errorf("branch = %q, want %q", branch.Name, want)
	}
	if branch.SHA != sha || branch.Reused {
		t.Errorf("branch = %+v, want the task commit, not reused", branch)
	}
	if !backlog.IsAgentBranch(branch.Name) {
		t.Errorf("branch %q is not an agent branch", branch.Name)
	}

	// The branch starts from the task commit: it carries its files.
	if _, err := pf.GetFile(t.Context(), repo, taskPath, branch.Name); err != nil {
		t.Errorf("GetFile on the new branch: %v", err)
	}

	// A branch that already exists is reused, so a retried activity
	// succeeds.
	again, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{
		Repository: repo, TaskID: "TASK-019", TaskTitle: "Implement login", FromRef: sha,
	})
	if err != nil {
		t.Fatalf("CreateImplementationBranch (retry): %v", err)
	}
	if again.Name != branch.Name || !again.Reused {
		t.Errorf("retry = %+v, want the existing branch reused", again)
	}
}

func TestCreateImplementationBranchErrors(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	if _, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{Repository: repo, TaskID: "TASK-1", FromRef: "nosuchref"}); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("missing ref: error = %v, want ErrNotFound", err)
	}
	if _, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{Repository: repo, TaskID: "TASK-1"}); err == nil {
		t.Error("missing from_ref: want error, got nil")
	}
	pf.Err = errors.New("provider unavailable")
	if _, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{Repository: repo, TaskID: "TASK-1", FromRef: sha}); err == nil {
		t.Error("provider error: want failure, got nil")
	}
	if _, err := NewActivities(nil, llm.NewFake()).CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{Repository: repo, TaskID: "TASK-1", FromRef: sha}); err == nil {
		t.Error("no provider: want failure, got nil")
	}
}

func TestImplementationBranchName(t *testing.T) {
	tests := []struct {
		id, title, sha, want string
	}{
		{"TASK-22", "Add repository tooling!", "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0", "implement/task-22-add-repository-tooling-8ef0de217a18"},
		{"TASK-22", "", "abc", "implement/task-22-abc"},
		{"", "Only a title", "abc", "implement/only-a-title-abc"},
		{"", "", "abc", "implement/task-abc"},
	}
	for _, tt := range tests {
		if got := ImplementationBranchName(tt.id, tt.title, tt.sha); got != tt.want {
			t.Errorf("ImplementationBranchName(%q, %q, %q) = %q, want %q", tt.id, tt.title, tt.sha, got, tt.want)
		}
	}
}

func TestCommitImplementation(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	branch, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{
		Repository: repo, TaskID: "TASK-019", TaskTitle: "Implement login", FromRef: sha,
	})
	if err != nil {
		t.Fatalf("CreateImplementationBranch: %v", err)
	}

	result, err := acts.CommitImplementation(t.Context(), CommitImplementationInput{
		Repository: repo,
		Branch:     branch.Name,
		Message:    "Implement login",
		Files: []provider.FileChange{
			{Path: "src/login.go", Content: []byte("package login\n")},
			{Path: "src/main.go", Content: []byte("package main\n\n// changed\n")},
		},
	})
	if err != nil {
		t.Fatalf("CommitImplementation: %v", err)
	}
	if result.SHA == "" || result.SHA == sha {
		t.Errorf("commit sha = %q, want a new commit", result.SHA)
	}
	if fmt.Sprint(result.Files) != "[src/login.go src/main.go]" {
		t.Errorf("files = %v", result.Files)
	}

	// The commit landed on the implementation branch, not on main.
	file, err := pf.GetFile(t.Context(), repo, "src/login.go", branch.Name)
	if err != nil {
		t.Fatalf("GetFile on the branch: %v", err)
	}
	if string(file.Content) != "package login\n" {
		t.Errorf("content = %q", file.Content)
	}
	if _, err := pf.GetFile(t.Context(), repo, "src/login.go", "main"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("main: error = %v, want the file absent from main", err)
	}
}

func TestCommitImplementationRejectsBranches(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	for _, branch := range []string{
		"main",
		"",
		backlog.RefinementBranchPrefix + "task-019-8ef0de217a18",
		"feature/login",
		"implement/",
	} {
		_, err := acts.CommitImplementation(t.Context(), CommitImplementationInput{
			Repository: repo,
			Branch:     branch,
			Files:      []provider.FileChange{{Path: "src/login.go", Content: []byte("x")}},
		})
		if err == nil {
			t.Errorf("branch %q: want error, got nil", branch)
		}
	}
	// Nothing was written to main.
	if _, err := pf.GetFile(t.Context(), repo, "src/login.go", sha); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("main: error = %v, want nothing committed", err)
	}
}

func TestCommitImplementationRejectsPaths(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	branch, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{
		Repository: repo, TaskID: "TASK-019", FromRef: sha,
	})
	if err != nil {
		t.Fatalf("CreateImplementationBranch: %v", err)
	}
	tests := []struct {
		name  string
		files []provider.FileChange
	}{
		{"absolute path", []provider.FileChange{{Path: "/etc/passwd"}}},
		{"parent segment", []provider.FileChange{{Path: "../outside.go"}}},
		{"escape inside", []provider.FileChange{{Path: "src/../../outside.go"}}},
		{"empty path", []provider.FileChange{{Path: ""}}},
		{"directory path", []provider.FileChange{{Path: "src/"}}},
		{"duplicate path", []provider.FileChange{{Path: "src/a.go"}, {Path: "src/a.go"}}},
		{"no files", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := acts.CommitImplementation(t.Context(), CommitImplementationInput{
				Repository: repo, Branch: branch.Name, Files: tt.files,
			})
			if err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestCommitImplementationErrors(t *testing.T) {
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	branch, err := acts.CreateImplementationBranch(t.Context(), CreateImplementationBranchInput{
		Repository: repo, TaskID: "TASK-019", FromRef: sha,
	})
	if err != nil {
		t.Fatalf("CreateImplementationBranch: %v", err)
	}
	in := CommitImplementationInput{
		Repository: repo, Branch: branch.Name,
		Files: []provider.FileChange{{Path: "src/login.go", Content: []byte("x")}},
	}

	// A branch the provider does not have fails with ErrNotFound.
	missing := in
	missing.Branch = "implement/task-999-nosuch"
	if _, err := acts.CommitImplementation(t.Context(), missing); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("missing branch: error = %v, want ErrNotFound", err)
	}

	pf.Err = errors.New("provider unavailable")
	if _, err := acts.CommitImplementation(t.Context(), in); err == nil {
		t.Error("provider error: want failure, got nil")
	}
	if _, err := NewActivities(nil, llm.NewFake()).CommitImplementation(t.Context(), in); err == nil {
		t.Error("no provider: want failure, got nil")
	}
}

// --- Activity execution (AC5) ---

func TestRepositoryActivitiesRunInActivityEnvironment(t *testing.T) {
	// Every tooling activity runs through the SDK's activity test
	// environment, the way a Temporal worker runs it.
	pf, sha := newFake(t)
	acts := NewActivities(pf, llm.NewFake())
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(acts.ReadTask, activity.RegisterOptions{Name: ReadTaskName})
	env.RegisterActivityWithOptions(acts.ListRepository, activity.RegisterOptions{Name: ListRepositoryName})
	env.RegisterActivityWithOptions(acts.ReadFiles, activity.RegisterOptions{Name: ReadFilesName})
	env.RegisterActivityWithOptions(acts.CreateImplementationBranch, activity.RegisterOptions{Name: CreateImplementationBranchName})
	env.RegisterActivityWithOptions(acts.CommitImplementation, activity.RegisterOptions{Name: CommitImplementationName})

	encoded, err := env.ExecuteActivity(acts.ReadTask, ReadTaskInput{Repository: repo, Path: taskPath, Ref: sha})
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	var task Task
	if err := encoded.Get(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.ID != "TASK-019" {
		t.Errorf("task id = %q", task.ID)
	}

	encoded, err = env.ExecuteActivity(acts.ListRepository, ListRepositoryInput{Repository: repo, Ref: sha})
	if err != nil {
		t.Fatalf("ListRepository: %v", err)
	}
	var listing RepositoryListing
	if err := encoded.Get(&listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if listing.Total != 4 {
		t.Errorf("total = %d, want 4", listing.Total)
	}

	encoded, err = env.ExecuteActivity(acts.ReadFiles, ReadFilesInput{Repository: repo, Paths: []string{"src/main.go"}, Ref: sha})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	var files []ReadFile
	if err := encoded.Get(&files); err != nil {
		t.Fatalf("decode files: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("files = %d, want 1", len(files))
	}

	encoded, err = env.ExecuteActivity(acts.CreateImplementationBranch, CreateImplementationBranchInput{
		Repository: repo, TaskID: "TASK-019", TaskTitle: "Implement login", FromRef: sha,
	})
	if err != nil {
		t.Fatalf("CreateImplementationBranch: %v", err)
	}
	var branch ImplementationBranch
	if err := encoded.Get(&branch); err != nil {
		t.Fatalf("decode branch: %v", err)
	}

	encoded, err = env.ExecuteActivity(acts.CommitImplementation, CommitImplementationInput{
		Repository: repo, Branch: branch.Name,
		Files: []provider.FileChange{{Path: "src/login.go", Content: []byte("package login\n")}},
	})
	if err != nil {
		t.Fatalf("CommitImplementation: %v", err)
	}
	var commit CommitImplementationResult
	if err := encoded.Get(&commit); err != nil {
		t.Fatalf("decode commit: %v", err)
	}
	if commit.SHA == "" {
		t.Error("commit sha is empty")
	}
}
