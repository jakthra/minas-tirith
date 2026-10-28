// Package worker provides the Temporal execution layer for the Minas
// Tirith autonomous agents: a template registry that agent types plug
// into, a starter that starts workflows by type name (used by the
// listener), a runner that starts one Temporal worker per registered
// template (used by the worker binary), and an event bridge that maps
// dispatched repository events to workflow starts.
//
// An agent type is a Template: it contributes its workflow and activity
// implementations plus the repository events that should start them. The
// starter, runner, and event bridge are agent-agnostic; adding a new
// agent type means implementing Template and registering it, without
// touching worker startup or event wiring.
package worker

import (
	"minas-tirith/internal/event"
)

// Workflow describes one workflow type a template contributes.
type Workflow struct {
	// Name is the Temporal workflow type name, e.g.
	// "userStoryReviewWorkflow". It must be unique across all registered
	// templates: it identifies the workflow when starting executions and
	// resolves the task queue they run on.
	Name string
	// Fn is the workflow function, e.g. UserStoryReviewWorkflow.
	Fn any
}

// Activity describes one activity type a template contributes. Activities
// run on the same task queue as their template's workflows.
type Activity struct {
	// Name is the Temporal activity type name, e.g.
	// "userStoryReview.analyzeCommit".
	Name string
	// Fn is the activity function, e.g. AnalyzeCommit.
	Fn any
}

// Trigger maps one kind of repository event to starts of one workflow.
// Input builds the workflow's input from a dispatched event; it reports
// false for events that should not start the workflow.
type Trigger struct {
	// Kind is the event kind this trigger reacts to, e.g.
	// event.KindCommit.
	Kind event.Kind
	// Workflow is the workflow type name (registered by this or another
	// template) that matching events start.
	Workflow string
	// Input converts a dispatched event into the workflow input. Must not
	// be nil.
	Input func(ev event.Event) (any, bool)
}

// Template is one agent type: the workflows and activities that implement
// it, and the repository events that should start it.
type Template interface {
	// Name identifies the template and its task queue (the base task
	// queue plus "-" plus Name).
	Name() string
	// Workflows lists the workflow types the template registers.
	Workflows() []Workflow
	// Activities lists the activity types the template registers.
	Activities() []Activity
	// Triggers lists the event kinds that start the template's workflows.
	Triggers() []Trigger
}
