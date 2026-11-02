package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
	"minas-tirith/internal/worker/backlog"
	"minas-tirith/internal/worker/taskimplementation"
	"minas-tirith/internal/worker/userstoryreview"
)

const (
	repo = "root/BeanieGenie"

	taskPath  = "backlog/tasks/task-19 - Implement login.md"
	storyPath = "backlog/tasks/task-2 - Autonomous refinement agent.md"
)

func taskFile(id string) string {
	return "---\nid: " + id + "\ntitle: Implement login\nstatus: To Do\ntype: task\n---\n\n## Description\n\nImplement login.\n"
}

func storyFile(id string) string {
	return "---\nid: " + id + "\ntitle: Autonomous refinement agent\nstatus: Backlog\ntype: feature\n---\n\n## Description\n\nRefine stories.\n"
}

// recordingStarter records workflow starts and runs the classification
// activity against the provider fake, the way the real starter does
// against Temporal — including the serialization boundary an activity
// crosses: the activity's result is decoded into the caller's typed
// pointer, never passed through in-process.
type recordingStarter struct {
	provider  provider.Client
	bridge    *worker.EventBridge
	workflows []string
	inputs    []any
}

func (r *recordingStarter) StartWorkflow(_ context.Context, workflow string, input any, _ string) (string, error) {
	r.workflows = append(r.workflows, workflow)
	r.inputs = append(r.inputs, input)
	return "run-1", nil
}

func (r *recordingStarter) ExecuteActivity(ctx context.Context, activity string, arg any, result any) error {
	switch activity {
	case backlog.ClassifyCommitActivity:
		res, err := backlog.NewActivities(r.provider).ClassifyCommit(ctx, arg.(event.Event))
		if err != nil {
			return err
		}
		return decodeActivityResult(res, result)
	case userstoryreview.RefineCommitName:
		res, err := userstoryreview.NewActivities(r.provider, llm.NewFake()).RefineCommit(ctx, arg.(userstoryreview.Input))
		if err != nil {
			return err
		}
		return decodeActivityResult(res, result)
	case taskimplementation.CheckCommitName:
		res, err := taskimplementation.NewActivities(r.provider, llm.NewFake()).CheckCommit(ctx, arg.(taskimplementation.Input))
		if err != nil {
			return err
		}
		return decodeActivityResult(res, result)
	default:
		return fmt.Errorf("unexpected activity %q", activity)
	}
}

