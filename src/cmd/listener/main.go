// Command listener runs the Minas Tirith git event listener service: it
// receives webhooks from the local Forgejo instance and dispatches new
// commits and new merge request comments as provider-agnostic events to
// subscribed consumers.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"minas-tirith/internal/config"
	"minas-tirith/internal/event"
	"minas-tirith/internal/server"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "listener:", err)
		os.Exit(2)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(ctx, cfg)
	if err != nil {
		slog.Error("failed to start event listener", "error", err)
		os.Exit(1)
	}

	// Built-in consumer: log every dispatched event. Downstream agents
	// (refinement, implementation) subscribe the same way.
	cancel := srv.Subscribe(logEvent)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		slog.Error("event listener failed", "error", err)
		os.Exit(1)
	}
}

// logEvent is the built-in logging consumer.
func logEvent(ctx context.Context, ev event.Event) {
	raw, _ := json.Marshal(ev)
	slog.Info("event dispatched", "summary", ev.Summary(), "event", string(raw))
}
