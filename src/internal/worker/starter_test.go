package worker

import (
	"context"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"

	"minas-tirith/internal/event"
)

// lazyStarterClient returns a Temporal client that connects lazily, so
// starter tests need no running server.
func lazyStarterClient(t *testing.T) client.Client {
	t.Helper()
	c, err := client.NewLazyClient(client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestStarterRegisterResolvesTaskQueues(t *testing.T) {
	s := NewStarter(nil, "base")
	if err := s.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(validTemplate("agentB", "workflowB")); err != nil {
		t.Fatal(err)
	}

	if got, want := s.TaskQueue("agentA"), "base-agentA"; got != want {
		t.Errorf("task queue = %q, want %q", got, want)
	}
	// Workflow names resolve to the task queue of the template that
	// registered them.
	for name, want := range map[string]string{"workflowA": "base-agentA", "workflowB": "base-agentB"} {
		queue, ok := s.queues[name]
		if !ok {
			t.Fatalf("workflow %q did not resolve to a task queue", name)
		}
		if queue != want {
			t.Errorf("workflow %q resolved to %q, want %q", name, queue, want)
		}
	}
}

func TestStarterRegisterRejectsInvalidTemplates(t *testing.T) {
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
			s := NewStarter(nil, "base")
			err := s.Register(tt.template)
			if err == nil {
				t.Fatal("Register accepted an invalid template")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantError)
			}
		})
	}
}

func TestStarterRegisterRejectsDuplicateTemplateAndWorkflow(t *testing.T) {
	s := NewStarter(nil, "base")
	if err := s.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(validTemplate("agentA", "workflowZ")); err == nil {
		t.Error("duplicate template name was accepted")
	}
	if err := s.Register(validTemplate("agentB", "workflowA")); err == nil {
		t.Error("duplicate workflow name was accepted")
	}
}

func TestStarterTriggersAggregateInRegistrationOrder(t *testing.T) {
	s := NewStarter(nil, "base")
	commitTrigger := Trigger{Kind: event.KindCommit, Workflow: "w1", Input: passInput}
	commentTrigger := Trigger{Kind: event.KindComment, Workflow: "w2", Input: passInput}
	if err := s.Register(fakeTemplate{name: "a", workflows: []Workflow{{Name: "w1", Fn: noopWorkflow}}, triggers: []Trigger{commitTrigger}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(fakeTemplate{name: "b", workflows: []Workflow{{Name: "w2", Fn: noopWorkflow}}, triggers: []Trigger{commentTrigger}}); err != nil {
		t.Fatal(err)
	}

	got := s.Triggers()
	if len(got) != 2 {
		t.Fatalf("got %d triggers, want 2", len(got))
	}
	if got[0].Workflow != "w1" || got[1].Workflow != "w2" {
		t.Errorf("trigger order = [%s, %s], want [w1, w2]", got[0].Workflow, got[1].Workflow)
	}
}

func TestStarterStartWorkflowUnknownWorkflow(t *testing.T) {
	s := NewStarter(nil, "base")
	if err := s.Register(validTemplate("agentA", "workflowA")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartWorkflow(context.Background(), "nope", nil, ""); err == nil {
		t.Fatal("start of unregistered workflow was accepted")
	}
}
