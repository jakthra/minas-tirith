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
//
// A trigger either derives the workflow input from the event alone
// (Trigger.Input) or, when the decision needs repository facts such as
// file content, classifies the event with a shared activity first
// (Trigger.Classify and Trigger.Route). The latter is how several agents
// share one routing decision: each registers an activity trigger over the
// same classification and starts only for its own kind of file.
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
	// Classify, when set, makes this an activity trigger: before the
	// workflow starts, the event bridge runs the named activity (registered
	// by any template) and lets Route decide from its result whether the
	// workflow starts and with which input. It is how a trigger decides on
	// facts only the repository holds — file content, for instance — which
	// the pure Input function cannot read. Exactly one of Input and
	// Classify is set.
	Classify string
	// ClassifyResult returns a new pointer to an empty value of the
	// classification activity's result type. The classifier decodes the
	// activity's result into it, so Route receives the concrete type —
	// across a Temporal boundary the raw result would otherwise decode
	// into a generic map. Required for activity triggers; every trigger
	// that names the same activity must declare the same result type,
	// because one activity run (and one decoded result) is shared by all
	// of them.
	ClassifyResult func() any
	// Route converts the event and the classification activity's result
	// into the workflow input, reporting false when the workflow must not
	// start. Required for activity triggers. The classification is the
	// value ClassifyResult created, decoded.
	Route func(ev event.Event, classification any) (any, bool)
}

// IsActivityTrigger reports whether the trigger classifies the event with
// an activity before starting its workflow.
func (tr Trigger) IsActivityTrigger() bool { return tr.Classify != "" }

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
