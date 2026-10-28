// This file implements the MR comment loop of the user story review
// agent (step 4 of the TASK-2 flow): the user adjusts a refinement
// through comments on the merge request the agent opened, and the
// agent applies that feedback to the refinement tickets.

package userstoryreview

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/event"
)

// MrCommentWorkflowName is the Temporal workflow type name for the
// merge request comment workflow: the one comment events start.
const MrCommentWorkflowName = "mrCommentWorkflow"

// MrCommentProcessorWorkflowName is the Temporal workflow type name
// for the per-merge-request comment processor. It is an internal detail
// of the comment loop (a child workflow; see MrCommentWorkflow), not
// started by events directly.
const MrCommentProcessorWorkflowName = "mrCommentProcessorWorkflow"

// Activity type names for the comment loop.
const (
	// FindRefinementName is the Temporal activity type name for the
	// activity that correlates a comment to the refinement run that
	// created its merge request.
	FindRefinementName = "userStoryReview.findRefinement"
	// CollectTicketsName is the Temporal activity type name for the
	// activity that collects a refinement merge request's ticket files
	// with their current content.
	CollectTicketsName = "userStoryReview.collectTickets"
	// InterpretCommentName is the Temporal activity type name for the
	// activity that interprets a comment with the LLM.
	InterpretCommentName = "userStoryReview.interpretComment"
	// ApplyTicketUpdatesName is the Temporal activity type name for the
	// activity that commits updated tickets to the merge request's
	// branch.
	ApplyTicketUpdatesName = "userStoryReview.applyTicketUpdates"
	// PostResponseCommentName is the Temporal activity type name for
	// the activity that posts the agent's response comment.
	PostResponseCommentName = "userStoryReview.postResponseComment"
)

// RefinementTitlePrefix prefixes the title of every refinement merge
// request the agent opens (see buildMRTitle). The comment trigger
// filters on it; findRefinement re-verifies through the provider.
const RefinementTitlePrefix = "Refine "

// AgentResponseMarker prefixes every comment the refinement agent
// posts on a merge request. The comment trigger declines events whose
// body carries it, so the agent never picks up its own responses, and
// findRefinement recognizes earlier responses through the comment id
// the marker references.
const AgentResponseMarker = "<!-- minas-tirith: refinement-agent"

// Comment classifications: how the agent reads one comment.
const (
	// ClassificationInline marks feedback on specific ticket file(s).
	ClassificationInline = "inline"
	// ClassificationSummary marks feedback on the overall refinement.
	ClassificationSummary = "summary"
	// ClassificationNone marks a comment with no actionable ticket
	// feedback (greetings, thanks, questions).
	ClassificationNone = "none"
)

// MrCommentInput is the merge request comment payload the comment
// workflows receive: which repository and merge request the comment
// was posted on, the comment itself, and its inline position when the
// provider reported one.
type MrCommentInput struct {
	Provider      string `json:"provider"`   // event source provider, e.g. "forgejo"
	Project       string `json:"project"`    // watched project name, e.g. "BeanieGenie"
	Repository    string `json:"repository"` // provider full repo name, e.g. "root/BeanieGenie"
	MRNumber      int64  `json:"mr_number"`  // merge request the comment is on
	MRTitle       string `json:"mr_title,omitempty"`
	CommentID     int64  `json:"comment_id"` // the comment's id on the provider
	CommentBody   string `json:"comment_body"`
	CommentAuthor string `json:"comment_author,omitempty"`
	// PositionFile and PositionLine carry the comment's inline location
	// when the provider reported one (a comment on a specific file and
	// line). Forgejo webhooks do not deliver inline comment locations
	// today, so these are usually empty and inline-ness is interpreted
	// from the comment body instead.
	PositionFile string `json:"position_file,omitempty"`
	PositionLine int    `json:"position_line,omitempty"`
}

// Validate reports whether the payload identifies a processable comment.
func (in MrCommentInput) Validate() error {
	if strings.TrimSpace(in.Repository) == "" {
		return errors.New("repository is required")
	}
	if in.MRNumber <= 0 {
		return errors.New("merge request number is required")
	}
	if in.CommentID <= 0 {
		return errors.New("comment id is required")
	}
	if strings.TrimSpace(in.CommentBody) == "" {
		return errors.New("comment body is required")
	}
	return nil
}

