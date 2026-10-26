// Package source defines the pluggable event-source abstraction through
// which repository activity is detected and dispatched.
//
// A source is anything that can produce events: a webhook receiver (passive,
// exposes an HTTP endpoint providers post to) or an active producer such as a
// poller or queue consumer. Sources attach themselves to the dispatcher via
// Start; the listener registers whatever HTTP routes they return. New sources
// can therefore be added without any changes to event dispatch.
package source

import (
	"context"
	"net/http"

	"minas-tirith/internal/event"
)

// Dispatcher is the sink into which sources dispatch events.
// *event.Dispatcher satisfies it.
type Dispatcher interface {
	Dispatch(ctx context.Context, ev event.Event)
}

// Route is an HTTP endpoint a source needs the listener to serve.
type Route struct {
	Method  string // HTTP method, e.g. "POST"
	Pattern string // net/http ServeMux pattern, e.g. "/v1/{project}/webhooks/{provider}"
	Handler http.Handler
}

// EventSource is a pluggable origin of repository events.
type EventSource interface {
	// Name identifies the source, e.g. "webhook".
	Name() string

	// Start attaches the source to the dispatcher and returns the HTTP
	// routes it needs served. Active sources start their own producers
	// (bound to ctx for cancellation) and return no routes.
	Start(ctx context.Context, d Dispatcher) ([]Route, error)
}
