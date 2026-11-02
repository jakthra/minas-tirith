package worker

import (
	"context"
	"encoding/json"
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

// classifyCall is one recorded classification activity execution.
type classifyCall struct {
	activity string
	arg      any
}

// fakeClassifier records ExecuteActivity calls and decodes a scripted
// result or error into the caller's pointer, crossing the same
// serialization boundary the Temporal SDK puts between an activity and
// its caller.
type fakeClassifier struct {
	mu     sync.Mutex
	calls  []classifyCall
	result any
	err    error
}

func (f *fakeClassifier) ExecuteActivity(ctx context.Context, activity string, arg any, result any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, classifyCall{activity: activity, arg: arg})
	if f.err != nil {
		return f.err
	}
	data, err := json.Marshal(f.result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func (f *fakeClassifier) recorded() []classifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]classifyCall(nil), f.calls...)
}

// classifyingStarter is a starter that also classifies activities, the
// way *Starter and *Runner do.
type classifyingStarter struct {
	*fakeStarter
	classifier *fakeClassifier
}

func (c classifyingStarter) ExecuteActivity(ctx context.Context, activity string, arg any, result any) error {
	return c.classifier.ExecuteActivity(ctx, activity, arg, result)
}

// classification is the shape a routing trigger's Route function reads
// from the classification activity's result. The json tags matter: the
// fakes cross the same serialization boundary the real SDK does, so the
// result survives only what the tags preserve.
type classification struct {
	Tasks   []string `json:"tasks"`
	Stories []string `json:"stories"`
}

// activityTrigger mirrors an agent's activity trigger: it starts the
// workflow only when the classification assigned it at least one file.
func activityTrigger(workflow string, pick func(classification) []string) Trigger {
	return Trigger{
		Kind:     event.KindCommit,
		Workflow: workflow,
		Classify: "backlog.classifyCommit",
		ClassifyResult: func() any {
			return &classification{}
		},
		Route: func(ev event.Event, result any) (any, bool) {
			c, ok := result.(*classification)
			if !ok {
				return nil, false
			}
			paths := pick(*c)
			if len(paths) == 0 {
				return nil, false
			}
			return paths, true
		},
	}
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

// --- Activity triggers: content-based routing before the workflow starts ---

func TestHandleEventActivityTriggerStartsWhenRouted(t *testing.T) {
	starter := &fakeStarter{}
	classifier := &fakeClassifier{result: classification{Tasks: []string{"backlog/tasks/task-19 - Implement login.md"}}}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier},
		[]Trigger{activityTrigger("taskImplementationWorkflow", func(c classification) []string { return c.Tasks })})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	calls := classifier.recorded()
	if len(calls) != 1 {
		t.Fatalf("classification calls = %d, want 1", len(calls))
	}
	if calls[0].activity != "backlog.classifyCommit" {
		t.Errorf("activity = %q, want backlog.classifyCommit", calls[0].activity)
	}
	if ev, ok := calls[0].arg.(event.Event); !ok || ev.Commit == nil || ev.Commit.SHA != "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0" {
		t.Errorf("activity arg = %#v, want the dispatched event", calls[0].arg)
	}

	got := starter.recorded()
	if len(got) != 1 {
		t.Fatalf("started %d workflows, want 1", len(got))
	}
	if got[0].workflow != "taskImplementationWorkflow" {
		t.Errorf("workflow = %q, want taskImplementationWorkflow", got[0].workflow)
	}
	paths, ok := got[0].input.([]string)
	if !ok || len(paths) != 1 || paths[0] != "backlog/tasks/task-19 - Implement login.md" {
		t.Errorf("input = %#v, want the routed task paths", got[0].input)
	}
}

func TestHandleEventActivityTriggerSkipsWhenNotRouted(t *testing.T) {
	starter := &fakeStarter{}
	classifier := &fakeClassifier{result: classification{Stories: []string{"backlog/tasks/task-2 - Story.md"}}}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier},
		[]Trigger{activityTrigger("taskImplementationWorkflow", func(c classification) []string { return c.Tasks })})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows for a commit routed elsewhere, want 0", len(got))
	}
}