// MrCommentResult is the outcome of one merge request comment run.
type MrCommentResult struct {
	// Processed reports that the comment was handled: interpreted,
	// ticket updates applied, and a response posted.
	Processed bool `json:"processed"`
	// Status summarizes what the run did.
	Status string `json:"status"`
	// Classification is how the comment was interpreted: inline,
	// summary, or none (empty when the run did not interpret it).
	Classification string `json:"classification,omitempty"`
	// UpdatedFiles lists the ticket files the run rewrote.
	UpdatedFiles []string `json:"updated_files,omitempty"`
	// CommitSHA is the commit that carried the updated tickets onto the
	// merge request's branch.
	CommitSHA string `json:"commit_sha,omitempty"`
	// ResponsePosted reports whether the response comment went onto the
	// merge request.
	ResponsePosted bool `json:"response_posted"`
	// AlreadyProcessed reports that an earlier run already answered this
	// comment (the merge request carries the agent's response to it), so
	// this run did nothing.
	AlreadyProcessed bool `json:"already_processed,omitempty"`

	// Correlation of the comment back to the refinement run that
	// created the merge request. The review workflow's result records
	// every merge request it opened (Submission.MRNumber, TASK-8); the
	// merge request's refine/* head branch is the same branch that run
	// committed the tickets to, and its name embeds the short SHA of
	// the commit that run reviewed — which identifies the review
	// workflow execution (the event bridge derives its workflow ID as
	// WorkflowName + "-" + provider + "|" + repository + "|commit|" +
	// commit SHA).
	MRNumber                   int64  `json:"mr_number"`
	RefinementBranch           string `json:"refinement_branch,omitempty"`
	BaseBranch                 string `json:"base_branch,omitempty"`
	OriginCommitShortSHA       string `json:"origin_commit_short_sha,omitempty"`
	RefinementWorkflowIDPrefix string `json:"refinement_workflow_id_prefix,omitempty"`
}

// MrCommentInputFromEvent builds the comment workflow input from a
// dispatched comment event. It reports false for events that cannot
// start the comment loop:
//
//   - non-comment events and comments without a merge request payload;
//   - comments on merge requests the refinement agent did not create —
//     every refinement MR is titled "Refine ..." (buildMRTitle), so
//     any other title is a user's own MR and out of scope (the cheap,
//     trigger-side half of the agent-MR filter; findRefinement
//     re-verifies through the provider before any ticket is touched);
//   - comments carrying the agent response marker: the agent's own
//     responses, which the loop must never pick up again.
func MrCommentInputFromEvent(ev event.Event) (any, bool) {
	if ev.Kind != event.KindComment || ev.Comment == nil || ev.Comment.MR == nil {
		return nil, false
	}
	c := ev.Comment
	if c.MR.Number <= 0 || c.ID <= 0 || strings.TrimSpace(c.Body) == "" {
		return nil, false
	}
	if !IsRefinementTitle(c.MR.Title) {
		return nil, false
	}
	if IsAgentResponse(c.Body) {
		return nil, false
	}
	in := MrCommentInput{
		Provider:      ev.Provider,
		Project:       ev.Project,
		Repository:    ev.Repository,
		MRNumber:      c.MR.Number,
		MRTitle:       c.MR.Title,
		CommentID:     c.ID,
		CommentBody:   c.Body,
		CommentAuthor: authorName(c.Author),
	}
	if c.Position != nil {
		in.PositionFile = c.Position.File
		in.PositionLine = c.Position.Line
	}
	return in, true
}

// IsRefinementTitle reports whether a merge request title matches the
// refinement agent's title pattern: every MR the agent opens is titled
// "Refine ..." (see buildMRTitle).
func IsRefinementTitle(title string) bool {
	return strings.HasPrefix(strings.TrimSpace(title), RefinementTitlePrefix)
}

// IsAgentResponse reports whether a comment body is (carries) the
// refinement agent's own response — see AgentResponseMarker.
func IsAgentResponse(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), AgentResponseMarker)
}

