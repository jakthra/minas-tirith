package worker

import (
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Runner owns the Temporal workers for all registered templates. It
// embeds Starter: template registration, task-queue resolution, trigger
// aggregation, and workflow starts come from the starter; the runner
// adds the workers that execute the started workflows.
//
// Use: NewRunner, then Register every template, then Start. The runner
// is then safe for concurrent use: workflow starts run concurrently with
// the workers. Registering after Start is rejected. Stop drains
// in-flight workflow and activity tasks of every worker.
type Runner struct {
	*Starter

	// newWorkerFn builds and starts the Temporal worker for one template.
	// It is a field so tests can exercise the runner lifecycle without a
	// Temporal server.
	newWorkerFn func(t Template) (tworker.Worker, error)

	started bool
	workers []tworker.Worker
}

// NewRunner returns a runner that will start one Temporal worker per
// registered template. Each template's task queue is the baseQueue plus
// "-" plus the template name, so agents poll independently of each other.
func NewRunner(c client.Client, baseQueue string) *Runner {
	r := &Runner{Starter: NewStarter(c, baseQueue)}
	r.newWorkerFn = r.newWorker
	return r
}

// Register adds a template to the runner. It must be called before Start.
func (r *Runner) Register(t Template) error {
	if r.started {
		return fmt.Errorf("worker: cannot register template %q after start", t.Name())
	}
	return r.Starter.Register(t)
}

// Start registers every template's workflows and activities and starts
// one Temporal worker per template. If a template's trigger references a
// workflow that no template registered, or a worker fails to start, no
// worker is left running.
func (r *Runner) Start() error {
	if r.started {
		return fmt.Errorf("worker: runner is already started")
	}
	for _, t := range r.templates {
		for _, tr := range t.Triggers() {
			if _, ok := r.queues[tr.Workflow]; !ok {
				return fmt.Errorf("worker: template %q triggers unregistered workflow %q", t.Name(), tr.Workflow)
			}
		}
	}

	started := make([]tworker.Worker, 0, len(r.templates))
	for _, t := range r.templates {
		w, err := r.newWorkerFn(t)
		if err != nil {
			for _, sw := range started {
				sw.Stop()
			}
			return err
		}
		started = append(started, w)
	}

	r.workers = started
	r.started = true
	return nil
}

// newWorker builds and starts the Temporal worker for one template.
func (r *Runner) newWorker(t Template) (tworker.Worker, error) {
	queue := r.TaskQueue(t.Name())
	w := tworker.New(r.client, queue, tworker.Options{
		// A fatal worker error (e.g. registration or polling failure)
		// stops that worker; log it loudly. The rest keeps running so
		// events keep being ingested and other agents keep executing.
		OnFatalError: func(err error) {
			slog.Error("temporal worker failed", "template", t.Name(), "task_queue", queue, "error", err)
		},
	})
	for _, wf := range t.Workflows() {
		w.RegisterWorkflowWithOptions(wf.Fn, workflow.RegisterOptions{Name: wf.Name})
	}
	for _, a := range t.Activities() {
		w.RegisterActivityWithOptions(a.Fn, activity.RegisterOptions{Name: a.Name})
	}
	if err := w.Start(); err != nil {
		return nil, fmt.Errorf("worker: start temporal worker for template %q: %w", t.Name(), err)
	}
	slog.Info("temporal worker started",
		"template", t.Name(),
		"task_queue", queue,
		"workflows", len(t.Workflows()),
		"activities", len(t.Activities()))
	return w, nil
}

// Stop drains and stops every started worker. It is safe to call when no
// worker was started.
func (r *Runner) Stop() {
	if !r.started {
		return
	}
	for _, w := range r.workers {
		if w != nil {
			w.Stop() // waits for in-flight workflow/activity tasks to drain
		}
	}
	r.workers = nil
	r.started = false
	slog.Info("temporal workers stopped")
}
