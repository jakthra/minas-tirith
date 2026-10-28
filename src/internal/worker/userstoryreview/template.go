package userstoryreview

import (
	"strings"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker"
)

// template implements worker.Template for the user story review agent.
// It holds the activities wired to the provider and LLM clients.
type template struct {
	activities *Activities
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

// Activities lists the activity types the template registers.
func (t template) Activities() []worker.Activity {
	return []worker.Activity{
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
// Commit events start a user story review; comment events on the
// agent's refinement merge requests start the MR comment loop.
func (template) Triggers() []worker.Trigger {
	return []worker.Trigger{
		{
			Kind:     event.KindCommit,
			Workflow: WorkflowName,
			Input:    InputFromEvent,
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
	return template{activities: NewActivities(p, l)}
}

// InputFromEvent builds the workflow input from a dispatched commit event.
// It reports false for events that cannot start a review: non-commit
// events, missing commit payloads, commits without a SHA, and commits on
// the agent's own refinement branches — those carry the agent's generated
// ticket files, and refining them would make the agent re-trigger on its
// own commits in an endless loop.
func InputFromEvent(ev event.Event) (any, bool) {
	if ev.Kind != event.KindCommit || ev.Commit == nil {
		return nil, false
	}
	c := ev.Commit
	if c.SHA == "" {
		return nil, false
	}
	if strings.HasPrefix(c.Branch, RefinementBranchPrefix) {
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