// responseMarkerFor returns the marker line the agent's response to
// comment id carries, so a response can be matched to the comment it
// answers.
func responseMarkerFor(commentID int64) string {
	return fmt.Sprintf("%s response to comment %d -->", AgentResponseMarker, commentID)
}

// MrCommentWorkflow responds to one user comment on a refinement merge
// request the agent opened (step 4 of the TASK-2 flow): the comment is
// interpreted, the refinement tickets are updated accordingly, the
// updates are committed to the merge request's branch, and a response
// comment summarizes what changed and why.
//
// Comments on the same merge request must never be applied
// concurrently: two runs rewriting the same branch would overwrite each
// other's updates. The workflow therefore does its work in a child
// workflow whose ID is derived from the merge request — Temporal
// starts only one execution per workflow ID at a time — so comments on
// one MR are applied strictly one after another while comments on
// different MRs still run in parallel. A comment arriving while its
// MR's previous comment is still processing waits for the slot
// (acquireMrCommentSlot) and runs as soon as the slot frees.
func MrCommentWorkflow(ctx workflow.Context, in MrCommentInput) (MrCommentResult, error) {
	if err := in.Validate(); err != nil {
		return MrCommentResult{}, fmt.Errorf("invalid %s input: %w", MrCommentWorkflowName, err)
	}
	logger := workflow.GetLogger(ctx)
	logger.Info("mr comment received",
		"repository", in.Repository, "mr", in.MRNumber,
		"comment", in.CommentID, "author", in.CommentAuthor)

	child, err := acquireMrCommentSlot(ctx, in)
	if err != nil {
		return MrCommentResult{}, err
	}
	var result MrCommentResult
	if err := child.Get(ctx, &result); err != nil {
		return MrCommentResult{}, fmt.Errorf("process comment %d on mr %d: %w",
			in.CommentID, in.MRNumber, err)
	}
	return result, nil
}

// Slot acquisition: the per-MR serialization of comment handling.

const (
	// mrCommentSlotInitialDelay is the wait before re-attempting to
	// acquire a busy merge request's processing slot.
	mrCommentSlotInitialDelay = 10 * time.Second
	// mrCommentSlotMaxDelay caps the acquisition backoff.
	mrCommentSlotMaxDelay = 2 * time.Minute
	// mrCommentSlotWaitLimit bounds how long a comment workflow waits
	// for its merge request's slot before failing, so a wedged processor
	// cannot pile up waiting comment workflows forever. (The comment
	// event can be redelivered — the workflow start deduplicates on the
	// event identity and re-runs failed workflows.)
	mrCommentSlotWaitLimit = time.Hour
	// mrCommentProcessorTimeout bounds one comment's processing run.
	mrCommentProcessorTimeout = 2 * time.Hour
)

// mrCommentProcessorID returns the comment processor workflow ID for
// one merge request: MrCommentProcessorWorkflowName plus provider,
// repository, and MR number. One execution with this ID runs at a
// time; the ALLOW_DUPLICATE reuse policy lets the next comment's
// processor start once the previous one completed.
func mrCommentProcessorID(in MrCommentInput) string {
	return fmt.Sprintf("%s-%s|%s|mr|%d",
		MrCommentProcessorWorkflowName, in.Provider, in.Repository, in.MRNumber)
}

// acquireMrCommentSlot starts the per-MR comment processor for this
// comment and returns its future. While another comment on the same MR
// is still being processed (its processor execution is running), the
// start is rejected with WorkflowExecutionAlreadyStarted; this loop
// waits with backoff and re-attempts until the slot frees, so comments
// on one MR are handled sequentially.
func acquireMrCommentSlot(ctx workflow.Context, in MrCommentInput) (workflow.ChildWorkflowFuture, error) {
	return acquireChildWorkflowSlot(ctx, mrCommentProcessorID(in), MrCommentProcessorWorkflow, in)
}

