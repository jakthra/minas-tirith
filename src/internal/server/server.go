// Package server wires event sources, the dispatcher, and the HTTP server
// together and owns the listener's lifecycle.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"minas-tirith/internal/config"
	"minas-tirith/internal/event"
	"minas-tirith/internal/source"
	"minas-tirith/internal/source/webhook"
)

// Server is the event listener service: an HTTP server exposing the routes
// its event sources need, dispatching detected events to subscribers.
type Server struct {
	cfg        *config.Config
	dispatcher *event.Dispatcher
	mux        *http.ServeMux
	httpServer *http.Server
}

// New builds the server with the webhook event source registered under the
// forgejo and gitea providers. ctx bounds sources that start background
// producers (none today); it is not the server's run context.
func New(ctx context.Context, cfg *config.Config) (*Server, error) {
	dispatcher := event.NewDispatcher()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	s := &Server{
		cfg:        cfg,
		dispatcher: dispatcher,
		mux:        mux,
		httpServer: &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}

	// The webhook event source, with its provider adapters. New providers
	// are one RegisterProvider call away; new sources are one AddSource
	// call away, without touching dispatch.
	webhookSource := webhook.NewSource(webhook.Config{
		Secret:    cfg.Secret,
		Watched:   cfg.WatchedRepos,
		DedupeTTL: cfg.DedupeTTL,
	})
	webhookSource.RegisterProvider(webhook.NewForgejoAdapter("forgejo"))
	webhookSource.RegisterProvider(webhook.NewForgejoAdapter("gitea"))
	if err := s.AddSource(ctx, webhookSource); err != nil {
		return nil, err
	}
	return s, nil
}

// AddSource attaches an event source to the dispatcher and registers the
// HTTP routes the source needs served.
func (s *Server) AddSource(ctx context.Context, src source.EventSource) error {
	routes, err := src.Start(ctx, s.dispatcher)
	if err != nil {
		return fmt.Errorf("start %s source: %w", src.Name(), err)
	}
	for _, r := range routes {
		s.mux.Handle(r.Method+" "+r.Pattern, r.Handler)
		slog.Info("registered source route", "source", src.Name(), "method", r.Method, "pattern", r.Pattern)
	}
	return nil
}

// Subscribe registers a consumer for all dispatched events and returns a
// cancel function. Consumers filter by event kind, project, or repository as
// they see fit; what they do with events is their concern, not the
// listener's.
func (s *Server) Subscribe(h event.Handler) (cancel func()) {
	return s.dispatcher.Subscribe(h)
}

// Handler returns the server's HTTP handler (for tests).
func (s *Server) Handler() http.Handler { return s.mux }

// Run serves until ctx is canceled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("event listener listening", "addr", s.cfg.ListenAddr, "watched", s.cfg.WatchedRepos)
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		slog.Info("event listener stopped")
		return nil
	}
}
