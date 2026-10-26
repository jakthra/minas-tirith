package event

import (
	"context"
	"log/slog"
	"sync"
)

// Handler receives dispatched events. Handlers run each in their own
// goroutine with a context that is not canceled when the originating request
// ends, so slow consumers never block the webhook delivery path.
type Handler func(ctx context.Context, ev Event)

// Dispatcher fans events out to subscribed handlers.
type Dispatcher struct {
	mu   sync.RWMutex
	subs map[*subscription]struct{}
}

type subscription struct {
	h Handler
}

// NewDispatcher returns an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{subs: make(map[*subscription]struct{})}
}

// Subscribe registers a handler for all dispatched events and returns a
// cancel function that unsubscribes it. Canceling is idempotent.
func (d *Dispatcher) Subscribe(h Handler) (cancel func()) {
	if h == nil {
		return func() {}
	}
	sub := &subscription{h: h}
	d.mu.Lock()
	d.subs[sub] = struct{}{}
	d.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			delete(d.subs, sub)
			d.mu.Unlock()
		})
	}
}

// Dispatch delivers ev to all subscribed handlers. It never blocks: each
// handler runs in its own goroutine, and handlers receive a context that is
// detached from the caller's cancellation.
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event) {
	d.mu.RLock()
	subs := make([]*subscription, 0, len(d.subs))
	for sub := range d.subs {
		subs = append(subs, sub)
	}
	d.mu.RUnlock()
	if len(subs) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	for _, sub := range subs {
		go d.invoke(ctx, sub.h, ev)
	}
}

func (d *Dispatcher) invoke(ctx context.Context, h Handler, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("event handler panicked", "panic", r, "event", ev.Summary())
		}
	}()
	h(ctx, ev)
}