// acquireChildWorkflowSlot starts a child workflow with the given ID
// and input, retrying with backoff while another execution with that
// ID is running, until it starts. It fails once the wait exceeds
// mrCommentSlotWaitLimit.
func acquireChildWorkflowSlot(ctx workflow.Context, workflowID string, fn any, input any) (workflow.ChildWorkflowFuture, error) {
	logger := workflow.GetLogger(ctx)
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:                workflowID,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionTimeout: mrCommentProcessorTimeout,
	})
	deadline := workflow.Now(ctx).Add(mrCommentSlotWaitLimit)
	delay := mrCommentSlotInitialDelay
	for {
		child := workflow.ExecuteChildWorkflow(childCtx, fn, input)
		var execution workflow.Execution
		err := child.GetChildWorkflowExecution().Get(ctx, &execution)
		if err == nil {
			logger.Info("mr comment slot acquired",
				"child_workflow_id", workflowID, "run_id", execution.RunID)
			return child, nil
		}
		if !temporal.IsWorkflowExecutionAlreadyStartedError(err) {
			return nil, fmt.Errorf("start %s (id %s): %w", MrCommentProcessorWorkflowName, workflowID, err)
		}
		if workflow.Now(ctx).After(deadline) {
			return nil, fmt.Errorf("mr comment processing slot %s busy for more than %s; giving up",
				workflowID, mrCommentSlotWaitLimit)
		}
		logger.Info("mr comment slot busy; waiting for the running processor",
			"child_workflow_id", workflowID, "retry_in", delay)
		if err := workflow.Sleep(ctx, delay); err != nil {
			return nil, fmt.Errorf("waiting for %s: %w", workflowID, err)
		}
		delay *= 2
		if delay > mrCommentSlotMaxDelay {
			delay = mrCommentSlotMaxDelay
		}
	}
}

