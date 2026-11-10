// Package provider abstracts provider (Forgejo, and Gitea which shares
// its API) repository access behind a small Go interface.
//
// The autonomous agent workflows interact with the repository through
// Client: reading files (backlog items, source code), listing
// directories, creating branches, committing file changes, and opening
// and discussing merge requests. Provider API calls live in Temporal
// activities, never in workflow bodies, so workflow code stays
// deterministic; and Client is an interface, so activity tests
// substitute fakes (Fake) without any real API calls.
package provider

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotFound reports that the requested repository, file, branch, or
// merge request does not exist (the provider answered 404). Operations
// that resolve paths against a branch (Commit) also use it when the
// branch itself is missing.
var ErrNotFound = errors.New("provider resource not found")

// APIError is a provider API error response outside the 2xx range. The
// StatusCode carries the provider's HTTP status; Message carries the
// provider's error message when it sent one.
type APIError struct {
	// StatusCode is the HTTP status the provider answered.
	StatusCode int
	// Status is the HTTP status line, e.g. "422 Unprocessable Entity".
	Status string
	// Message is the provider's error message, if any.
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("provider api error: %s", e.Status)
	}
	return fmt.Sprintf("provider api error: %s: %s", e.Status, e.Message)
}

// File is a repository file's content and identity.
type File struct {
	// Path is the file's path from the repository root.
	Path string
	// Content is the decoded file content.
	Content []byte
	// SHA is the file blob's SHA, as the provider computes it. Update
	// flows require it.
	SHA string
	// Size is the file size in bytes.
	Size int64
}

// FileInfo identifies one file in a directory listing.
type FileInfo struct {
	// Path is the file's path from the repository root.
	Path string
	// SHA is the file blob's SHA.
	SHA string
	// Size is the file size in bytes.
	Size int64
}

// Branch is a repository branch.
type Branch struct {
	// Name is the short branch name, e.g. "feature/story-1".
	Name string
	// SHA is the head commit the branch points at.
	SHA string
}

// FileChange is one file to write in a commit: create the file or
// update it, whichever its current state requires.
type FileChange struct {
	// Path is the file's path from the repository root.
	Path string
	// Content is the new file content.
	Content []byte
}

// CommitResult reports the commit a Commit operation produced.
type CommitResult struct {
	// SHA is the new commit's hash.
	SHA string
	// URL is the commit's page on the provider.
	URL string
}

// MergeRequest is a provider merge request (pull request).
type MergeRequest struct {
	// Number identifies the MR within the repository (provider issue
	// index).
	Number int64
	// Title is the MR title.
	Title string
	// Description is the MR body.
	Description string
	// State is the MR state, e.g. "open" or "closed".
	State string
	// Draft reports whether the MR is a draft (work in progress) that
	// is not ready to merge.
	Draft bool
	// Head is the source branch of the MR.
	Head string
	// Base is the target branch of the MR.
	Base string
	// URL is the MR's page on the provider.
	URL string
	// Author is the MR creator's username.
	Author string
}

// Comment is a comment on a merge request.
type Comment struct {
	// ID identifies the comment.
	ID int64
	// Body is the comment text (markdown).
	Body string
	// Author is the comment author's username.
	Author string
	// CreatedAt is the provider-formatted creation time.
	CreatedAt string
	// Path and Line are the comment's inline location when it is a review
	// (diff) comment on a specific file and line; both are empty for a
	// summary comment on the merge request overall.
	Path string
	Line int
}

// Webhook is a repository webhook the provider delivers events to.
type Webhook struct {
	// ID identifies the webhook on the provider.
	ID int64
	// URL is the target address deliveries are sent to.
	URL string
	// Events are the provider's event names that trigger deliveries,
	// e.g. "push" or "issue_comment".
	Events []string
	// Active reports whether the webhook is enabled and delivers.
	Active bool
}

// Client abstracts the provider repository operations the autonomous
// agents and the installer need. Implementations live in this package
// (ForgejoClient for the real API, Fake for tests); activities and
// their tests depend on this interface, so no provider API is called
// from test code.
//
// A Client serves every repository of one provider instance; each
// operation names its repository. Repositories are addressed by their
// full name ("owner/repo"). Refs are branch names or commit SHAs; an
// empty ref means the repository's default branch.
type Client interface {
	// GetFile returns the file content at path on ref. It fails with
	// ErrNotFound when the repository, path, or ref does not exist.
	GetFile(ctx context.Context, repo, path, ref string) (File, error)

	// ListFiles lists every file under path on ref, recursively,
	// including files in nested directories, sorted by path. An empty
	// path lists the whole repository; when path names a file, that
	// file is the single entry. It fails with ErrNotFound when the
	// repository, path, or ref does not exist.
	ListFiles(ctx context.Context, repo, path, ref string) ([]FileInfo, error)

	// CreateBranch creates branch from the existing ref (branch, tag,
	// or commit; empty ref = default branch) and returns the new branch
	// with its head commit.
	CreateBranch(ctx context.Context, repo, branch, fromRef string) (Branch, error)

	// Commit writes the file changes to branch as one new commit,
	// creating or updating each file as its current state requires, and
	// returns the resulting commit. The branch must exist.
	Commit(ctx context.Context, repo, branch, message string, files []FileChange) (CommitResult, error)

	// CreateMergeRequest opens a merge request from branch head to
	// branch base and returns it. A draft merge request is a work in
	// progress that is not ready to merge; callers choose explicitly
	// with draft, and the returned MergeRequest reports the provider's
	// answer in its Draft field.
	CreateMergeRequest(ctx context.Context, repo, title, description, head, base string, draft bool) (MergeRequest, error)

	// GetMergeRequest returns the merge request details by number. It
	// fails with ErrNotFound when the repository or merge request does
	// not exist.
	GetMergeRequest(ctx context.Context, repo string, number int64) (MergeRequest, error)

	// ListMergeRequestComments returns the comments on a merge request,
	// oldest first.
	ListMergeRequestComments(ctx context.Context, repo string, number int64) ([]Comment, error)

	// ListMergeRequestReviewComments returns the merge request's inline
	// (diff) review comments across its reviews, oldest first, each with
	// its Path and Line set. Summary comments on the merge request
	// conversation are not included — see ListMergeRequestComments.
	ListMergeRequestReviewComments(ctx context.Context, repo string, number int64) ([]Comment, error)

	// AddMergeRequestComment adds a comment to a merge request and
	// returns it.
	AddMergeRequestComment(ctx context.Context, repo string, number int64, body string) (Comment, error)

	// CreateWebhook registers a webhook on repo: the provider delivers
	// the named events, as JSON signed with secret, to targetURL, and
	// returns the created webhook. events are the provider's webhook
	// event names, e.g. "push" or "issue_comment".
	CreateWebhook(ctx context.Context, repo, targetURL, secret string, events []string) (Webhook, error)
}
