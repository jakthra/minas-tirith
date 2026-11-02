package backlog

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/activity"

	"minas-tirith/internal/event"
	"minas-tirith/internal/provider"
)

// ClassifyCommitActivity is the Temporal activity type name for the
// shared backlog classification. It is registered by the user story
// review template and invoked by the event bridge's activity triggers
// before either agent's workflow starts.
const ClassifyCommitActivity = "backlog.classifyCommit"

// Commit is the commit whose backlog files are classified.
type Commit struct {
	// Repository is the provider full repo name, e.g. "root/BeanieGenie".
	Repository string `json:"repository"`
	// SHA is the commit to read the files at.
	SHA string `json:"sha"`
	// Added and Modified list the commit's changed paths; Removed paths
	// are deletions and never classified.
	Added    []string `json:"added,omitempty"`
	Modified []string `json:"modified,omitempty"`
}

// Classification is the routing decision for one commit: which of its
// backlog files are tasks and which are user stories, in commit order.
type Classification struct {
	// TaskPaths lists the backlog task files the commit adds or modifies.
	TaskPaths []string `json:"task_paths,omitempty"`
	// StoryPaths lists the backlog user story files the commit adds or
	// modifies.
	StoryPaths []string `json:"story_paths,omitempty"`
	// Reason explains the decision (visible in logs and workflow
	// results).
	Reason string `json:"reason"`
}

// Activities holds the dependencies the classification needs: the
// provider client for repository access. Its methods are registered as
// Temporal activities, keeping every provider API call inside an
// activity (Temporal determinism preserved).
type Activities struct {
	provider provider.Client
}

// NewActivities returns the activity set wired to the given provider
// client. A nil client is accepted (the listener registers the template
// without executing activities); ClassifyCommit fails with a clear error
// when called without one.
func NewActivities(p provider.Client) *Activities {
	return &Activities{provider: p}
}

// ClassifyCommit reads the backlog files a commit event's commit adds or
// modifies and reports which are tasks and which are user stories. It
// runs as a Temporal activity so the provider API calls that read the
// files stay out of the workflow body and out of the event bridge.
//
// The activity takes the whole event — not just the extracted commit —
// because it crosses a Temporal boundary: the event bridge passes the
// dispatched event, and the SDK serializes it on one side and decodes it
// on the other, which only preserves the type when both sides share it.
func (a *Activities) ClassifyCommit(ctx context.Context, ev event.Event) (Classification, error) {
	c := commitFromEvent(ev)
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("classifying backlog files",
			"repository", c.Repository, "sha", shortSHA(c.SHA),
			"files", len(c.Added)+len(c.Modified))
	}
	if a.provider == nil {
		return Classification{}, fmt.Errorf("backlog: no provider client configured")
	}
	return classifyCommit(ctx, a.provider, c)
}

// commitFromEvent extracts the commit a classification reads from a
// dispatched event. A non-commit event or one without a commit payload
// yields an empty commit, which classifyCommit rejects: classifying
// needs a repository and a sha.
func commitFromEvent(ev event.Event) Commit {
	var c Commit
	if ev.Kind != event.KindCommit || ev.Commit == nil {
		return c
	}
	c.Repository = ev.Repository
	c.SHA = ev.Commit.SHA
	c.Added = ev.Commit.Added
	c.Modified = ev.Commit.Modified
	return c
}

// classifyCommit is the logic of ClassifyCommit, separated so tests can
// exercise it with a provider fake directly.
func classifyCommit(ctx context.Context, p provider.Client, c Commit) (Classification, error) {
	if c.Repository == "" || c.SHA == "" {
		return Classification{}, fmt.Errorf("backlog: classifying a commit needs a repository and a sha")
	}

	var out Classification
	seen := make(map[string]bool)
	for _, path := range append(append([]string{}, c.Added...), c.Modified...) {
		if !IsFile(path) || seen[path] {
			continue
		}
		seen[path] = true

		// Read the file at the commit itself, so the classification sees
		// the content the commit introduced.
		file, err := p.GetFile(ctx, c.Repository, path, c.SHA)
		if err != nil {
			return Classification{}, fmt.Errorf("read %s at %s in %s: %w", path, shortSHA(c.SHA), c.Repository, err)
		}
		if Classify(string(file.Content)) == KindTask {
			out.TaskPaths = append(out.TaskPaths, path)
		} else {
			out.StoryPaths = append(out.StoryPaths, path)
		}
	}

	switch {
	case len(out.TaskPaths) == 0 && len(out.StoryPaths) == 0:
		out.Reason = "commit does not add or modify any backlog file under " + Dir
	default:
		out.Reason = fmt.Sprintf("commit adds or modifies %d backlog task file(s) and %d user story file(s) under %s",
			len(out.TaskPaths), len(out.StoryPaths), Dir)
	}
	return out, nil
}

// shortSHA shortens a commit hash for logging and errors.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
