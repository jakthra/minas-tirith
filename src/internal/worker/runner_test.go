package worker

import (
	"context"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"
	tworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/event"
)

// fakeTemplate is a minimal Template for registry tests.
type fakeTemplate struct {
	name       string
	workflows  []Workflow
	activities []Activity
	triggers   []Trigger
}

func (t fakeTemplate) Name() string           { return t.name }
func (t fakeTemplate) Workflows() []Workflow  { return t.workflows }
func (t fakeTemplate) Activities() []Activity { return t.activities }
func (t fakeTemplate) Triggers() []Trigger    { return t.triggers }

// noopWorkflow/noopActivity carry valid signatures so the Temporal worker
// accepts their registration.
func noopWorkflow(workflow.Context) error { return nil }
func noopActivity(context.Context) error  { return nil }

// lazyClient returns a Temporal client that connects lazily, so runner
// tests need no running server.
func lazyClient(t *testing.T) client.Client {
	t.Helper()
	c, err := client.NewLazyClient(client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// unreachableClient returns a client pointed at a port nothing listens
// on, so worker startup fails deterministically.
func unreachableClient(t *testing.T) client.Client {
	t.Helper()
	c, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func validTemplate(name, workflowName string) fakeTemplate {
	return fakeTemplate{
		name: name,
		workflows: []Workflow{
			{Name: workflowName, Fn: noopWorkflow},
		},
		activities: []Activity{
			{Name: name + ".noop", Fn: noopActivity},
		},
	}
}

func TestRegisterResolvesTaskQueues(t *testing.T) {
	r := NewRunner(nil, "base")
	if err := r.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(validTemplate("agentB", "workflowB")); err != nil {
		t.Fatal(err)
	}

	if got, want := r.TaskQueue("agentA"), "base-agentA"; got != want {
		t.Errorf("task queue = %q, want %q", got, want)
	}
	// Workflow names resolve to the task queue of the template that
	// registered them.
	for name, want := range map[string]string{"workflowA": "base-agentA", "workflowB": "base-agentB"} {
		queue, ok := r.queues[name]
		if !ok {
			t.Fatalf("workflow %q did not resolve to a task queue", name)
		}
		if queue != want {
			t.Errorf("workflow %q resolved to %q, want %q", name, queue, want)
		}
	}
}

func TestRegisterRejectsInvalidTemplates(t *testing.T) {
	tests := []struct {
		name      string
		template  fakeTemplate
		wantError string
	}{
		{
			name:      "empty name",
			template:  fakeTemplate{workflows: []Workflow{{Name: "w", Fn: noopWorkflow}}},
			wantError: "template name is required",
		},
		{
			name:      "workflow without name",
			template:  fakeTemplate{name: "a", workflows: []Workflow{{Fn: noopWorkflow}}},
			wantError: "without name or function",
		},
		{
			name:      "workflow without function",
			template:  fakeTemplate{name: "a", workflows: []Workflow{{Name: "w"}}},
			wantError: "without name or function",
		},
		{
			name:      "activity without name",
			template:  fakeTemplate{name: "a", workflows: []Workflow{{Name: "w", Fn: noopWorkflow}}, activities: []Activity{{Fn: noopActivity}}},
			wantError: "without name or function",
		},
		{
			name:      "trigger without kind",
			template:  fakeTemplate{name: "a", workflows: []Workflow{{Name: "w", Fn: noopWorkflow}}, triggers: []Trigger{{Workflow: "w", Input: passInput}}},
			wantError: "trigger without event kind",
		},
		{
			name:      "trigger without workflow",
			template:  fakeTemplate{name: "a", workflows: []Workflow{{Name: "w", Fn: noopWorkflow}}, triggers: []Trigger{{Kind: event.KindCommit, Input: passInput}}},
			wantError: "trigger without workflow",
		},
		{
			name:      "trigger without input builder",
			template:  fakeTemplate{name: "a", workflows: []Workflow{{Name: "w", Fn: noopWorkflow}}, triggers: []Trigger{{Kind: event.KindCommit, Workflow: "w"}}},
			wantError: "trigger without input builder",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner(nil, "base")
			err := r.Register(tt.template)
			if err == nil {
				t.Fatal("Register accepted an invalid template")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantError)
			}
		})
	}
}

func TestRegisterRejectsDuplicateTemplateAndWorkflow(t *testing.T) {
	r := NewRunner(nil, "base")
	if err := r.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(validTemplate("agentA", "workflowZ")); err == nil {
		t.Error("duplicate template name was accepted")
	}
	if err := r.Register(validTemplate("agentB", "workflowA")); err == nil {
		t.Error("duplicate workflow name was accepted")
	}
}

func TestTriggersAggregateInRegistrationOrder(t *testing.T) {
	r := NewRunner(nil, "base")
	commitTrigger := Trigger{Kind: event.KindCommit, Workflow: "w1", Input: passInput}
	commentTrigger := Trigger{Kind: event.KindComment, Workflow: "w2", Input: passInput}
	if err := r.Register(fakeTemplate{name: "a", workflows: []Workflow{{Name: "w1", Fn: noopWorkflow}}, triggers: []Trigger{commitTrigger}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeTemplate{name: "b", workflows: []Workflow{{Name: "w2", Fn: noopWorkflow}}, triggers: []Trigger{commentTrigger}}); err != nil {
		t.Fatal(err)
	}

	got := r.Triggers()
	if len(got) != 2 {
		t.Fatalf("got %d triggers, want 2", len(got))
	}
	if got[0].Workflow != "w1" || got[1].Workflow != "w2" {
		t.Errorf("trigger order = [%s, %s], want [w1, w2]", got[0].Workflow, got[1].Workflow)
	}
}

func TestStartRejectsTriggerForUnregisteredWorkflow(t *testing.T) {
	// A trigger may only reference workflows that some registered template
	// provides; otherwise events would map to starts of nothing.
	r := NewRunner(lazyClient(t), "base")
	dangling := fakeTemplate{
		name:      "a",
		workflows: []Workflow{{Name: "w", Fn: noopWorkflow}},
		triggers:  []Trigger{{Kind: event.KindCommit, Workflow: "otherWorkflow", Input: passInput}},
	}
	if err := r.Register(dangling); err != nil {
		t.Fatal(err)
	}
	err := r.Start()
	if err == nil {
		t.Fatal("Start accepted a trigger for an unregistered workflow")
	}
	if !strings.Contains(err.Error(), `unregistered workflow "otherWorkflow"`) {
		t.Errorf("error = %q, want it to name the dangling workflow", err)
	}
}

func TestStartStartsWorkerPerTemplate(t *testing.T) {
	// The core startup iterates registered templates without knowing
	// anything about them: two templates, two started workers. The worker
	// factory is stubbed: the Temporal SDK's own lifecycle is exercised
	// against a real server during live verification.
	r := NewRunner(nil, "base")
	created := 0
	r.newWorkerFn = func(tpl Template) (tworker.Worker, error) {
		created++
		if queue := r.TaskQueue(tpl.Name()); queue != "base-"+tpl.Name() {
			t.Errorf("worker for template %q got task queue %q, want %q", tpl.Name(), queue, "base-"+tpl.Name())
		}
		return nil, nil
	}
	if err := r.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(validTemplate("agentB", "workflowB")); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Errorf("started %d workers, want 2", created)
	}
	if !r.started {
		t.Error("runner not marked started")
	}
	if err := r.Start(); err == nil {
		t.Error("second Start was accepted")
	}
	if err := r.Register(validTemplate("agentC", "workflowC")); err == nil {
		t.Error("Register after Start was accepted")
	}
	r.Stop()
	if r.started {
		t.Error("runner still started after Stop")
	}
	r.Stop() // idempotent
}

func TestStartFailsWhenWorkerStartFails(t *testing.T) {
	// When the Temporal worker for a template fails to start (e.g. the
	// server is unreachable), no worker is left running and the runner
	// stays usable.
	r := NewRunner(unreachableClient(t), "base")
	if err := r.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(validTemplate("agentB", "workflowB")); err != nil {
		t.Fatal(err)
	}
	r.newWorkerFn = r.newWorker // real SDK worker: start fails, no server

	err := r.Start()
	if err == nil {
		t.Fatal("Start succeeded without a reachable Temporal server")
	}
	if !strings.Contains(err.Error(), `template "agentA"`) {
		t.Errorf("error = %v, want it to name the failing template", err)
	}
	if r.started {
		t.Error("runner marked started despite failure")
	}
}

func TestStartWorkflowUnknownWorkflow(t *testing.T) {
	r := NewRunner(nil, "base")
	if err := r.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StartWorkflow(context.Background(), "nope", nil, ""); err == nil {
		t.Fatal("start of unregistered workflow was accepted")
	}
}

func TestStopWithoutStart(t *testing.T) {
	r := NewRunner(nil, "base")
	r.Stop() // must not panic
	if r.started {
		t.Error("runner reports started after Stop")
	}
}

// passInput is a trivial input builder for tests in this package.
func passInput(ev event.Event) (any, bool) { return ev, true }
