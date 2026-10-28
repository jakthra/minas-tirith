// Command worker runs the Minas Tirith agent execution service: it
// runs Temporal workers that execute the autonomous agent workflows the
// event listener (cmd/listener) starts when repository events arrive.
//
// The worker is a pure Temporal worker: it opens no inbound port and
// receives no webhooks. The listener receives webhooks, verifies and
// dedupes them, and starts the triggered workflows on Temporal; this
// binary polls the task queues and executes them.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"minas-tirith/internal/config"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
	"minas-tirith/internal/worker/userstoryreview"
)

func main() {
	cfg, err := config.LoadWorker(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(2)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	if err := run(cfg); err != nil {
		slog.Error("agent worker failed", "error", err)
		os.Exit(1)
	}
	slog.Info("agent worker stopped")
}

// run wires and runs the agent worker until the process is signaled. The
// deferred shutdown drains the Temporal workers' in-flight workflow and
// activity tasks, then closes the Temporal client.
func run(cfg *config.WorkerConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Temporal client: fail fast when the server is unreachable.
	tc, err := worker.DialClient(ctx, worker.ClientConfig{
		HostPort:  cfg.Temporal.HostPort,
		Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		return err
	}
	defer tc.Close()

	// Provider client: the agent activities use it for repository access
	// (fetch story files, and later create branches, commit, open MRs).
	providerClient, err := provider.NewForgejoClient(cfg.Provider.URL, cfg.Provider.Token)
	if err != nil {
		return fmt.Errorf("provider client: %w", err)
	}

	// LLM client: the agent activities use it for reasoning (generate
	// implementation tickets, and later interpret review comments).
	llmClient, err := llm.NewChatCompletionsClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	if err != nil {
		return fmt.Errorf("llm client: %w", err)
	}

	// Agent templates register here. A new agent type is a Template
	// implementation plus one Register call; the worker core and
	// lifecycle management stay untouched.
	runner := worker.NewRunner(tc, cfg.Temporal.TaskQueue)
	if err := runner.Register(userstoryreview.Template(providerClient, llmClient)); err != nil {
		return err
	}
	if err := runner.Start(); err != nil {
		return err
	}
	defer runner.Stop()

	slog.Info("agent worker running",
		"temporal", cfg.Temporal.HostPort,
		"namespace", cfg.Temporal.Namespace,
		"task_queue", cfg.Temporal.TaskQueue)

	// Wait until SIGTERM/SIGINT, then shut down gracefully.
	<-ctx.Done()
	return nil
}
