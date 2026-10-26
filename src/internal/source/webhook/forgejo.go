package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"minas-tirith/internal/event"
)

// ForgejoAdapter handles webhook deliveries from Forgejo (and Gitea, whose
// wire format it shares). It verifies HMAC-SHA256 signatures and parses
// push and issue-comment payloads into provider-agnostic events.
//
// Header and payload formats were captured from a live Forgejo 16 instance;
// Forgejo sends both X-Forgejo-* and X-Gitea-* header aliases with identical
// values.
type ForgejoAdapter struct {
	name string
}

// NewForgejoAdapter returns an adapter served under the given {provider}
// name, e.g. "forgejo" or "gitea".
func NewForgejoAdapter(name string) *ForgejoAdapter {
	return &ForgejoAdapter{name: name}
}

// Name implements Provider.
func (a *ForgejoAdapter) Name() string { return a.name }

// Verify implements Provider: Forgejo signs the raw request body with
// HMAC-SHA256 using the webhook's shared secret and sends the hex digest in
// X-Forgejo-Signature (and the X-Gitea-Signature / X-Gogs-Signature aliases).
func (a *ForgejoAdapter) Verify(header http.Header, body []byte, secret string) error {
	signature := header.Get("X-Forgejo-Signature")
	if signature == "" {
		signature = header.Get("X-Gitea-Signature")
	}
	if signature == "" {
		signature = header.Get("X-Gogs-Signature")
	}
	if signature == "" {
		return fmt.Errorf("missing signature header")
	}
	given, err := hex.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("malformed signature: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), given) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// Parse implements Provider.
func (a *ForgejoAdapter) Parse(header http.Header, body []byte, project string) ([]event.Event, error) {
	switch strings.ToLower(deliveryEvent(header)) {
	case "push":
		return a.parsePush(body, project)
	// Forgejo reports merge request comments as event "issue_comment" with
	// event type "pull_request_comment"; older Gitea versions may use
	// "pull_request_comment" as the event itself. The payload shape is the
	// same for both.
	case "issue_comment", "pull_request_comment":
		return a.parseIssueComment(body, project)
	default:
		slog.Info("ignoring forgejo delivery", "event", deliveryEvent(header), "project", project)
		return nil, nil
	}
}

// deliveryEvent returns the logical event name from the delivery headers.
func deliveryEvent(h http.Header) string {
	for _, name := range []string{"X-Forgejo-Event", "X-Gitea-Event", "X-Gogs-Event", "X-Github-Event"} {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}

// --- Forgejo payload structs (only the fields the listener uses) ---

type pushPayload struct {
	Ref        string          `json:"ref"`
	Before     string          `json:"before"`
	After      string          `json:"after"`
	Repository repositoryField `json:"repository"`
	Commits    []commitField   `json:"commits"`
}

type commitField struct {
	ID        string        `json:"id"`
	Message   string        `json:"message"`
	URL       string        `json:"url"`
	Author    *gitUserField `json:"author"`
	Timestamp string        `json:"timestamp"`
	Added     []string      `json:"added"`
	Removed   []string      `json:"removed"`
	Modified  []string      `json:"modified"`
}

// gitUserField is a git author/committer identity as sent in push payloads.
type gitUserField struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

// userField is a Forgejo account as sent in comment/pull-request payloads.
type userField struct {
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

type repositoryField struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

type issueCommentPayload struct {
	Action      string            `json:"action"`
	IsPull      bool              `json:"is_pull"`
	Issue       *issueField       `json:"issue"`
	Comment     *commentField     `json:"comment"`
	PullRequest *pullRequestField `json:"pull_request"`
	Repository  repositoryField   `json:"repository"`
}

type issueField struct {
	Number      int64             `json:"number"`
	Title       string            `json:"title"`
	PullRequest *issuePullRequest `json:"pull_request"`
}

// issuePullRequest is present on the issue only when it is a merge request.
type issuePullRequest struct {
	Merged bool `json:"merged"`
}

type commentField struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	CreatedAt string     `json:"created_at"`
	URL       string     `json:"html_url"`
	User      *userField `json:"user"`
}

type pullRequestField struct {
	Number int64      `json:"number"`
	Title  string     `json:"title"`
	URL    string     `json:"html_url"`
	State  string     `json:"state"`
	User   *userField `json:"user"`
}

// --- parsing ---

// parsePush turns a push delivery into one commit event per pushed commit.
// Only branch pushes carry new commits; tag pushes and ref deletions are
// ignored.
func (a *ForgejoAdapter) parsePush(body []byte, project string) ([]event.Event, error) {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parse push payload: %w", err)
	}
	branch, ok := branchFromRef(p.Ref)
	if !ok {
		slog.Info("ignoring push to non-branch ref", "ref", p.Ref, "project", project)
		return nil, nil
	}
	events := make([]event.Event, 0, len(p.Commits))
	for _, c := range p.Commits {
		ev := event.Event{
			Kind:       event.KindCommit,
			Provider:   a.name,
			Project:    project,
			Repository: p.Repository.FullName,
			Commit: &event.Commit{
				SHA:       c.ID,
				Message:   c.Message,
				URL:       c.URL,
				Timestamp: c.Timestamp,
				Ref:       p.Ref,
				Branch:    branch,
				Added:     c.Added,
				Removed:   c.Removed,
				Modified:  c.Modified,
			},
		}
		if c.Author != nil {
			ev.Commit.Author = event.Person{
				Name:     c.Author.Name,
				Email:    c.Author.Email,
				Username: c.Author.Username,
			}
		}
		events = append(events, ev)
	}
	return events, nil
}

// parseIssueComment turns an issue-comment delivery into a comment event,
// but only for comments newly created on merge requests: plain issue
// comments and comment edits are out of scope.
func (a *ForgejoAdapter) parseIssueComment(body []byte, project string) ([]event.Event, error) {
	var p issueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parse issue comment payload: %w", err)
	}
	if p.Action != "created" {
		slog.Info("ignoring comment action", "action", p.Action, "project", project)
		return nil, nil
	}
	isPull := p.IsPull
	if p.Issue != nil && p.Issue.PullRequest != nil {
		isPull = true
	}
	if !isPull || p.Comment == nil {
		slog.Info("ignoring non-merge-request comment", "project", project)
		return nil, nil
	}

	mr := event.MergeRequest{State: "open"}
	switch {
	case p.PullRequest != nil:
		mr.Number = p.PullRequest.Number
		mr.Title = p.PullRequest.Title
		mr.URL = p.PullRequest.URL
		mr.State = p.PullRequest.State
		if p.PullRequest.User != nil {
			mr.Author = personFromUser(p.PullRequest.User)
		}
	case p.Issue != nil:
		mr.Number = p.Issue.Number
		mr.Title = p.Issue.Title
	}

	ev := event.Event{
		Kind:       event.KindComment,
		Provider:   a.name,
		Project:    project,
		Repository: p.Repository.FullName,
		Comment: &event.Comment{
			ID:        p.Comment.ID,
			Body:      p.Comment.Body,
			URL:       p.Comment.URL,
			CreatedAt: p.Comment.CreatedAt,
			MR:        &mr,
		},
	}
	if p.Comment.User != nil {
		ev.Comment.Author = personFromUser(p.Comment.User)
	}
	return []event.Event{ev}, nil
}

// branchFromRef splits a full ref into its branch name. Only branch refs
// count; tags, notes, and other refs are rejected.
func branchFromRef(ref string) (string, bool) {
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	return branch, ok && branch != ""
}

// personFromUser maps a Forgejo account onto the provider-agnostic person.
func personFromUser(u *userField) event.Person {
	return event.Person{
		Username: u.Login,
		Name:     u.FullName,
		Email:    u.Email,
	}
}
