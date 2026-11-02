package backlog

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"minas-tirith/internal/event"
	"minas-tirith/internal/provider"
)

const (
	repo      = "root/BeanieGenie"
	commitSHA = "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0"

	taskPath  = "backlog/tasks/task-19 - Implement login.md"
	storyPath = "backlog/tasks/task-2 - Autonomous refinement agent.md"
)

// taskFile returns a backlog.md file whose frontmatter declares a task.
func taskFile(id string) string {
	return "---\nid: " + id + "\ntitle: Implement login\nstatus: To Do\ntype: task\n---\n\n## Description\n\nImplement login.\n"
}

// storyFile returns a backlog.md file whose frontmatter declares a user
// story.
func storyFile(id string) string {
	return "---\nid: " + id + "\ntitle: Autonomous refinement agent\nstatus: Backlog\ntype: feature\n---\n\n## Description\n\nRefine stories.\n"
}

// seedCommit stores files on main and commits them, returning the commit
// SHA the way a push event reports it.
func seedCommit(t *testing.T, f *provider.Fake, files map[string]string) string {
	t.Helper()
	changes := make([]provider.FileChange, 0, len(files))
	for path, content := range files {
		changes = append(changes, provider.FileChange{Path: path, Content: []byte(content)})
	}
	commit, err := f.Commit(context.Background(), repo, "main", "Add backlog files", changes)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return commit.SHA
}

// classifyEvent builds the commit event the classification activity
// receives: the shape the event bridge passes across the Temporal
// boundary.
func classifyEvent(sha string, added []string) event.Event {
	return event.Event{
		Kind:       event.KindCommit,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: repo,
		Commit: &event.Commit{
			SHA:    sha,
			Branch: "main",
			Added:  added,
		},
	}
}

func TestIsFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"backlog/tasks/task-1.md", true},
		{"backlog/tasks/task-42 - Implement feature.md", true},
		{"backlog/tasks/", false},            // directory, not a file
		{"backlog/tasks/sub/file.md", false}, // nested, not direct child
		{"backlog/tasks/README.txt", false},  // not markdown
		{"backlog/stories/task-1.md", false}, // wrong directory
		{"src/main.go", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsFile(tt.path); got != tt.want {
				t.Errorf("IsFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsAgentBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"refine/task-2-autonomous-refinement-agent-8ef0de217a18", true},
		{"implement/task-19-8ef0de217a18", true},
		{"refine/", true},
		{"implement/", true},
		{"main", false},
		{"feature/login", false},
		{"refinement/x", false}, // prefix must be a whole path segment
		{"implements/x", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := IsAgentBranch(tt.branch); got != tt.want {
				t.Errorf("IsAgentBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    Kind
	}{
		{"type task", taskFile("TASK-019"), KindTask},
		{"type feature is a user story", storyFile("TASK-002"), KindStory},
		{"type bug is not a task", "---\nid: TASK-017\ntype: bug\n---\n", KindStory},
		{"type is case-insensitive", "---\nid: X\ntype: Task\n---\n", KindTask},
		{"no type, TASK id is not enough", "---\nid: TASK-019\ntitle: x\n---\n", KindStory},
		{"no frontmatter", "# Just a note\n", KindStory},
		{"unparseable frontmatter", "---\nid: [unclosed\n---\n", KindStory},
		{"empty content", "", KindStory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.content); got != tt.want {
				t.Errorf("Classify(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// TestClassifySampleTask is the regression test for the sample
// implementation task: scripts/commit-sample-task.sh writes a task file
// with "type: task", and that file must be classified as a task so the
// implementation agent — not the refinement agent — acts on it. The
// fixture is the exact file the script writes.
func TestClassifySampleTask(t *testing.T) {
	content, err := os.ReadFile("testdata/sample-task.md")
	if err != nil {
		t.Fatalf("read sample task fixture: %v", err)
	}
	if got := Classify(string(content)); got != KindTask {
		t.Errorf("Classify(sample task) = %v, want %v", got, KindTask)
	}
}

// TestClassifyCommit is the routing decision table: which agent each kind
// of commit is routed to.
func TestClassifyCommit(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		added     []string
		modified  []string
		wantTasks []string
		wantStory []string
	}{
		{
			name:      "task-only commit routes to the implementation agent",
			files:     map[string]string{taskPath: taskFile("TASK-019")},
			added:     []string{taskPath},
			wantTasks: []string{taskPath},
		},
		{
			name:      "story-only commit routes to the review agent",
			files:     map[string]string{storyPath: storyFile("TASK-002")},
			added:     []string{storyPath},
			wantStory: []string{storyPath},
		},
		{
			name:      "modified story routes to the review agent",
			files:     map[string]string{storyPath: storyFile("TASK-002")},
			modified:  []string{storyPath},
			wantStory: []string{storyPath},
		},
		{
			name:      "modified task routes to the implementation agent",
			files:     map[string]string{taskPath: taskFile("TASK-019")},
			modified:  []string{taskPath},
			wantTasks: []string{taskPath},
		},
		{
			name: "mixed commit routes each file to its own agent",
			files: map[string]string{
				taskPath:  taskFile("TASK-019"),
				storyPath: storyFile("TASK-002"),
			},
			added:     []string{storyPath, taskPath},
			wantTasks: []string{taskPath},
			wantStory: []string{storyPath},
		},
		{
			name:  "non-backlog commit routes nowhere",
			files: map[string]string{"src/main.go": "package main"},
			added: []string{"src/main.go", "README.md"},
		},
		{
			name:  "nested backlog file routes nowhere",
			files: map[string]string{"backlog/tasks/sub/x.md": storyFile("TASK-003")},
			added: []string{"backlog/tasks/sub/x.md"},
		},
		{
			name:  "no files routes nowhere",
			files: map[string]string{"README.md": "hi"},
		},
		{
			name:      "duplicate paths are classified once",
			files:     map[string]string{taskPath: taskFile("TASK-019")},
			added:     []string{taskPath},
			modified:  []string{taskPath},
			wantTasks: []string{taskPath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := provider.NewFake(repo)
			sha := seedCommit(t, f, tt.files)
			got, err := classifyCommit(context.Background(), f, Commit{
				Repository: repo,
				SHA:        sha,
				Added:      tt.added,
				Modified:   tt.modified,
			})
			if err != nil {
				t.Fatalf("classifyCommit: %v", err)
			}
			if strings.Join(got.TaskPaths, ",") != strings.Join(tt.wantTasks, ",") {
				t.Errorf("task paths = %v, want %v", got.TaskPaths, tt.wantTasks)
			}
			if strings.Join(got.StoryPaths, ",") != strings.Join(tt.wantStory, ",") {
				t.Errorf("story paths = %v, want %v", got.StoryPaths, tt.wantStory)
			}
			if got.Reason == "" {
				t.Error("reason is empty")
			}
		})
	}
}

func TestClassifyCommitErrors(t *testing.T) {
	f := provider.NewFake(repo)
	if _, err := classifyCommit(context.Background(), f, Commit{
		Repository: repo, SHA: commitSHA, Added: []string{taskPath},
	}); err == nil {
		t.Error("classifyCommit with a missing file: want error, got nil")
	}

	if _, err := classifyCommit(context.Background(), f, Commit{Added: []string{taskPath}}); err == nil {
		t.Error("classifyCommit without a sha: want error, got nil")
	}

	f.Err = errors.New("provider unavailable")
	if _, err := classifyCommit(context.Background(), f, Commit{
		Repository: repo, SHA: commitSHA, Added: []string{taskPath},
	}); err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Errorf("classifyCommit with a provider error = %v, want the provider error", err)
	}
}

func TestClassifyCommitWithoutProvider(t *testing.T) {
	a := NewActivities(nil)
	if _, err := a.ClassifyCommit(context.Background(), classifyEvent(commitSHA, []string{taskPath})); err == nil || !strings.Contains(err.Error(), "no provider client") {
		t.Fatalf("ClassifyCommit without provider = %v, want no-provider error", err)
	}
}

// TestClassifyCommitRejectsEventsWithoutCommitPayload checks the activity
// validates the event shape it receives across the Temporal boundary.
func TestClassifyCommitRejectsEventsWithoutCommitPayload(t *testing.T) {
	a := NewActivities(provider.NewFake(repo))
	empty := event.Event{Kind: event.KindCommit, Repository: repo}
	if _, err := a.ClassifyCommit(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "repository and a sha") {
		t.Fatalf("ClassifyCommit without commit payload = %v, want the repository-and-sha error", err)
	}
	comment := event.Event{Kind: event.KindComment, Repository: repo}
	if _, err := a.ClassifyCommit(context.Background(), comment); err == nil || !strings.Contains(err.Error(), "repository and a sha") {
		t.Fatalf("ClassifyCommit with a comment event = %v, want the repository-and-sha error", err)
	}
}

func TestClassifyCommitActivityEnv(t *testing.T) {
	// The activity runs through the SDK's activity test environment, the
	// way the worker executes it for the event bridge — including the
	// serialization boundary the bridge's activity trigger crosses.
	f := provider.NewFake(repo)
	sha := seedCommit(t, f, map[string]string{taskPath: taskFile("TASK-019")})

	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	acts := NewActivities(f)
	env.RegisterActivityWithOptions(acts.ClassifyCommit, activity.RegisterOptions{Name: ClassifyCommitActivity})

	encoded, err := env.ExecuteActivity(acts.ClassifyCommit, classifyEvent(sha, []string{taskPath}))
	if err != nil {
		t.Fatalf("ClassifyCommit failed: %v", err)
	}
	var got Classification
	if err := encoded.Get(&got); err != nil {
		t.Fatalf("decoding activity result: %v", err)
	}
	if len(got.TaskPaths) != 1 || got.TaskPaths[0] != taskPath {
		t.Errorf("task paths = %v, want [%s]", got.TaskPaths, taskPath)
	}
	if len(got.StoryPaths) != 0 {
		t.Errorf("story paths = %v, want none", got.StoryPaths)
	}
}
