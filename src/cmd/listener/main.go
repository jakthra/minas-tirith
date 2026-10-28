// Command listener runs the Minas Tirith git event listener service: it
// receives webhooks from the local Forgejo instance, verifies and
// dedupes them, and starts the agent workflows they trigger on Temporal.
// The worker binary (cmd/worker) executes those workflows.
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
	"minas-tirith/internal/worker"
	"minas-tirith/internal/worker/userstoryreview"
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

	// Temporal client: the listener starts the workflows the events
	// trigger; the worker binary executes them. The dial fails fast
	// when the server is unreachable, so a misconfigured listener exits
	// before accepting a single webhook it could not hand downstream.
	tc, err := worker.DialClient(ctx, worker.ClientConfig{
		HostPort:  cfg.Temporal.HostPort,
		Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		slog.Error("failed to connect to temporal", "error", err)
		os.Exit(1)
	}
	defer tc.Close()

	// Agent templates register here, same as in the worker: the starter
	// learns the workflow-to-task-queue mapping and the event triggers.
	// A new agent type is a Template implementation plus one Register
	// call; the listener core, event subscription, and lifecycle
	// management stay untouched.
	//
	// The listener never executes activities (the worker binary does), so
	// it registers the template with nil clients: the activity functions
	// are validated for registration but never called here.
	starter := worker.NewStarter(tc, cfg.Temporal.TaskQueue)
	if err := starter.Register(userstoryreview.Template(nil, nil)); err != nil {
		slog.Error("failed to register agent template", "error", err)
		os.Exit(1)
	}

	// Event bridge: subscribe it to the dispatcher so every verified,
	// deduped event starts the workflows its triggers declare. Start
	// failures are logged: the webhook that dispatched the event has
	// already been answered.
	bridge := worker.NewEventBridge(starter, starter.Triggers())
	cancelBridge := srv.Subscribe(func(ctx context.Context, ev event.Event) {
		if err := bridge.HandleEvent(ctx, ev); err != nil {
			slog.Error("failed to start workflow for event", "error", err, "event", ev.Summary())
		}
	})
	defer cancelBridge()

	// Built-in consumer: log every dispatched event.
	cancel := srv.Subscribe(logEvent)
	defer cancel()

	slog.Info("event listener running",
		"listen", cfg.ListenAddr,
		"temporal", cfg.Temporal.HostPort,
		"namespace", cfg.Temporal.Namespace,
		"task_queue", cfg.Temporal.TaskQueue)

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
