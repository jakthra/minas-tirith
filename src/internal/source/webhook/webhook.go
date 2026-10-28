// Package webhook implements the webhook event source: an authenticated
// HTTP endpoint that receives provider webhook deliveries, parses them via
// provider adapters, drops duplicate deliveries, and dispatches the resulting
// provider-agnostic events.
//
// The source itself is provider-agnostic. Provider-specific details (header
// names, signature scheme, payload format) live in Provider adapters
// registered under a name; the adapter named in the route's {provider}
// segment handles the delivery. Which repositories are watched, and the
// secret each one authenticates with, come from the declarative config store.
// New providers or stores register without changes to routing, dispatch, or
// dedupe.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"minas-tirith/internal/configstore"
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

	// Verify authenticates the delivery using the addressed repository's
	// webhook secret. It must not consume or modify the body.
	Verify(header http.Header, body []byte, secret string) error

	// Parse converts an authenticated delivery into zero or more events.
	// project is the watched project name from the route. Deliveries that
	// are irrelevant (other event types, non-new comments, plain issues)
	// yield no events rather than an error; only malformed deliveries fail.
	Parse(header http.Header, body []byte, project string) ([]event.Event, error)
}

// Config configures the webhook source.
type Config struct {
	// Store loads the watched repositories and their webhook secrets.
	// Deliveries are verified with the secret of the project they are
	// addressed to, so each watched repository can have its own secret.
	Store configstore.Store
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
	repos     map[string]string // watched project -> its webhook secret
	providers map[string]Provider
}

// NewSource creates a webhook source for the given configuration.
// Providers must be registered via RegisterProvider before Start; the
// watched repositories are loaded from the config store at Start.
func NewSource(cfg Config) *Source {
	if cfg.DedupeTTL <= 0 {
		cfg.DedupeTTL = 24 * time.Hour
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = defaultMaxBodySize
	}
	return &Source{
		cfg:       cfg,
		dedupe:    dedupe.NewStore(cfg.DedupeTTL),
		providers: make(map[string]Provider),
	}
}

// RegisterProvider registers a provider adapter under its name.
func (s *Source) RegisterProvider(p Provider) {
	s.providers[p.Name()] = p
}

// Name implements source.EventSource.
func (s *Source) Name() string { return "webhook" }

// Start implements source.EventSource: webhooks are passive, so Start loads
// the watched repositories and their secrets from the config store,
// validates configuration, and returns the route the listener must serve.
func (s *Source) Start(ctx context.Context, d source.Dispatcher) ([]source.Route, error) {
	if s.cfg.Store == nil {
		return nil, errors.New("webhook source: config store is not configured")
	}
	repos, err := s.cfg.Store.Repositories()
	if err != nil {
		return nil, fmt.Errorf("webhook source: load watched repositories: %w", err)
	}

	// The store's declarations are trusted but checked: a broken store
	// fails startup loudly instead of deliveries quietly. Every problem
	// is reported in one error.
	var errs []error
	watched := make(map[string]string, len(repos))
	projects := make([]string, 0, len(repos))
	for _, r := range repos {
		switch {
		case r.Project == "":
			errs = append(errs, errors.New("webhook source: watched repository without a project name"))
			continue
		case r.Secret == "":
			errs = append(errs, fmt.Errorf("webhook source: watched repository %q has no webhook secret", r.Project))
			continue
		}
		if _, dup := watched[r.Project]; dup {
			errs = append(errs, fmt.Errorf("webhook source: watched repository %q is declared more than once", r.Project))
			continue
		}
		watched[r.Project] = r.Secret
		projects = append(projects, r.Project)
	}
	if len(watched) == 0 {
		errs = append(errs, errors.New("webhook source: no watched repositories configured"))
	}
	if len(s.providers) == 0 {
		errs = append(errs, errors.New("webhook source: no providers registered"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	s.repos = watched
	slog.Info("webhook source watching repositories", "projects", projects)

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
	secret, ok := h.src.repos[project]
	if !ok {
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
	// Each delivery is verified with the addressed project's own secret.
	if err := provider.Verify(r.Header, body, secret); err != nil {
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
