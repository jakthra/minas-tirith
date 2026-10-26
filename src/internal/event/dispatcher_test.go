package event

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchReachesSubscribers(t *testing.T) {
	d := NewDispatcher()

	var got1, got2 atomic.Int64
	cancel1 := d.Subscribe(func(ctx context.Context, ev Event) { got1.Add(1) })
	d.Subscribe(func(ctx context.Context, ev Event) { got2.Add(1) })

	ev := Event{Kind: KindCommit, Commit: &Commit{SHA: "abc"}}
	d.Dispatch(context.Background(), ev)

	waitFor(t, time.Second, func() bool { return got1.Load() == 1 && got2.Load() == 1 })
	cancel1()

	d.Dispatch(context.Background(), ev)
	waitFor(t, time.Second, func() bool { return got2.Load() == 2 })
	if got1.Load() != 1 {
		t.Errorf("canceled subscriber still received events: %d", got1.Load())
	}
}

func TestDispatchWithoutSubscribers(t *testing.T) {
	d := NewDispatcher()
	done := make(chan struct{})
	go func() {
		d.Dispatch(context.Background(), Event{Kind: KindCommit})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch without subscribers blocked")
	}
}

func TestDispatchDetachedContext(t *testing.T) {
	// Handlers must outlive the webhook request they were dispatched from.
	d := NewDispatcher()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-canceled request context

	received := make(chan error, 1)
	d.Subscribe(func(ctx context.Context, ev Event) {
		select {
		case received <- ctx.Err():
		default:
		}
	})
	d.Dispatch(ctx, Event{Kind: KindComment, Comment: &Comment{ID: 1}})

	select {
	case err := <-received:
		if err != nil {
			t.Errorf("handler context canceled: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler never invoked")
	}
}

func TestPanickingHandlerDoesNotAffectOthers(t *testing.T) {
	d := NewDispatcher()
	d.Subscribe(func(ctx context.Context, ev Event) { panic("boom") })

	var got atomic.Int64
	d.Subscribe(func(ctx context.Context, ev Event) { got.Add(1) })

	d.Dispatch(context.Background(), Event{Kind: KindCommit})
	waitFor(t, time.Second, func() bool { return got.Load() == 1 })
}

func TestSubscribeNilHandler(t *testing.T) {
	d := NewDispatcher()
	cancel := d.Subscribe(nil)
	cancel() // must not panic
}

func TestEventDedupeKey(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{
			name: "commit",
			ev: Event{Provider: "forgejo", Project: "BeanieGenie", Repository: "root/BeanieGenie",
				Kind: KindCommit, Commit: &Commit{SHA: "deadbeef"}},
			want: "forgejo|root/BeanieGenie|commit|deadbeef",
		},
		{
			name: "comment",
			ev: Event{Provider: "forgejo", Project: "BeanieGenie", Repository: "root/BeanieGenie",
				Kind: KindComment, Comment: &Comment{ID: 42}},
			want: "forgejo|root/BeanieGenie|comment|42",
		},
		{
			name: "commit falls back to project when repository empty",
			ev:   Event{Provider: "forgejo", Project: "BeanieGenie", Kind: KindCommit, Commit: &Commit{SHA: "x"}},
			want: "forgejo|BeanieGenie|commit|x",
		},
		{
			name: "kind without payload stays distinguishable",
			ev:   Event{Provider: "forgejo", Project: "BeanieGenie", Kind: KindCommit},
			want: "forgejo|BeanieGenie|commit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ev.DedupeKey(); got != tt.want {
				t.Errorf("dedupe key = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEventSummary(t *testing.T) {
	ev := Event{Provider: "forgejo", Project: "BeanieGenie", Repository: "root/BeanieGenie",
		Kind: KindCommit, Commit: &Commit{SHA: "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0",
			Branch: "main", Message: "Add feature\n\nLong body"}}
	if got, want := ev.Summary(), "commit 8ef0de217a18 pushed to root/BeanieGenie (branch main): Add feature"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}

	ev = Event{Provider: "forgejo", Repository: "root/BeanieGenie", Kind: KindComment,
		Comment: &Comment{Author: Person{Username: "root"}, MR: &MergeRequest{Number: 1, Title: "T"}, Body: "hello"}}
	if got, want := ev.Summary(), "comment by root on root/BeanieGenie (!1 T): hello"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	// Consumers must be able to serialize events without provider types.
	// (Empty Person structs serialize as {} — omitempty does not apply to
	// structs, and that is fine: consumers treat it as "unknown".)
	ev := Event{Kind: KindComment, Provider: "forgejo", Project: "p", Repository: "root/p",
		Comment: &Comment{ID: 7, Body: "b", MR: &MergeRequest{Number: 3}}}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"kind":"comment","provider":"forgejo","project":"p","repository":"root/p","comment":{"id":7,"body":"b","author":{},"mr":{"number":3,"author":{}}}}` {
		t.Errorf("unexpected JSON: %s", b)
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
