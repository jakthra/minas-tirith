package worker

import (
	"context"
	"fmt"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// Starter starts workflows on the task queues that registered templates
// declare, without running any Temporal workers. It is the listener's
// Temporal client: it connects to the server, learns the
// workflow-to-task-queue mapping from registered templates, and starts
// workflows by type name.
//
// Use: NewStarter, then Register every template (so the starter learns the
// queues and triggers), then start workflows. The runner (Runner) embeds
// Starter and adds the Temporal workers that execute the started
// workflows.
type Starter struct {
	client    client.Client
	baseQueue string

	templates []Template
	// queues maps workflow type name → task queue, filled by Register.
	queues map[string]string
}

// NewStarter returns a starter that starts workflows on task queues
// derived from baseQueue: each template's queue is baseQueue plus "-"
// plus the template name.
func NewStarter(c client.Client, baseQueue string) *Starter {
	return &Starter{
		client:    c,
		baseQueue: baseQueue,
		queues:    make(map[string]string),
	}
}

// Register adds a template to the starter: its workflows resolve to task
// queues, and its triggers become available through Triggers. Register
// validates the template the same way the runner does.
func (s *Starter) Register(t Template) error {
	if t.Name() == "" {
		return fmt.Errorf("worker: template name is required")
	}
	for _, existing := range s.templates {
		if existing.Name() == t.Name() {
			return fmt.Errorf("worker: template %q is already registered", t.Name())
		}
	}
	for _, wf := range t.Workflows() {
		if wf.Name == "" || wf.Fn == nil {
			return fmt.Errorf("worker: template %q has a workflow without name or function", t.Name())
		}
		if _, taken := s.queues[wf.Name]; taken {
			return fmt.Errorf("worker: workflow %q of template %q is already registered", wf.Name, t.Name())
		}
	}
	for _, a := range t.Activities() {
		if a.Name == "" || a.Fn == nil {
			return fmt.Errorf("worker: template %q has an activity without name or function", t.Name())
		}
	}
	for _, tr := range t.Triggers() {
		if tr.Kind == "" {
			return fmt.Errorf("worker: template %q has a trigger without event kind", t.Name())
		}
		if tr.Workflow == "" {
			return fmt.Errorf("worker: template %q has a trigger without workflow", t.Name())
		}
		if tr.Input == nil {
			return fmt.Errorf("worker: template %q has a trigger without input builder", t.Name())
		}
	}
	s.templates = append(s.templates, t)
	for _, wf := range t.Workflows() {
		s.queues[wf.Name] = s.TaskQueue(t.Name())
	}
	return nil
}

// TaskQueue returns the task queue the given template's workflows run on.
func (s *Starter) TaskQueue(template string) string {
	return s.baseQueue + "-" + template
}

// Triggers returns the event triggers of all registered templates, in
// registration order.
func (s *Starter) Triggers() []Trigger {
	var out []Trigger
	for _, t := range s.templates {
		out = append(out, t.Triggers()...)
	}
	return out
}

// StartWorkflow starts the named workflow with input and returns its run
// ID. An empty workflowID lets the server generate one.
//
// Starts are deduplicated by workflow ID: re-using the ID of a completed
// workflow is rejected unless that workflow failed, in which case it
// re-runs. A workflow with that ID still running is rejected too. This
// makes event-triggered starts idempotent: the same repository event
// dispatched twice starts the workflow once.
func (s *Starter) StartWorkflow(ctx context.Context, workflow string, input any, workflowID string) (string, error) {
	queue, ok := s.queues[workflow]
	if !ok {
		return "", fmt.Errorf("worker: start: unregistered workflow %q", workflow)
	}
	run, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                queue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, workflow, input)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}
