package userstoryreview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// --- FindRefinement: correlate a comment to the refinement run (AC9) ---

// FindRefinementInput is the input to the FindRefinement activity.
type FindRefinementInput struct {
	// Provider is the event source provider, e.g. "forgejo"; it names
	// the provider in the refinement workflow ID the correlation
	// reconstructs.
	Provider string `json:"provider"`
	// Repository is the provider full repo name, e.g. "root/BeanieGenie".
	Repository string `json:"repository"`
	// MRNumber is the merge request the comment is on: the MR number
	// the review workflow recorded in the Submission of its run.
	MRNumber int64 `json:"mr_number"`
	// CommentID identifies the comment being processed; used to detect
	// comments an earlier run already answered.
	CommentID int64 `json:"comment_id"`
}

// FindRefinementOutput is the correlation the FindRefinement activity
// established.
type FindRefinementOutput struct {
	// IsRefinement reports whether the merge request is one of the
	// agent's refinement merge requests: its head branch is a refine/*
	// branch the agent created (see RefinementBranchName). Anything else
	// is out of scope for the comment loop, whatever its title.
	IsRefinement bool `json:"is_refinement"`
	// AlreadyProcessed reports whether the agent already responded to
	// this comment in an earlier run: the merge request carries a
	// response comment referencing it. Redelivering a comment whose
	// run failed after committing but before responding is still safe.
	AlreadyProcessed bool `json:"already_processed"`
	// RefinementBranch is the merge request's head branch: the refine/*
	// branch carrying the refinement tickets.
	RefinementBranch string `json:"refinement_branch,omitempty"`
	// BaseBranch is the merge request's target branch.
	BaseBranch string `json:"base_branch,omitempty"`
	// OriginCommitShortSHA is the short commit SHA the refinement
	// branch name embeds: the SHA of the commit the originating review
	// workflow ran on (empty when the branch name carries none).
	OriginCommitShortSHA string `json:"origin_commit_short_sha,omitempty"`
	// RefinementWorkflowIDPrefix identifies the originating review
	// workflow execution: its workflow ID is this prefix plus the
	// remainder of the origin commit's SHA.
	RefinementWorkflowIDPrefix string `json:"refinement_workflow_id_prefix,omitempty"`
}

// FindRefinement correlates a comment event to the refinement run that
// created the merge request, and is the authoritative agent-MR check:
// whatever the title suggested, the loop only proceeds when the MR's
// head branch is one of the agent's refine/* branches. It runs as a
// Temporal activity so the provider API calls stay out of the workflow
// body.
func (a *Activities) FindRefinement(ctx context.Context, in FindRefinementInput) (FindRefinementOutput, error) {
	if a.provider == nil {
		return FindRefinementOutput{}, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("correlating mr comment to its refinement run",
			"repository", in.Repository, "mr", in.MRNumber, "comment", in.CommentID)
	}

	mr, err := a.provider.GetMergeRequest(ctx, in.Repository, in.MRNumber)
	if err != nil {
		return FindRefinementOutput{}, fmt.Errorf("fetch merge request %d in %s: %w",
			in.MRNumber, in.Repository, err)
	}

	out := FindRefinementOutput{
		RefinementBranch: mr.Head,
		BaseBranch:       mr.Base,
	}
	if !strings.HasPrefix(mr.Head, RefinementBranchPrefix) {
		// Not one of the agent's refinement MRs. Not a failure: the
		// trigger's title filter is a heuristic, and a user MR titled
		// "Refine ..." must be ignored, not retried.
		return out, nil
	}
	out.IsRefinement = true
	out.OriginCommitShortSHA = refinementShortSHA(mr.Head)
	out.RefinementWorkflowIDPrefix = refinementWorkflowIDPrefix(in.Provider, in.Repository, out.OriginCommitShortSHA)

	// Detect comments an earlier run already answered: the agent's
	// response references the comment id it answers (see
	// responseMarkerFor). This keeps a redelivered or restarted comment
	// from being applied twice.
	comments, err := a.provider.ListMergeRequestComments(ctx, in.Repository, in.MRNumber)
	if err != nil {
		return FindRefinementOutput{}, fmt.Errorf("list comments on merge request %d in %s: %w",
			in.MRNumber, in.Repository, err)
	}
	reference := responseMarkerFor(in.CommentID)
	for _, c := range comments {
		if strings.Contains(c.Body, reference) {
			out.AlreadyProcessed = true
			break
		}
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("correlated mr comment",
			"repository", in.Repository, "mr", in.MRNumber,
			"refinement_branch", out.RefinementBranch,
			"origin_commit", out.OriginCommitShortSHA,
			"already_processed", out.AlreadyProcessed)
	}
	return out, nil
}