// MrCommentProcessorWorkflow processes one comment on one refinement
// merge request. It runs as a child workflow whose ID is per merge
// request (see acquireMrCommentSlot), which serializes comments on the
// same MR. All provider and LLM API calls run inside activities, so
// the workflow body stays deterministic.
func MrCommentProcessorWorkflow(ctx workflow.Context, in MrCommentInput) (MrCommentResult, error) {
	if err := in.Validate(); err != nil {
		return MrCommentResult{}, fmt.Errorf("invalid %s input: %w", MrCommentProcessorWorkflowName, err)
	}
	logger := workflow.GetLogger(ctx)
	logger.Info("processing mr comment",
		"repository", in.Repository, "mr", in.MRNumber, "comment", in.CommentID)

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    10 * time.Minute,
		ScheduleToCloseTimeout: 30 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Minute,
			MaximumAttempts:    5,
		},
	})

	// Correlate the comment to the refinement run that created the MR:
	// the MR number identifies the Submission the review workflow
	// recorded, and the MR's head branch carries the tickets.
	var found FindRefinementOutput
	if err := workflow.ExecuteActivity(ctx, FindRefinementName, FindRefinementInput{
		Provider:   in.Provider,
		Repository: in.Repository,
		MRNumber:   in.MRNumber,
		CommentID:  in.CommentID,
	}).Get(ctx, &found); err != nil {
		return MrCommentResult{}, fmt.Errorf("find refinement for mr %d: %w", in.MRNumber, err)
	}

	result := MrCommentResult{
		MRNumber:                   in.MRNumber,
		RefinementBranch:           found.RefinementBranch,
		BaseBranch:                 found.BaseBranch,
		OriginCommitShortSHA:       found.OriginCommitShortSHA,
		RefinementWorkflowIDPrefix: found.RefinementWorkflowIDPrefix,
	}

	if !found.IsRefinement {
		// The MR title matched the refinement pattern but the MR is not
		// one of the agent's: nothing to do, and not a failure.
		logger.Info("comment is not on an agent refinement merge request; ignoring",
			"repository", in.Repository, "mr", in.MRNumber)
		result.Status = "merge request is not a refinement created by the agent"
		return result, nil
	}
	if found.AlreadyProcessed {
		logger.Info("comment already answered by an earlier run; ignoring",
			"repository", in.Repository, "mr", in.MRNumber, "comment", in.CommentID)
		result.AlreadyProcessed = true
		result.Status = "comment already answered by an earlier run"
		return result, nil
	}

	// The MR's tickets as they exist right now: their current content
	// is what the interpretation updates.
	var tickets []RefinementTicket
	if err := workflow.ExecuteActivity(ctx, CollectTicketsName, CollectTicketsInput{
		Repository:       in.Repository,
		RefinementBranch: found.RefinementBranch,
		BaseBranch:       found.BaseBranch,
	}).Get(ctx, &tickets); err != nil {
		return result, fmt.Errorf("collect tickets from %s: %w", found.RefinementBranch, err)
	}

	// Interpret the comment against those tickets with the LLM.
	var interpretation CommentInterpretation
	if err := workflow.ExecuteActivity(ctx, InterpretCommentName, InterpretCommentInput{
		Repository: in.Repository,
		MRNumber:   in.MRNumber,
		MRTitle:    in.MRTitle,
		Comment: CommentFeedback{
			ID:           in.CommentID,
			Body:         in.CommentBody,
			Author:       in.CommentAuthor,
			PositionFile: in.PositionFile,
			PositionLine: in.PositionLine,
		},
		Tickets: tickets,
	}).Get(ctx, &interpretation); err != nil {
		return result, fmt.Errorf("interpret comment %d: %w", in.CommentID, err)
	}
	result.Classification = interpretation.Classification

	// Deterministic validation before anything is committed: the
	// interpretation may only rewrite ticket files the MR carries, and
	// a comment with a reported inline position on one of those
	// tickets may only update that ticket.
	updates, err := validatedUpdates(interpretation, tickets, in)
	if err != nil {
		return result, fmt.Errorf("interpret comment %d: %w", in.CommentID, err)
	}

	if len(updates) > 0 {
		// Commit the updated tickets to the merge request's branch —
		// the same refine/* branch the MR is proposing, never a new
		// one.
		var applied ApplyTicketUpdatesResult
		if err := workflow.ExecuteActivity(ctx, ApplyTicketUpdatesName, ApplyTicketUpdatesInput{
			Repository: in.Repository,
			MRNumber:   in.MRNumber,
			Branch:     found.RefinementBranch,
			CommentID:  in.CommentID,
			Updates:   updates,
		}).Get(ctx, &applied); err != nil {
			return result, fmt.Errorf("apply ticket updates to %s: %w", found.RefinementBranch, err)
		}
		result.UpdatedFiles = applied.UpdatedFiles
		result.CommitSHA = applied.CommitSHA
	}

	// Respond on the MR with what changed and why.
	var posted PostResponseCommentResult
	if err := workflow.ExecuteActivity(ctx, PostResponseCommentName, PostResponseCommentInput{
		Repository: in.Repository,
		MRNumber:   in.MRNumber,
		Body:       buildResponseComment(in, interpretation, updates, result),
	}).Get(ctx, &posted); err != nil {
		return result, fmt.Errorf("post response comment on mr %d: %w", in.MRNumber, err)
	}

	result.Processed = true
	result.ResponsePosted = true
	result.Status = classificationStatus(interpretation.Classification, len(updates))
	logger.Info("mr comment processed",
		"repository", in.Repository, "mr", in.MRNumber, "comment", in.CommentID,
		"classification", interpretation.Classification, "updated_files", len(updates),
		"commit", ShortSHA(result.CommitSHA))
	return result, nil
}

// validatedUpdates checks the LLM's proposed ticket updates against
// what the workflow knows. It fails the run (retryable through
// Temporal) when the interpretation wants to rewrite a file that is
// not one of the MR's tickets, update the same ticket twice, or empty
// a ticket; a comment with a reported inline position on one of the
// tickets may only update that ticket; a non-actionable comment
// updates nothing.
func validatedUpdates(interpretation CommentInterpretation, tickets []RefinementTicket, in MrCommentInput) ([]TicketUpdate, error) {
	switch interpretation.Classification {
	case ClassificationNone:
		return nil, nil
	case ClassificationInline, ClassificationSummary:
	default:
		return nil, fmt.Errorf("unknown comment classification %q", interpretation.Classification)
	}
	if len(interpretation.Updates) == 0 {
		return nil, nil
	}

	ticketPaths := make(map[string]bool, len(tickets))
	for _, t := range tickets {
		ticketPaths[t.Path] = true
	}

	var updates []TicketUpdate
	seen := make(map[string]bool, len(interpretation.Updates))
	for _, u := range interpretation.Updates {
		if !ticketPaths[u.Path] {
			return nil, fmt.Errorf("update for %s is not a ticket of this merge request", u.Path)
		}
		if seen[u.Path] {
			return nil, fmt.Errorf("duplicate update for %s", u.Path)
		}
		if strings.TrimSpace(u.Content) == "" {
			return nil, fmt.Errorf("update for %s is empty", u.Path)
		}
		if ticketPaths[in.PositionFile] && u.Path != in.PositionFile {
			return nil, fmt.Errorf("comment is positioned on %s but the interpretation updates %s",
				in.PositionFile, u.Path)
		}
		seen[u.Path] = true
		updates = append(updates, u)
	}
	return updates, nil
}