// decodeActivityResult emulates what the Temporal SDK does at the
// activity boundary: the activity's return value is encoded as a payload
// and the caller decodes that payload into its own typed pointer. A fake
// that passed the value straight through would hide the arg and result
// shape bugs that only appear at runtime.
func decodeActivityResult(res any, into any) error {
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

// newBridge registers both backlog agents on a starter, exactly as the
// listener does, and builds the event bridge from their triggers.
func newBridge(t *testing.T, p provider.Client) *recordingStarter {
	t.Helper()
	starter := &recordingStarter{provider: p}
	validate := worker.NewStarter(nil, "minas-tirith")
	for _, tmpl := range []worker.Template{
		userstoryreview.Template(p, llm.NewFake()),
		taskimplementation.Template(p, llm.NewFake()),
	} {
		if err := validate.Register(tmpl); err != nil {
			t.Fatalf("Register(%s): %v", tmpl.Name(), err)
		}
	}
	starter.bridge = worker.NewEventBridge(starter, validate.Triggers())
	return starter
}

func commitEvent(sha string, added, modified []string) event.Event {
	return event.Event{
		Kind:       event.KindCommit,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: repo,
		Commit: &event.Commit{
			SHA:      sha,
			Branch:   "main",
			Message:  "Add backlog files",
			Author:   event.Person{Username: "root"},
			Added:    added,
			Modified: modified,
		},
	}
}

func seed(t *testing.T, f *provider.Fake, files map[string]string) string {
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

func TestBacklogCommitsRouteToOneAgent(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		added    []string
		modified []string
		want     []string
	}{
		{
			name:  "task-only commit starts the implementation agent",
			files: map[string]string{taskPath: taskFile("TASK-019")},
			added: []string{taskPath},
			want:  []string{taskimplementation.WorkflowName},
		},
		{
			name:  "story-only commit starts the review agent",
			files: map[string]string{storyPath: storyFile("TASK-002")},
			added: []string{storyPath},
			want:  []string{userstoryreview.WorkflowName},
		},
		{
			name:     "modified story starts the review agent",
			files:    map[string]string{storyPath: storyFile("TASK-002")},
			modified: []string{storyPath},
			want:     []string{userstoryreview.WorkflowName},
		},
		{
			name:  "mixed commit starts each agent once",
			files: map[string]string{taskPath: taskFile("TASK-019"), storyPath: storyFile("TASK-002")},
			added: []string{storyPath, taskPath},
			want:  []string{userstoryreview.WorkflowName, taskimplementation.WorkflowName},
		},
		{
			name:  "non-backlog commit starts neither agent",
			files: map[string]string{"src/main.go": "package main"},
			added: []string{"src/main.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := provider.NewFake(repo)
			sha := seed(t, f, tt.files)
			starter := newBridge(t, f)

			if err := starter.bridge.HandleEvent(context.Background(), commitEvent(sha, tt.added, tt.modified)); err != nil {
				t.Fatalf("HandleEvent: %v", err)
			}
			if strings.Join(starter.workflows, ",") != strings.Join(tt.want, ",") {
				t.Errorf("started %v, want %v", starter.workflows, tt.want)
			}
		})
	}
}

// TestSampleTaskCommitStartsTheImplementationAgent is the regression test
// for the sample implementation task: the commit `just commit-sample-task`
// pushes carries a "type: task" file, and it must start the implementation
// agent, not the refinement agent. The fixture is the exact file
// scripts/commit-sample-task.sh writes.
func TestSampleTaskCommitStartsTheImplementationAgent(t *testing.T) {
	content, err := os.ReadFile("backlog/testdata/sample-task.md")
	if err != nil {
		t.Fatalf("read sample task fixture: %v", err)
	}
	const samplePath = "backlog/tasks/task-014 - Add-a-plant-to-the-garden.md"

	f := provider.NewFake(repo)
	sha := seed(t, f, map[string]string{samplePath: string(content)})
	starter := newBridge(t, f)

	if err := starter.bridge.HandleEvent(context.Background(), commitEvent(sha, []string{samplePath}, nil)); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if strings.Join(starter.workflows, ",") != taskimplementation.WorkflowName {
		t.Fatalf("started %v, want only %s", starter.workflows, taskimplementation.WorkflowName)
	}
	in := starter.inputs[0].(taskimplementation.Input)
	if len(in.TaskPaths) != 1 || in.TaskPaths[0] != samplePath {
		t.Errorf("implementation input task paths = %v, want [%s]", in.TaskPaths, samplePath)
	}
}

func TestBacklogCommitsOnAgentBranchesStartNeitherAgent(t *testing.T) {
	f := provider.NewFake(repo)
	sha := seed(t, f, map[string]string{taskPath: taskFile("TASK-019"), storyPath: storyFile("TASK-002")})
	starter := newBridge(t, f)

	for _, branch := range []string{
		userstoryreview.RefinementBranchPrefix + "task-002-autonomous-refinement-agent-8ef0de217a18",
		taskimplementation.ImplementationBranchPrefix + "task-019-8ef0de217a18",
	} {
		starter.workflows = nil
		ev := commitEvent(sha, []string{taskPath, storyPath}, nil)
		ev.Commit.Branch = branch
		if err := starter.bridge.HandleEvent(context.Background(), ev); err != nil {
			t.Fatalf("HandleEvent on %s: %v", branch, err)
		}
		if len(starter.workflows) != 0 {
			t.Errorf("branch %s started %v, want neither agent", branch, starter.workflows)
		}
	}
}

// TestRoutedInputsCarryOnlyOwnFiles checks each agent receives only the
// files the shared classification assigned to it.
func TestRoutedInputsCarryOnlyOwnFiles(t *testing.T) {
	f := provider.NewFake(repo)
	sha := seed(t, f, map[string]string{taskPath: taskFile("TASK-019"), storyPath: storyFile("TASK-002")})
	starter := newBridge(t, f)

	if err := starter.bridge.HandleEvent(context.Background(), commitEvent(sha, []string{taskPath, storyPath}, nil)); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if len(starter.inputs) != 2 {
		t.Fatalf("started %d workflows, want 2", len(starter.inputs))
	}
	for i, workflow := range starter.workflows {
		switch workflow {
		case userstoryreview.WorkflowName:
			in := starter.inputs[i].(userstoryreview.Input)
			if len(in.StoryPaths) != 1 || in.StoryPaths[0] != storyPath {
				t.Errorf("review input story paths = %v, want [%s]", in.StoryPaths, storyPath)
			}
		case taskimplementation.WorkflowName:
			in := starter.inputs[i].(taskimplementation.Input)
			if len(in.TaskPaths) != 1 || in.TaskPaths[0] != taskPath {
				t.Errorf("implementation input task paths = %v, want [%s]", in.TaskPaths, taskPath)
			}
		}
	}
}
