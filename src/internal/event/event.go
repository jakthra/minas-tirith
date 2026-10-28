// Package event defines the provider-agnostic event model produced by event
// sources and consumed by downstream subscribers.
//
// Every detected piece of repository activity is normalized into an Event
// carrying a Kind and exactly one kind-specific payload (Commit or Comment).
// Providers (Forgejo today, others later) never leak their payload formats
// into the event model, so consumers stay independent of where an event came
// from.
package event

import (
	"fmt"
	"strings"
)

// Kind identifies what an Event represents.
type Kind string

const (
	// KindCommit reports that a new commit was pushed to a watched
	// repository.
	KindCommit Kind = "commit"
	// KindComment reports that a new comment was added on a merge request.
	KindComment Kind = "comment"
)

// Person identifies the user behind an event. Providers fill whichever
// fields they know.
type Person struct {
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
}

// MergeRequest identifies the merge request a Comment belongs to.
type MergeRequest struct {
	Number int64  `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`
	Author Person `json:"author,omitempty"`
}

// Commit holds the details of a single pushed commit.
type Commit struct {
	SHA       string   `json:"sha"`
	Message   string   `json:"message,omitempty"`
	URL       string   `json:"url,omitempty"`
	Author    Person   `json:"author,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"` // provider-formatted commit time
	Ref       string   `json:"ref,omitempty"`       // full ref, e.g. refs/heads/main
	Branch    string   `json:"branch,omitempty"`    // short branch name, e.g. main
	Added     []string `json:"added,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Modified  []string `json:"modified,omitempty"`
}

// CommentPosition is a comment's inline location: the file (and the line
// within it) the comment was posted on. A summary comment on the merge
// request overall carries no position. Providers that cannot report
// inline locations (Forgejo webhooks today) leave Position nil, and
// consumers infer inline-ness from the comment body instead.
type CommentPosition struct {
	// File is the commented file's path from the repository root.
	File string `json:"file"`
	// Line is the commented line number within File, 0 when unknown.
	Line int `json:"line,omitempty"`
}

// Comment holds the details of a merge request comment.
type Comment struct {
	ID        int64             `json:"id"`
	Body      string            `json:"body,omitempty"`
	URL       string            `json:"url,omitempty"`
	Author    Person            `json:"author,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"` // provider-formatted comment time
	MR        *MergeRequest     `json:"mr,omitempty"`
	Position  *CommentPosition  `json:"position,omitempty"` // inline location, when the provider reports one
}

// Event is a provider-agnostic repository event. Exactly one of Commit or
// Comment is set, matching Kind.
type Event struct {
	Kind       Kind   `json:"kind"`
	Provider   string `json:"provider"`   // event source provider, e.g. "forgejo"
	Project    string `json:"project"`    // watched project name, e.g. "BeanieGenie"
	Repository string `json:"repository"` // provider full repo name, e.g. "root/BeanieGenie"

	Commit  *Commit  `json:"commit,omitempty"`
	Comment *Comment `json:"comment,omitempty"`
}

// DedupeKey uniquely identifies the underlying repository activity so that
// duplicate deliveries of the same activity (e.g. provider redelivery) collapse
// into a single dispatch.
func (e Event) DedupeKey() string {
	repo := e.Repository
	if repo == "" {
		repo = e.Project
	}
	switch {
	case e.Kind == KindCommit && e.Commit != nil:
		return fmt.Sprintf("%s|%s|commit|%s", e.Provider, repo, e.Commit.SHA)
	case e.Kind == KindComment && e.Comment != nil:
		return fmt.Sprintf("%s|%s|comment|%d", e.Provider, repo, e.Comment.ID)
	default:
		return fmt.Sprintf("%s|%s|%s", e.Provider, repo, e.Kind)
	}
}

// Summary returns a one-line human-readable description of the event for
// logging.
func (e Event) Summary() string {
	switch {
	case e.Kind == KindCommit && e.Commit != nil:
		return fmt.Sprintf("commit %s pushed to %s (branch %s): %s",
			shortSHA(e.Commit.SHA), e.Repository, e.Commit.Branch, firstLine(e.Commit.Message))
	case e.Kind == KindComment && e.Comment != nil:
		mr := "?"
		if e.Comment.MR != nil {
			mr = fmt.Sprintf("!%d %s", e.Comment.MR.Number, e.Comment.MR.Title)
		}
		return fmt.Sprintf("comment by %s on %s (%s): %s",
			e.Comment.Author.Username, e.Repository, mr, firstLine(e.Comment.Body))
	default:
		return fmt.Sprintf("%s event on %s", e.Kind, e.Repository)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
