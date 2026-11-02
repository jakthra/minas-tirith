package taskimplementation

import (
	"context"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
	"minas-tirith/internal/worker/backlog"
)

// template implements worker.Template for the task implementation agent.
// It holds the routing check activity; the shared backlog classification
// is registered by the user story review template.
type template struct {
	activities *Activities
}

// Name identifies the template; its task queue is the configured base
// queue plus "-taskimplementation".
func (template) Name() string { return "taskimplementation" }

// Workflows lists the workflow types the template registers.
func (template) Workflows() []worker.Workflow {
	return []worker.Workflow{
		{Name: WorkflowName, Fn: TaskImplementationWorkflow},
	}
}

// Activities lists the activity types the template registers: the routing
// check that declines commits the agent must not act on, the repository
// tooling the implementation workflow reads the task, inspects the
// repository, and commits its changes through, and the LLM step that
// implements the task.
func (t template) Activities() []worker.Activity {
	return []worker.Activity{
		{Name: CheckCommitName, Fn: t.activities.CheckCommit},
		{Name: ReadTaskName, Fn: t.activities.ReadTask},
		{Name: ListRepositoryName, Fn: t.activities.ListRepository},
		{Name: ReadFilesName, Fn: t.activities.ReadFiles},
		{Name: GenerateImplementationName, Fn: t.activities.GenerateImplementation},
		{Name: CreateImplementationBranchName, Fn: t.activities.CreateImplementationBranch},
		{Name: CommitImplementationName, Fn: t.activities.CommitImplementation},
		{Name: SubmitMergeRequestName, Fn: t.activities.SubmitMergeRequest},
	}
}

// Triggers lists the repository events that start the workflows: commit
// events that add or modify a backlog task start a task implementation.
// The shared backlog classification decides that before the workflow
// starts, so a commit that only adds user stories starts the review agent
// instead.
func (t template) Triggers() []worker.Trigger {
	return []worker.Trigger{
		{
			Kind:     event.KindCommit,
			Workflow: WorkflowName,
			Classify: backlog.ClassifyCommitActivity,
			ClassifyResult: func() any {
				return &backlog.Classification{}
			},
			Route: t.routeFromClassification,
		},
	}
}

// Template returns the task implementation agent's worker template, wired
// to the given provider and LLM clients. The activities use them for
// repository access and for implementing the task.
func Template(p provider.Client, l llm.Client) worker.Template {
	return template{activities: NewActivities(p, l)}
}

// routeFromClassification builds the workflow input from a dispatched
// commit event and the shared backlog classification. It reports false
// for events that cannot start an implementation: non-commit events,
// missing commit payloads, commits without a SHA, commits on any agent
// branch (refine/* or implement/*, re-triggering on agent work would
// loop), and commits that add or modify no backlog task file. The
// classification arrives as the decoded *backlog.Classification the
// trigger's ClassifyResult factory declared.
func (t template) routeFromClassification(ev event.Event, classification any) (any, bool) {
	c, ok := classification.(*backlog.Classification)
	if !ok || len(c.TaskPaths) == 0 {
		return nil, false
	}
	in, ok := InputFromEvent(ev)
	if !ok {
		return nil, false
	}
	input := in.(Input)
	input.TaskPaths = c.TaskPaths
	// The agent's routing check re-checks the commit the same way its
	// workflow would, so a declined commit starts nothing.
	if ok, err := t.activities.CheckCommit(context.Background(), input); err != nil || !ok {
		return nil, false
	}
	return input, true
}

// InputFromEvent builds the workflow input from a dispatched commit event.
// It reports false for events that cannot start an implementation:
// non-commit events, missing commit payloads, commits without a SHA, and
// commits on any agent branch (refine/* or implement/*, see
// backlog.IsAgentBranch). Whether the commit touches backlog tasks is
// decided by the shared backlog classification (see
// routeFromClassification); the trigger only starts the workflow when it
// does.
func InputFromEvent(ev event.Event) (any, bool) {
	if ev.Kind != event.KindCommit || ev.Commit == nil {
		return nil, false
	}
	c := ev.Commit
	if c.SHA == "" {
		return nil, false
	}
	if backlog.IsAgentBranch(c.Branch) {
		return nil, false
	}
	return Input{
		Provider:   ev.Provider,
		Project:    ev.Project,
		Repository: ev.Repository,
		Branch:     c.Branch,
		SHA:        c.SHA,
		Message:    c.Message,
		URL:        c.URL,
		Author:     authorName(c.Author),
		Added:      c.Added,
		Removed:    c.Removed,
		Modified:   c.Modified,
	}, true
}

// authorName picks the most human-readable author identifier the provider
// filled in.
func authorName(p event.Person) string {
	switch {
	case p.Username != "":
		return p.Username
	case p.Name != "":
		return p.Name
	default:
		return p.Email
	}
}