// --- CollectTickets: the MR's tickets and their current content (AC4) ---

// CollectTicketsInput is the input to the CollectTickets activity.
type CollectTicketsInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// RefinementBranch is the merge request's head branch: the refine/*
	// branch carrying the tickets.
	RefinementBranch string `json:"refinement_branch"`
	// BaseBranch is the merge request's target branch.
	BaseBranch string `json:"base_branch"`
}

// RefinementTicket is one implementation ticket of a refinement merge
// request: its file on the refinement branch and its current content.
type RefinementTicket struct {
	// Path is the ticket file's path from the repository root.
	Path string `json:"path"`
	// Title is the ticket's title from its frontmatter (empty when the
	// file carries none).
	Title string `json:"title,omitempty"`
	// Content is the ticket file's current content on the refinement
	// branch.
	Content string `json:"content"`
}

// CollectTickets returns the merge request's implementation tickets
// with their current content: the backlog.md files the refinement
// branch added over the base branch. The story file the refinement was
// generated from lives on the base branch too, so it is excluded; the
// agent's own updates from earlier comment rounds stay included. It
// runs as a Temporal activity so the provider API calls stay out of
// the workflow body.
func (a *Activities) CollectTickets(ctx context.Context, in CollectTicketsInput) ([]RefinementTicket, error) {
	if a.provider == nil {
		return nil, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("collecting refinement tickets",
			"repository", in.Repository, "branch", in.RefinementBranch, "base", in.BaseBranch)
	}

	branchFiles, err := a.provider.ListFiles(ctx, in.Repository, storyDir, in.RefinementBranch)
	if err != nil && !errors.Is(err, provider.ErrNotFound) {
		return nil, fmt.Errorf("list %s on %s in %s: %w", storyDir, in.RefinementBranch, in.Repository, err)
	}
	baseFiles, err := a.provider.ListFiles(ctx, in.Repository, storyDir, in.BaseBranch)
	if err != nil && !errors.Is(err, provider.ErrNotFound) {
		return nil, fmt.Errorf("list %s on %s in %s: %w", storyDir, in.BaseBranch, in.Repository, err)
	}
	onBase := make(map[string]bool, len(baseFiles))
	for _, f := range baseFiles {
		onBase[f.Path] = true
	}

	var tickets []RefinementTicket
	for _, f := range branchFiles {
		if onBase[f.Path] {
			continue
		}
		file, err := a.provider.GetFile(ctx, in.Repository, f.Path, in.RefinementBranch)
		if err != nil {
			return nil, fmt.Errorf("fetch ticket %s on %s: %w", f.Path, in.RefinementBranch, err)
		}
		content := string(file.Content)
		tickets = append(tickets, RefinementTicket{
			Path:    f.Path,
			Title:   parseStoryMeta(content).Title,
			Content: content,
		})
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("collected refinement tickets",
			"repository", in.Repository, "branch", in.RefinementBranch, "tickets", len(tickets))
	}
	return tickets, nil
}

// --- InterpretComment: read the comment with the LLM (AC3, AC5, AC8) ---

// InterpretCommentInput is the input to the InterpretComment activity.
type InterpretCommentInput struct {
	// Repository is the provider full repo name (for context).
	Repository string `json:"repository"`
	// MRNumber and MRTitle identify the refinement merge request (for
	// context).
	MRNumber int64  `json:"mr_number"`
	MRTitle  string `json:"mr_title,omitempty"`
	// Comment is the user comment being interpreted.
	Comment CommentFeedback `json:"comment"`
	// Tickets are the MR's implementation tickets with their current
	// content.
	Tickets []RefinementTicket `json:"tickets"`
}