// classificationStatus summarizes one processed comment.
func classificationStatus(classification string, updated int) string {
	switch classification {
	case ClassificationInline:
		return fmt.Sprintf("inline feedback applied to %d ticket(s)", updated)
	case ClassificationSummary:
		return fmt.Sprintf("summary feedback applied to %d ticket(s)", updated)
	default:
		return "no ticket changes needed"
	}
}

// buildResponseComment composes the comment the agent posts in answer
// to one user comment: the marker line (so the trigger declines the
// agent's own comments and later runs can match responses to the
// comments they answer), the LLM's acknowledging prose, the per-ticket
// changes, and the commit that carried them.
func buildResponseComment(in MrCommentInput, interpretation CommentInterpretation, updates []TicketUpdate, res MrCommentResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", responseMarkerFor(in.CommentID))

	intro := "Thanks for the feedback."
	if in.CommentAuthor != "" {
		intro = fmt.Sprintf("Thanks for the feedback, **@%s**.", in.CommentAuthor)
	}
	switch interpretation.Classification {
	case ClassificationInline:
		intro += " Your comment reads as feedback on specific tickets, so I updated the ticket files it targets."
	case ClassificationSummary:
		intro += " Your comment reads as feedback on the overall refinement, so I updated the tickets accordingly."
	case ClassificationNone:
		intro += " I did not read your comment as ticket feedback, so I changed nothing."
	}
	fmt.Fprintf(&b, "%s\n\n", intro)

	if strings.TrimSpace(interpretation.Response) != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(interpretation.Response))
	}

	if len(updates) > 0 {
		b.WriteString("## Ticket changes\n\n")
		for _, u := range updates {
			summary := strings.TrimSpace(u.ChangeSummary)
			if summary == "" {
				summary = "Updated."
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", u.Path, summary)
		}
		fmt.Fprintf(&b, "\nCommitted to `%s` (this merge request's branch) as `%s`.\n",
			res.RefinementBranch, ShortSHA(res.CommitSHA))
	} else {
		b.WriteString("No ticket changes were needed.\n")
	}
	return b.String()
}

// refinementShortSHA extracts the originating commit's short SHA from a
// refinement branch name. RefinementBranchName builds
// refine/<story-slug>-<short-sha>, so the segment after the last dash
// is the SHA prefix — when it parses as one (hex, git's 7 to 12 short
// SHA characters); anything else yields "".
func refinementShortSHA(branch string) string {
	rest, ok := strings.CutPrefix(branch, RefinementBranchPrefix)
	if !ok {
		return ""
	}
	dash := strings.LastIndex(rest, "-")
	if dash < 0 {
		return ""
	}
	sha := rest[dash+1:]
	if len(sha) < 7 || len(sha) > 12 {
		return ""
	}
	for _, r := range sha {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return ""
		}
	}
	return sha
}

// refinementWorkflowIDPrefix reconstructs the identity of the review
// workflow execution that created the refinement: the event bridge
// derives workflow IDs as <workflow>-<provider>|<repository>|commit|<sha>,
// and the refinement branch name embeds the short SHA of the commit
// that execution reviewed, so its workflow ID begins with this prefix.
func refinementWorkflowIDPrefix(provider, repository, shortSHA string) string {
	if shortSHA == "" {
		return ""
	}
	return fmt.Sprintf("%s-%s|%s|%s", WorkflowName, provider, repository, shortSHA)
}