func TestHandleEventActivityTriggersShareOneClassification(t *testing.T) {
	// Two agents route on the same classification activity: it runs once
	// per event, and each agent starts (once) only for its own files.
	starter := &fakeStarter{}
	classifier := &fakeClassifier{result: classification{
		Tasks:   []string{"backlog/tasks/task-19 - Implement login.md"},
		Stories: []string{"backlog/tasks/task-2 - Story.md"},
	}}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier}, []Trigger{
		activityTrigger("userStoryReviewWorkflow", func(c classification) []string { return c.Stories }),
		activityTrigger("taskImplementationWorkflow", func(c classification) []string { return c.Tasks }),
	})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if calls := classifier.recorded(); len(calls) != 1 {
		t.Errorf("classification calls = %d, want 1 (shared by both triggers)", len(calls))
	}
	got := starter.recorded()
	if len(got) != 2 {
		t.Fatalf("started %d workflows, want 2 (one per agent)", len(got))
	}
	if got[0].workflow != "userStoryReviewWorkflow" || got[1].workflow != "taskImplementationWorkflow" {
		t.Errorf("workflows = %q, %q, want one start each", got[0].workflow, got[1].workflow)
	}
}

func TestHandleEventActivityTriggerClassificationFailure(t *testing.T) {
	starter := &fakeStarter{}
	classifier := &fakeClassifier{err: context.DeadlineExceeded}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier}, []Trigger{
		activityTrigger("userStoryReviewWorkflow", func(c classification) []string { return c.Stories }),
		activityTrigger("taskImplementationWorkflow", func(c classification) []string { return c.Tasks }),
	})

	err := bridge.HandleEvent(context.Background(), commitEvent())
	if err == nil {
		t.Fatal("HandleEvent(classification failure) = nil, want an error")
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows after a failed classification, want 0", len(got))
	}
	// The shared classification was attempted once, not once per trigger.
	if calls := classifier.recorded(); len(calls) != 1 {
		t.Errorf("classification calls = %d, want 1", len(calls))
	}
}

func TestHandleEventActivityTriggerWithoutClassifier(t *testing.T) {
	// A bridge built on a starter that cannot classify cannot evaluate
	// activity triggers: it reports the error and starts nothing.
	starter := &fakeStarter{}
	bridge := NewEventBridge(starter,
		[]Trigger{activityTrigger("taskImplementationWorkflow", func(c classification) []string { return c.Tasks })})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err == nil {
		t.Fatal("HandleEvent without a classifier = nil, want an error")
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows without a classifier, want 0", len(got))
	}
}

func TestHandleEventActivityTriggerWithoutRoute(t *testing.T) {
	starter := &fakeStarter{}
	classifier := &fakeClassifier{result: classification{Tasks: []string{"x"}}}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier},
		[]Trigger{{Kind: event.KindCommit, Workflow: "w", Classify: "backlog.classifyCommit"}})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err == nil {
		t.Fatal("HandleEvent(activity trigger without route) = nil, want an error")
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows, want 0", len(got))
	}
}

func TestHandleEventActivityTriggerWithoutResultFactory(t *testing.T) {
	// An activity trigger must declare the result type its Route reads:
	// across the Temporal boundary the result arrives as a payload, and
	// without the factory there is nothing to decode it into. The
	// trigger fails loudly instead of routing on an undecodable value.
	starter := &fakeStarter{}
	classifier := &fakeClassifier{result: classification{Tasks: []string{"x"}}}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier},
		[]Trigger{{
			Kind:     event.KindCommit,
			Workflow: "w",
			Classify: "backlog.classifyCommit",
			Route:    func(ev event.Event, result any) (any, bool) { return result, true },
		}})

	if err := bridge.HandleEvent(context.Background(), commitEvent()); err == nil {
		t.Fatal("HandleEvent(activity trigger without result factory) = nil, want an error")
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows, want 0", len(got))
	}
	// The factory check precedes the activity: nothing ran.
	if calls := classifier.recorded(); len(calls) != 0 {
		t.Errorf("classification calls = %d, want 0", len(calls))
	}
}

func TestHandleEventActivityTriggerIgnoresOtherKinds(t *testing.T) {
	starter := &fakeStarter{}
	classifier := &fakeClassifier{result: classification{Tasks: []string{"x"}}}
	bridge := NewEventBridge(classifyingStarter{fakeStarter: starter, classifier: classifier},
		[]Trigger{activityTrigger("taskImplementationWorkflow", func(c classification) []string { return c.Tasks })})

	if err := bridge.HandleEvent(context.Background(), commentEvent()); err != nil {
		t.Errorf("HandleEvent(comment event): %v", err)
	}
	if calls := classifier.recorded(); len(calls) != 0 {
		t.Errorf("classification calls = %d for a non-matching event, want 0", len(calls))
	}
	if got := starter.recorded(); len(got) != 0 {
		t.Errorf("started %d workflows, want 0", len(got))
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