// CommentFeedback is the user comment the agent interprets.
type CommentFeedback struct {
	// ID identifies the comment on the provider.
	ID int64 `json:"id"`
	// Body is the comment text (markdown).
	Body string `json:"body"`
	// Author is the comment author's username.
	Author string `json:"author,omitempty"`
	// PositionFile and PositionLine carry the comment's inline location
	// when the provider reported one; empty when it did not (Forgejo
	// webhooks do not deliver inline comment locations today).
	PositionFile string `json:"position_file,omitempty"`
	PositionLine int    `json:"position_line,omitempty"`
}

// CommentInterpretation is the LLM's reading of one comment.
type CommentInterpretation struct {
	// Classification is how the comment was read: inline, summary, or
	// none (see the Classification* constants).
	Classification string `json:"classification"`
	// Response is the prose acknowledging the comment, included in the
	// comment the agent posts on the merge request.
	Response string `json:"response"`
	// Updates are the ticket rewrites the comment asks for: the full
	// updated content of each ticket file.
	Updates []TicketUpdate `json:"updates"`
}

// TicketUpdate is one ticket file rewrite.
type TicketUpdate struct {
	// Path is the ticket file's path from the repository root.
	Path string `json:"path"`
	// Content is the ticket file's full updated content.
	Content string `json:"content"`
	// ChangeSummary is one sentence describing what changed and why,
	// shown in the agent's response comment.
	ChangeSummary string `json:"change_summary,omitempty"`
}

// commentInterpretationSystem is the system prompt for comment
// interpretation.
const commentInterpretationSystem = `You are the refinement agent that broke a user story into implementation tickets, now answering the user's review feedback on the refinement merge request.

You receive one user comment and the merge request's current tickets (backlog.md markdown files). Read the comment and decide what it asks for:
- "inline": the comment gives feedback on one or more specific tickets — it names, quotes, or references particular ticket files or ticket ids.
- "summary": the comment gives feedback on the overall refinement and may affect any of the tickets.
- "none": the comment is not feedback on the tickets (a greeting, thanks, or a question you should just answer in words).

When the tickets need changes, return the full updated content of every changed ticket file. Preserve the YAML frontmatter block exactly except the updated_date line, keep the markdown sections (## Context, ## What needs to happen, ## Acceptance Criteria) and the backlog.md task format, and apply the user's feedback. Do not create, rename, or delete ticket files: update the existing ones only. When the comment references tickets, identify them by their file path and the TASK-id in their frontmatter.

Respond with a single JSON object:
{"classification": "inline" | "summary" | "none", "updates": [{"path": "...", "content": "...", "change_summary": "one sentence"}], "response": "acknowledging prose for the user, in markdown"}

Use an empty updates array when no ticket needs changes. The response must acknowledge the comment and state what was changed and why; write it to the user. When the classification is none, answer or acknowledge the comment in the response without changing any ticket.`

// InterpretComment reads one merge request comment with the LLM and
// produces the ticket updates and response prose it asks for. It runs
// as a Temporal activity so the LLM API call stays out of the workflow
// body.
func (a *Activities) InterpretComment(ctx context.Context, in InterpretCommentInput) (CommentInterpretation, error) {
	if a.llm == nil {
		return CommentInterpretation{}, fmt.Errorf("userstoryreview: no llm client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("interpreting mr comment",
			"repository", in.Repository, "mr", in.MRNumber, "comment", in.Comment.ID,
			"position_file", in.Comment.PositionFile, "tickets", len(in.Tickets))
	}

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: commentInterpretationPrompt(in)},
	}
	var out CommentInterpretation
	if _, err := a.llm.ChatJSON(ctx, commentInterpretationSystem, messages, &out); err != nil {
		return CommentInterpretation{}, fmt.Errorf("interpret comment %d: %w", in.Comment.ID, err)
	}
	switch out.Classification {
	case ClassificationInline, ClassificationSummary, ClassificationNone:
	default:
		return CommentInterpretation{}, fmt.Errorf("interpret comment %d: unknown classification %q",
			in.Comment.ID, out.Classification)
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("interpreted mr comment",
			"repository", in.Repository, "mr", in.MRNumber, "comment", in.Comment.ID,
			"classification", out.Classification, "updates", len(out.Updates))
	}
	return out, nil
}

