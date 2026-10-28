package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.temporal.io/api/serviceerror"

	"minas-tirith/internal/event"
)

// start is one recorded workflow start.
type start struct {
	workflow   string
	input      any
	workflowID string
}

// fakeStarter records StartWorkflow calls.
type fakeStarter struct {
	mu     sync.Mutex
	starts []start
	err    error // returned by every call when set
}

func (f *fakeStarter) StartWorkflow(ctx context.Context, workflow string, input any, workflowID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, start{workflow: workflow, input: input, workflowID: workflowID})
	if f.err != nil {
		return "", f.err
	}
	return "run-1", nil
}

func (f *fakeStarter) recorded() []start {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]start(nil), f.starts...)
}

func commitEvent() event.Event {
	return event.Event{
		Kind:       event.KindCommit,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: "root/BeanieGenie",
		Commit: &event.Commit{
			SHA:     "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
			Branch:  "main",
			Message: "Refine user story",
		},
	}
}

func commentEvent() event.Event {
	return event.Event{
		Kind:       event.KindComment,
		Provider:   "forgejo",
		Repository: "root/BeanieGenie",
		Comment:    &event.Comment{ID: 1, Body: "looks good"},
	}
}

// reviewTrigger mirrors the user story review trigger: commit events start
// the review workflow with the commit payload.
func reviewTrigger() Trigger {
	return Trigger{
		Kind:     event.KindCommit,
		Workflow: "userStoryReviewWorkflow",
		Input: func(ev event.Event) (any, bool) {
			if ev.Commit == nil || ev.Commit.SHA == "" {
				return nil, false
			}
			return ev.Commit.SHA, true
		},
	}
}

func TestHandleEventStartsWorkflowForCommitEvent(t *testing.T) {
	starter := &fakeStarter{}
	bridge := NewEventBridge(starter, []Trigger{reviewTrigger()})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	got := starter.recorded()
	if len(got) != 1 {
		t.Fatalf("started %d workflows, want 1", len(got))
	}
	if got[0].workflow != "userStoryReviewWorkflow" {
		t.Errorf("workflow = %q, want userStoryReviewWorkflow", got[0].workflow)
	}
	if got[0].input != "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0" {
		t.Errorf("input = %v, want the commit payload", got[0].input)
	}
	// The workflow ID is derived from the event identity, so the same
	// event maps to the same workflow.
	wantID := "userStoryReviewWorkflow-forgejo|root/BeanieGenie|commit|8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0"
	if got[0].workflowID != wantID {
		t.Errorf("workflow ID = %q, want %q", got[0].workflowID, wantID)
	}
}

func TestHandleEventIgnoresNonMatchingEvents(t *testing.T) {
	starter := &fakeStarter{}
	bridge := NewEventBridge(starter, []Trigger{reviewTrigger()})

	if err := bridge.HandleEvent(context.Background(), commentEvent()); err != nil {
		t.Errorf("HandleEvent(comment event): %v", err)
	}
	if err := bridge.HandleEvent(context.Background(), event.Event{Kind: "other"}); err != nil {
		t.Errorf("HandleEvent(other kind): %v", err)
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows for non-matching events, want 0", len(got))
	}
}

func TestHandleEventSkipsWhenInputDeclines(t *testing.T) {
	starter := &fakeStarter{}
	bridge := NewEventBridge(starter, []Trigger{reviewTrigger()})

	// A commit event without a SHA yields no input: no start, no error.
	ev := commitEvent()
	ev.Commit.SHA = ""
	if err := bridge.HandleEvent(context.Background(), ev); err != nil {
		t.Errorf("HandleEvent without input: %v", err)
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows without input, want 0", len(got))
	}
}

func TestHandleEventStartsAllMatchingTriggers(t *testing.T) {
	starter := &fakeStarter{}
	triggers := []Trigger{reviewTrigger(), {
		Kind:     event.KindCommit,
		Workflow: "anotherWorkflow",
		Input:    func(ev event.Event) (any, bool) { return "x", true },
	}}
	bridge := NewEventBridge(starter, triggers)

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if got := starter.recorded(); len(got) != 2 {
		t.Errorf("started %d workflows, want 2 (one per matching trigger)", len(got))
	}
}

func TestHandleEventDuplicateStartIsNotAnError(t *testing.T) {
	// A starter that reports the workflow as already started (duplicate
	// delivery) is a skip, not a failure: the same event dispatched twice
	// must not surface as an error.
	starter := &fakeStarter{err: &serviceerror.WorkflowExecutionAlreadyStarted{Message: "already started"}}
	bridge := NewEventBridge(starter, []Trigger{reviewTrigger()})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err != nil {
		t.Errorf("HandleEvent(duplicate start) = %v, want nil", err)
	}
	if got := starter.recorded(); len(got) != 1 {
		t.Fatalf("starter called %d times, want 1", len(got))
	}
}

func TestHandleEventStartFailureDoesNotStopOtherTriggers(t *testing.T) {
	starter := &fakeStarter{err: context.DeadlineExceeded}
	bridge := NewEventBridge(starter, []Trigger{reviewTrigger(), {
		Kind:     event.KindCommit,
		Workflow: "anotherWorkflow",
		Input:    func(ev event.Event) (any, bool) { return "x", true },
	}})

	// The failed start surfaces as an error (logged by the listener's
	// subscription wrapper), and the other triggers were still attempted.
	err := bridge.HandleEvent(context.Background(), commitEvent())
	if err == nil {
		t.Error("HandleEvent(start failure) = nil, want an error")
	}
	if got := starter.recorded(); len(got) != 2 {
		t.Errorf("starter called %d times, want 2 (failure must not break the loop)", len(got))
	}
}

func TestBridgeSubscribesToDispatcher(t *testing.T) {
	// The bridge as a plain event handler (as an in-process subscriber
	// would run it): a dispatched commit event starts the workflow.
	starter := &fakeStarter{}
	d := event.NewDispatcher()
	bridge := NewEventBridge(starter, []Trigger{reviewTrigger()})
	cancel := d.Subscribe(func(ctx context.Context, ev event.Event) {
		if err := bridge.HandleEvent(ctx, ev); err != nil {
			t.Errorf("HandleEvent: %v", err)
		}
	})
	defer cancel()

	d.Dispatch(context.Background(), commitEvent())

	waitFor(t, time.Second, func() bool { return len(starter.recorded()) == 1 })
	got := starter.recorded()[0]
	if got.workflow != "userStoryReviewWorkflow" {
		t.Errorf("workflow = %q, want userStoryReviewWorkflow", got.workflow)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
