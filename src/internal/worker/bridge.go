package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/api/serviceerror"

	"minas-tirith/internal/event"
)

// WorkflowStarter starts a workflow by type name with the given input and
// workflow ID (empty = server-generated). *Starter and *Runner implement
// it; tests can fake it.
type WorkflowStarter interface {
	StartWorkflow(ctx context.Context, workflow string, input any, workflowID string) (runID string, err error)
}

// startTimeout bounds one workflow start RPC so a wedged Temporal server
// cannot pile up handler goroutines forever.
const startTimeout = 30 * time.Second

// EventBridge maps repository events to workflow starts through the
// registered triggers: for each trigger whose kind matches the event, it
// builds the workflow input and starts the trigger's workflow.
//
// The workflow ID is derived from the event identity
// (<workflow>-<event DedupeKey>), so a redelivered event maps to the same
// workflow execution instead of a duplicate one (see
// Starter.StartWorkflow's deduplication semantics).
//
// The listener subscribes the bridge to its event dispatcher: every
// verified, deduped webhook event becomes one or more workflow starts.
type EventBridge struct {
	starter  WorkflowStarter
	triggers []Trigger
}

// NewEventBridge returns a bridge that starts workflows through starter
// for every matching trigger. Register all templates on the starter
// before building the bridge: it snapshots the triggers it is given.
func NewEventBridge(starter WorkflowStarter, triggers []Trigger) *EventBridge {
	return &EventBridge{starter: starter, triggers: triggers}
}

// HandleEvent processes one event. It returns an error when any matched
// trigger's workflow start failed with something other than
// "already started"; an already-started workflow is a redelivery
// duplicate and counts as success. The listener logs the error: the
// webhook that dispatched the event has already been answered.
func (b *EventBridge) HandleEvent(ctx context.Context, ev event.Event) error {
	var errs []error
	for _, tr := range b.triggers {
		if tr.Kind != ev.Kind {
			continue
		}
		input, ok := tr.Input(ev)
		if !ok {
			slog.Info("event did not yield workflow input; skipping",
				"workflow", tr.Workflow, "event", ev.Summary())
			continue
		}
		if err := b.start(ctx, tr, ev, input); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// start starts one workflow for one matched trigger.
func (b *EventBridge) start(ctx context.Context, tr Trigger, ev event.Event, input any) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()

	workflowID := fmt.Sprintf("%s-%s", tr.Workflow, ev.DedupeKey())
	runID, err := b.starter.StartWorkflow(ctx, tr.Workflow, input, workflowID)
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	switch {
	case errors.As(err, &alreadyStarted):
		// The same event already started (or completed) this workflow.
		slog.Info("workflow already started for event; skipping duplicate",
			"workflow", tr.Workflow, "workflow_id", workflowID, "event", ev.Summary())
		return nil
	case err != nil:
		slog.Error("failed to start workflow",
			"workflow", tr.Workflow, "workflow_id", workflowID,
			"error", err, "event", ev.Summary())
		return fmt.Errorf("start workflow %q: %w", tr.Workflow, err)
	default:
		slog.Info("started workflow",
			"workflow", tr.Workflow, "workflow_id", workflowID,
			"run_id", runID, "event", ev.Summary())
		return nil
	}
}