// commentInterpretationPrompt builds the user prompt for comment
// interpretation: the merge request, the comment (with its inline
// position when known), and the tickets with their current content.
func commentInterpretationPrompt(in InterpretCommentInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request !%d: %s\n\n", in.MRNumber, in.MRTitle)
	fmt.Fprintf(&b, "Comment #%d by %s:\n\n%s\n", in.Comment.ID, in.Comment.Author, in.Comment.Body)
	if in.Comment.PositionFile != "" {
		fmt.Fprintf(&b, "\n(The comment was posted on file %s", in.Comment.PositionFile)
		if in.Comment.PositionLine > 0 {
			fmt.Fprintf(&b, ", line %d", in.Comment.PositionLine)
		}
		b.WriteString(".)\n")
	}
	fmt.Fprintf(&b, "\nThe merge request's current tickets (%d):\n\n", len(in.Tickets))
	for _, t := range in.Tickets {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", t.Path, t.Content)
	}
	return b.String()
}

// --- ApplyTicketUpdates: commit updated tickets to the MR branch (AC6) ---

// ApplyTicketUpdatesInput is the input to the ApplyTicketUpdates
// activity.
type ApplyTicketUpdatesInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// MRNumber identifies the merge request (for the commit message).
	MRNumber int64 `json:"mr_number"`
	// Branch is the merge request's head branch: the same refine/*
	// branch the tickets were committed to and the MR is proposing.
	// Updates land on this branch, never a new one.
	Branch string `json:"branch"`
	// CommentID identifies the comment the updates answer (for the
	// commit message).
	CommentID int64 `json:"comment_id"`
	// Updates are the validated ticket rewrites to commit.
	Updates []TicketUpdate `json:"updates"`
}

// ApplyTicketUpdatesResult is the result of the ApplyTicketUpdates
// activity.
type ApplyTicketUpdatesResult struct {
	// UpdatedFiles lists the ticket files written, in update order.
	UpdatedFiles []string `json:"updated_files"`
	// CommitSHA is the commit that carries the updates.
	CommitSHA string `json:"commit_sha,omitempty"`
}

