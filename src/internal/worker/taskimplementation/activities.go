package taskimplementation

import (
	"context"

	"go.temporal.io/sdk/activity"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker/backlog"
)

// CheckCommitName is the Temporal activity type name for the routing check
// that declines commits the implementation agent must not act on.
const CheckCommitName = "taskImplementation.checkCommit"

// Activities holds the dependencies the task implementation activities
// need: the provider client for repository access and the LLM client for
// implementing the task. Its methods are registered as Temporal
// activities, keeping every provider and LLM API call inside an activity
// (Temporal determinism preserved).
type Activities struct {
	provider provider.Client
	llm      llm.Client
}

// NewActivities returns the activity set wired to the given provider and
// LLM clients. Nil clients are accepted (the listener registers the
// template without executing activities); the methods that need a client
// fail with a clear error when called without one.
func NewActivities(p provider.Client, l llm.Client) *Activities {
	return &Activities{provider: p, llm: l}
}

// CheckCommit re-checks a commit the shared backlog classification
// assigned to this agent before the workflow acts on it: it reports false
// for a commit on any agent branch (refine/* or implement/*, see
// backlog.IsAgentBranch; the trigger declines those, this guards manual
// starts) and for one whose task files are not backlog files. It runs as
// a Temporal activity so the event bridge can make the same decision
// before starting the workflow.
func (a *Activities) CheckCommit(ctx context.Context, in Input) (bool, error) {
	if err := in.Validate(); err != nil {
		return false, err
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("checking commit for task implementation",
			"repository", in.Repository, "branch", in.Branch,
			"sha", ShortSHA(in.SHA), "files", len(in.TaskPaths))
	}
	if backlog.IsAgentBranch(in.Branch) {
		return false, nil
	}
	return len(taskPaths(in)) > 0, nil
}

// taskPaths returns the input's task files that are backlog files, in
// order and without duplicates.
func taskPaths(in Input) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, path := range in.TaskPaths {
		if backlog.IsFile(path) && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}
