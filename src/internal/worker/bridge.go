package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"minas-tirith/internal/event"
)

// WorkflowStarter starts a workflow by type name with the given input and
// workflow ID (empty = server-generated). *Starter and *Runner implement
// it; tests can fake it.
type WorkflowStarter interface {
	StartWorkflow(ctx context.Context, workflow string, input any, workflowID string) (runID string, err error)
}

// ActivityClassifier runs a trigger's classification activity and returns
// its result. *Starter and *Runner implement it; tests can fake it. The
// result is decoded into the pointer the trigger's ClassifyResult
// factory created — the same way the Temporal SDK decodes an activity
// result into a typed value — so fake classifiers must cross a
// serialization boundary too (e.g. a JSON round-trip), not just return
// the activity's in-process value.
type ActivityClassifier interface {
	ExecuteActivity(ctx context.Context, activity string, arg any, result any) error
}

// startTimeout bounds one workflow start RPC so a wedged Temporal server
// cannot pile up handler goroutines forever.
const startTimeout = 30 * time.Second

// classifyTimeout bounds one classification activity execution: it reads
// the event's changed files from the provider, so it gets the same budget
// as a workflow start.
const classifyTimeout = 30 * time.Second

// EventBridge maps repository events to workflow starts through the
// registered triggers: for each trigger whose kind matches the event, it
// builds the workflow input and starts the trigger's workflow.
//
// A trigger with an Input function decides from the event alone. An
// activity trigger (Trigger.Classify) first runs the trigger's
// classification activity and lets the trigger's Route function decide
// from the result, so several agents can share one content-based routing
// decision — each starts only for the files it handles.
//
// The workflow ID is derived from the event identity
// (<workflow>-<event DedupeKey>), so a redelivered event maps to the same
// workflow execution instead of a duplicate one (see
// Starter.StartWorkflow's deduplication semantics).
//
// The listener subscribes the bridge to its event dispatcher: every
// verified, deduped webhook event becomes one or more workflow starts.
type EventBridge struct {
	starter    WorkflowStarter
	classifier ActivityClassifier
	triggers   []Trigger
}

// NewEventBridge returns a bridge that starts workflows through starter
// for every matching trigger. Register all templates on the starter
// before building the bridge: it snapshots the triggers it is given.
//
// Activity triggers need a classifier: when starter implements
// ActivityClassifier (the *Starter and *Runner do), the bridge uses it.
// A bridge without one cannot evaluate activity triggers and skips them
// with an error.
func NewEventBridge(starter WorkflowStarter, triggers []Trigger) *EventBridge {
	b := &EventBridge{starter: starter, triggers: triggers}
	if c, ok := starter.(ActivityClassifier); ok {
		b.classifier = c
	}
	return b
}

// HandleEvent processes one event. It returns an error when any matched
// trigger's workflow start failed with something other than
// "already started"; an already-started workflow is a redelivery
// duplicate and counts as success. A failed classification fails the
// activity triggers that need it. The listener logs the error: the
// webhook that dispatched the event has already been answered.
func (b *EventBridge) HandleEvent(ctx context.Context, ev event.Event) error {
	var errs []error
	classifications := make(map[string]classificationResult)
	for _, tr := range b.triggers {
		if tr.Kind != ev.Kind {
			continue
		}
		var input any
		var ok bool
		if tr.IsActivityTrigger() {
			input, ok = b.route(ctx, tr, ev, classifications, &errs)
		} else {
			input, ok = tr.Input(ev)
		}
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

// classificationResult is one classification activity's outcome; err is
// non-nil when the activity failed. result is the decoded value the
// trigger's ClassifyResult factory created.
type classificationResult struct {
	result any
	err    error
}

// route evaluates an activity trigger: it runs the trigger's
// classification activity (once per event, shared by every trigger that
// names it) and lets the trigger's Route decide from the result.
func (b *EventBridge) route(ctx context.Context, tr Trigger, ev event.Event, cache map[string]classificationResult, errs *[]error) (any, bool) {
	if tr.Route == nil {
		*errs = append(*errs, fmt.Errorf("trigger for workflow %q: activity trigger without route", tr.Workflow))
		return nil, false
	}
	if tr.ClassifyResult == nil {
		*errs = append(*errs, fmt.Errorf("trigger for workflow %q: activity trigger without result factory", tr.Workflow))
		return nil, false
	}
	cr, done := cache[tr.Classify]
	if !done {
		if b.classifier == nil {
			cr = classificationResult{err: errors.New("no activity classifier configured")}
		} else {
			cr = b.classify(ctx, tr.Classify, tr.ClassifyResult(), ev)
		}
		cache[tr.Classify] = cr
	}
	if cr.err != nil {
		err := fmt.Errorf("classify event for workflow %q: %w", tr.Workflow, cr.err)
		slog.Error("failed to classify event", "workflow", tr.Workflow, "activity", tr.Classify,
			"error", cr.err, "event", ev.Summary())
		*errs = append(*errs, err)
		return nil, false
	}
	return tr.Route(ev, cr.result)
}

// classify runs one classification activity for an event and decodes its
// result into result, the pointer the trigger's ClassifyResult factory
// created.
func (b *EventBridge) classify(ctx context.Context, activity string, result any, ev event.Event) classificationResult {
	ctx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()
	err := b.classifier.ExecuteActivity(ctx, activity, ev, result)
	return classificationResult{result: result, err: err}
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

// ExecuteActivity runs one activity to completion and decodes its result
// into result, a pointer to the activity's result type. It implements
// ActivityClassifier: the event bridge uses it to evaluate activity
// triggers before starting workflows. The activity is a standalone
// execution (not tied to a workflow), so it can run before any workflow
// starts.
//
// The activity runs on the task queue of the template that registered it
// (the same queue a worker polls for it) — not on the base queue, which
// no worker polls. An activity no registered template provides is a
// wiring bug and fails loudly instead of timing out on a dead queue.
func (s *Starter) ExecuteActivity(ctx context.Context, activity string, arg any, result any) error {
	opts, err := s.activityOptions(activity)
	if err != nil {
		return err
	}
	if s.client == nil {
		return fmt.Errorf("worker: activity: no temporal client configured")
	}
	handle, err := s.client.ExecuteActivity(ctx, opts, activity, arg)
	if err != nil {
		return err
	}
	return handle.Get(ctx, result)
}

// activityOptions builds the start options for one activity: its ID, the
// schedule-to-close budget, and the task queue of the template that
// registered the activity. It reports an error for an activity no
// registered template provides.
func (s *Starter) activityOptions(activity string) (client.StartActivityOptions, error) {
	queue, ok := s.activityQueues[activity]
	if !ok {
		return client.StartActivityOptions{}, fmt.Errorf("worker: activity: unregistered activity %q", activity)
	}
	return client.StartActivityOptions{
		ID:                     fmt.Sprintf("classify-%d", time.Now().UnixNano()),
		TaskQueue:              queue,
		ScheduleToCloseTimeout: classifyTimeout,
	}, nil
}
