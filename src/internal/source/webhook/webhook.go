// Package webhook implements the webhook event source: an authenticated
// HTTP endpoint that receives provider webhook deliveries, parses them via
// provider adapters, drops duplicate deliveries, and dispatches the resulting
// provider-agnostic events.
//
// The source itself is provider-agnostic. Provider-specific details (header
// names, signature scheme, payload format) live in Provider adapters
// registered under a name; the adapter named in the route's {provider}
// segment handles the delivery. New providers register without changes to
// routing, authentication dispatch, or dedupe.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"minas-tirith/internal/dedupe"
	"minas-tirith/internal/event"
	"minas-tirith/internal/source"
)

// RoutePattern is the webhook route served for every registered provider.
// {project} selects the watched project, {provider} the adapter.
const RoutePattern = "/v1/{project}/webhooks/{provider}"

// defaultMaxBodySize is the cap on webhook request bodies (Forgejo push
// payloads grow with the number of pushed commits).
const defaultMaxBodySize = 10 << 20 // 10 MiB

// Provider adapts one provider's webhook format to the provider-agnostic
// event model.
type Provider interface {
	// Name is the {provider} path segment the adapter is served under.
	Name() string

	// Verify authenticates the delivery using the shared secret. It must
	// not consume or modify the body.
	Verify(header http.Header, body []byte, secret string) error

	// Parse converts an authenticated delivery into zero or more events.
	// project is the watched project name from the route. Deliveries that
	// are irrelevant (other event types, non-new comments, plain issues)
	// yield no events rather than an error; only malformed deliveries fail.
	Parse(header http.Header, body []byte, project string) ([]event.Event, error)
}

// Config configures the webhook source.
type Config struct {
	// Secret is the shared secret providers use to sign deliveries.
	Secret string
	// Watched lists the project names deliveries are accepted for.
	Watched []string
	// DedupeTTL is how long event identities are remembered so duplicate
	// deliveries are dropped. Defaults to 24h when zero.
	DedupeTTL time.Duration
	// MaxBodySize caps the request body size. Defaults to 10 MiB.
	MaxBodySize int64
}

// Source is the webhook event source.
type Source struct {
	cfg       Config
	dedupe    *dedupe.Store
	watched   map[string]struct{}
	providers map[string]Provider
}

// NewSource creates a webhook source for the given configuration. Providers
// must be registered via RegisterProvider before Start.
func NewSource(cfg Config) *Source {
	if cfg.DedupeTTL <= 0 {
		cfg.DedupeTTL = 24 * time.Hour
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = defaultMaxBodySize
	}
	watched := make(map[string]struct{}, len(cfg.Watched))
	for _, p := range cfg.Watched {
		watched[p] = struct{}{}
	}
	return &Source{
		cfg:       cfg,
		dedupe:    dedupe.NewStore(cfg.DedupeTTL),
		watched:   watched,
		providers: make(map[string]Provider),
	}
}

// RegisterProvider registers a provider adapter under its name.
func (s *Source) RegisterProvider(p Provider) {
	s.providers[p.Name()] = p
}

// Name implements source.EventSource.
func (s *Source) Name() string { return "webhook" }

// Start implements source.EventSource: webhooks are passive, so Start only
// validates configuration and returns the route the listener must serve.
func (s *Source) Start(ctx context.Context, d source.Dispatcher) ([]source.Route, error) {
	if s.cfg.Secret == "" {
		return nil, errors.New("webhook source: shared secret is not configured")
	}
	if len(s.watched) == 0 {
		return nil, errors.New("webhook source: no watched repositories configured")
	}
	if len(s.providers) == 0 {
		return nil, errors.New("webhook source: no providers registered")
	}
	return []source.Route{{
		Method:  http.MethodPost,
		Pattern: RoutePattern,
		Handler: &deliveryHandler{src: s, dispatch: d},
	}}, nil
}

// deliveryHandler handles one inbound webhook delivery.
type deliveryHandler struct {
	src      *Source
	dispatch source.Dispatcher
}

func (h *deliveryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	providerName := r.PathValue("provider")
	logger := slog.With("project", project, "provider", providerName)

	// Only deliveries for watched projects and registered providers are
	// accepted. Not found (rather than forbidden) avoids revealing which
	// projects exist.
	if _, ok := h.src.watched[project]; !ok {
		http.NotFound(w, r)
		return
	}
	provider, ok := h.src.providers[providerName]
	if !ok {
		http.NotFound(w, r)
		return
	}

	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		logger.Warn("rejecting delivery: unsupported content type", "content_type", ct)
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.src.cfg.MaxBodySize))
	if err != nil {
		status := http.StatusBadRequest
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
		}
		logger.Warn("rejecting delivery: cannot read body", "error", err)
		http.Error(w, "cannot read request body", status)
		return
	}

	// Unauthenticated deliveries are rejected before parsing anything.
	if err := provider.Verify(r.Header, body, h.src.cfg.Secret); err != nil {
		logger.Warn("rejecting unauthenticated delivery", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"authentication failed"}`))
		return
	}

	events, err := provider.Parse(r.Header, body, project)
	if err != nil {
		logger.Error("rejecting malformed delivery", "error", err)
		http.Error(w, `{"error":"malformed webhook payload"}`, http.StatusBadRequest)
		return
	}

	// Drop duplicate deliveries before dispatching so redeliveries never
	// reach consumers twice.
	dispatched, duplicates := 0, 0
	for _, ev := range events {
		if h.src.dedupe.Seen(ev.DedupeKey()) {
			duplicates++
			continue
		}
		h.dispatch.Dispatch(r.Context(), ev)
		dispatched++
	}
	logger.Info("webhook delivered",
		"events", len(events), "dispatched", dispatched, "duplicates", duplicates)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DeliveryResponse{
		Status:     "ok",
		Dispatched: dispatched,
		Duplicates: duplicates,
	})
}

// DeliveryResponse is the JSON body returned for accepted deliveries.
type DeliveryResponse struct {
	Status     string `json:"status"`
	Dispatched int    `json:"dispatched"`
	Duplicates int    `json:"duplicates"`
}
