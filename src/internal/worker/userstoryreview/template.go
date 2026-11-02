package userstoryreview

import (
	"context"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
	"minas-tirith/internal/worker/backlog"
)

// template implements worker.Template for the user story review agent.
// It holds the activities wired to the provider and LLM clients, and the
// shared backlog classification activity.
type template struct {
	activities *Activities
	backlog    *backlog.Activities
}

// Name identifies the template; its task queue is the configured base
// queue plus "-userstoryreview".
func (template) Name() string { return "userstoryreview" }

// Workflows lists the workflow types the template registers: the
// user story review workflow and the merge request comment loop
// (MrCommentWorkflow with its per-MR processor child).
func (template) Workflows() []worker.Workflow {
	return []worker.Workflow{
		{Name: WorkflowName, Fn: UserStoryReviewWorkflow},
		{Name: MrCommentWorkflowName, Fn: MrCommentWorkflow},
		{Name: MrCommentProcessorWorkflowName, Fn: MrCommentProcessorWorkflow},
	}
}

// Activities lists the activity types the template registers. The
// backlog classification is shared with the other backlog agents: it is
// registered here (this template owns the provider client) and invoked by
// the event bridge before any agent's workflow starts.
func (t template) Activities() []worker.Activity {
	return []worker.Activity{
		{Name: backlog.ClassifyCommitActivity, Fn: t.backlog.ClassifyCommit},
		{Name: RefineCommitName, Fn: t.activities.RefineCommit},
		{Name: AnalyzeCommitName, Fn: t.activities.AnalyzeCommit},
		{Name: FetchStoryName, Fn: t.activities.FetchStory},
		{Name: GenerateTicketsName, Fn: t.activities.GenerateTickets},
		{Name: CreateRefinementBranchName, Fn: t.activities.CreateRefinementBranch},
		{Name: CommitTicketsName, Fn: t.activities.CommitTickets},
		{Name: OpenMergeRequestName, Fn: t.activities.OpenMergeRequest},
		{Name: FindRefinementName, Fn: t.activities.FindRefinement},
		{Name: CollectTicketsName, Fn: t.activities.CollectTickets},
		{Name: InterpretCommentName, Fn: t.activities.InterpretComment},
		{Name: ApplyTicketUpdatesName, Fn: t.activities.ApplyTicketUpdates},
		{Name: PostResponseCommentName, Fn: t.activities.PostResponseComment},
	}
}

// Triggers lists the repository events that start the workflows.
// Commit events start a user story review when the commit adds or
// modifies a user story file: the shared backlog classification decides
// that before the workflow starts, so a commit that only adds backlog
// tasks starts the implementation agent instead. Comment events on the
// agent's refinement merge requests start the MR comment loop.
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
		{
			Kind:     event.KindComment,
			Workflow: MrCommentWorkflowName,
			Input:    MrCommentInputFromEvent,
		},
	}
}

// Template returns the user story review agent's worker template, wired
// to the given provider and LLM clients. The activities use these clients
// for repository access and ticket generation.
func Template(p provider.Client, l llm.Client) worker.Template {
	return template{activities: NewActivities(p, l), backlog: backlog.NewActivities(p)}
}

// routeFromClassification builds the workflow input from a dispatched
// commit event and the shared backlog classification. It reports false
// for events that cannot start a review: non-commit events, missing
// commit payloads, commits without a SHA, commits on any agent branch
// (refine/* or implement/*) — those carry agent-generated backlog files,
// and acting on them would make the agents re-trigger on their own
// commits in an endless loop — and commits that add or modify no user
// story file. The classification arrives as the decoded
// *backlog.Classification the trigger's ClassifyResult factory declared.
func (t template) routeFromClassification(ev event.Event, classification any) (any, bool) {
	c, ok := classification.(*backlog.Classification)
	if !ok || len(c.StoryPaths) == 0 {
		return nil, false
	}
	in, ok := InputFromEvent(ev)
	if !ok {
		return nil, false
	}
	input := in.(Input)
	input.StoryPaths = c.StoryPaths
	// The agent's routing check re-checks the commit the same way its
	// workflow would, so a declined commit starts nothing.
	if ok, err := t.activities.RefineCommit(context.Background(), input); err != nil || !ok {
		return nil, false
	}
	return input, true
}

// InputFromEvent builds the workflow input from a dispatched commit event.
// It reports false for events that cannot start a review: non-commit
// events, missing commit payloads, commits without a SHA, and commits on
// any agent branch (refine/* or implement/*, see backlog.IsAgentBranch).
// Whether the commit touches user stories is decided by the shared
// backlog classification (see routeFromClassification); the trigger only
// starts the workflow when it does.
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