// ApplyTicketUpdates commits the updated tickets to the merge
// request's branch as one commit. It re-fetches each ticket's current
// content from the branch, validates the update against it (the
// frontmatter id must survive; the activity bumps updated_date), and
// commits. It runs as a Temporal activity so the provider API calls
// stay out of the workflow body.
func (a *Activities) ApplyTicketUpdates(ctx context.Context, in ApplyTicketUpdatesInput) (ApplyTicketUpdatesResult, error) {
	if a.provider == nil {
		return ApplyTicketUpdatesResult{}, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if len(in.Updates) == 0 {
		return ApplyTicketUpdatesResult{}, nil
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("applying ticket updates",
			"repository", in.Repository, "branch", in.Branch, "updates", len(in.Updates))
	}

	now := time.Now().Format("2006-01-02 15:04")
	changes := make([]provider.FileChange, 0, len(in.Updates))
	paths := make([]string, 0, len(in.Updates))
	for _, u := range in.Updates {
		current, err := a.provider.GetFile(ctx, in.Repository, u.Path, in.Branch)
		if err != nil {
			return ApplyTicketUpdatesResult{}, fmt.Errorf("fetch ticket %s on %s in %s: %w",
				u.Path, in.Branch, in.Repository, err)
		}
		content, err := updatedTicketContent(string(current.Content), u.Content, now)
		if err != nil {
			return ApplyTicketUpdatesResult{}, fmt.Errorf("update ticket %s: %w", u.Path, err)
		}
		changes = append(changes, provider.FileChange{Path: u.Path, Content: []byte(content)})
		paths = append(paths, u.Path)
	}

	message := fmt.Sprintf("Process feedback on refinement MR !%d (comment %d)", in.MRNumber, in.CommentID)
	commit, err := a.provider.Commit(ctx, in.Repository, in.Branch, message, changes)
	if err != nil {
		return ApplyTicketUpdatesResult{}, fmt.Errorf("commit ticket updates to %s in %s: %w",
			in.Branch, in.Repository, err)
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("applied ticket updates",
			"repository", in.Repository, "branch", in.Branch,
			"commit", ShortSHA(commit.SHA), "files", len(paths))
	}
	return ApplyTicketUpdatesResult{UpdatedFiles: paths, CommitSHA: commit.SHA}, nil
}

// updatedTicketContent validates one ticket rewrite against the
// ticket's current content and returns the content to commit. The
// update must keep the ticket's frontmatter and its id — the agent
// refines the tickets it generated, and a rewrite that lost either is
// a bad interpretation, retried — and the updated_date line is set to
// now. A ticket without frontmatter (not one the agent generated)
// fails the update.
func updatedTicketContent(original, updated, now string) (string, error) {
	_, origOK := frontmatterBlock(original)
	_, updOK := frontmatterBlock(updated)
	if origOK && !updOK {
		return "", errors.New("updated ticket lost the frontmatter block")
	}
	if !origOK {
		return "", errors.New("ticket carries no frontmatter to preserve")
	}
	origID := parseStoryMeta(original).ID
	updID := parseStoryMeta(updated).ID
	if origID == "" {
		return "", errors.New("ticket carries no id to preserve")
	}
	if origID != updID {
		return "", fmt.Errorf("updated ticket changed the id %q to %q", origID, updID)
	}
	content, ok := bumpUpdatedDate(updated, now)
	if !ok {
		return "", errors.New("updated ticket lost the frontmatter block")
	}
	return content, nil
}

// bumpUpdatedDate rewrites the updated_date line inside content's YAML
// frontmatter to now, inserting it before the closing --- when the
// frontmatter carries none. It reports false when content has no
// frontmatter block.
func bumpUpdatedDate(content, now string) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], "\r") != "---" {
		return content, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return content, false
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[0])
	replaced := false
	for _, line := range lines[1:end] {
		if !replaced && strings.HasPrefix(strings.TrimSpace(line), "updated_date:") {
			out = append(out, "updated_date: '"+now+"'")
			replaced = true
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		out = append(out, "updated_date: '"+now+"'")
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), true
}

// --- PostResponseComment: answer on the MR (AC7) ---

// PostResponseCommentInput is the input to the PostResponseComment
// activity.
type PostResponseCommentInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// MRNumber is the merge request to comment on.
	MRNumber int64 `json:"mr_number"`
	// Body is the comment body (markdown), composed by the workflow
	// from the interpretation.
	Body string `json:"body"`
}

// PostResponseCommentResult is the result of the PostResponseComment
// activity.
type PostResponseCommentResult struct {
	// CommentID is the response comment's id on the provider.
	CommentID int64 `json:"comment_id"`
}

// PostResponseComment posts the agent's response comment on the merge
// request. It runs as a Temporal activity so the provider API call
// stays out of the workflow body.
func (a *Activities) PostResponseComment(ctx context.Context, in PostResponseCommentInput) (PostResponseCommentResult, error) {
	if a.provider == nil {
		return PostResponseCommentResult{}, fmt.Errorf("userstoryreview: no provider client configured")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("posting response comment",
			"repository", in.Repository, "mr", in.MRNumber)
	}

	comment, err := a.provider.AddMergeRequestComment(ctx, in.Repository, in.MRNumber, in.Body)
	if err != nil {
		return PostResponseCommentResult{}, fmt.Errorf("comment on merge request %d in %s: %w",
			in.MRNumber, in.Repository, err)
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("posted response comment",
			"repository", in.Repository, "mr", in.MRNumber, "comment", comment.ID)
	}
	return PostResponseCommentResult{CommentID: comment.ID}, nil
}
