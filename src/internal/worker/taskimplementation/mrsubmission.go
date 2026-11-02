package taskimplementation

import (
	"context"
	"fmt"
	"strings"

	"go.temporal.io/sdk/activity"

	"minas-tirith/internal/worker/backlog"
)

// This file holds the implementation agent's merge request submission:
// the SubmitMergeRequest activity that opens the MR carrying one
// implementation and, for a partial implementation, hands the task to
// the refiner agent on it. Every provider API call happens here, never in
// the workflow body, so the workflow stays deterministic.

// SubmitMergeRequestInput is the input to the SubmitMergeRequest
// activity.
type SubmitMergeRequestInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Implementation is the implementation the MR carries: its branch,
	// outcome, changed files, reason, and open questions.
	Implementation TaskImplementation `json:"implementation"`
	// Base is the branch the task commit landed on: the MR's target.
	Base string `json:"base"`
	// CommitSHA, CommitMessage, and CommitAuthor describe the triggering
	// commit, for the MR description.
	CommitSHA     string `json:"commit_sha"`
	CommitMessage string `json:"commit_message,omitempty"`
	CommitAuthor  string `json:"commit_author,omitempty"`
	// ProviderURL is the provider's web base URL, used to link the task
	// file in the description. Optional.
	ProviderURL string `json:"provider_url,omitempty"`
}

// SubmitMergeRequestResult identifies the merge request the activity
// opened and the refiner hand-off it posted.
type SubmitMergeRequestResult struct {
	// Number identifies the merge request within the repository.
	Number int64 `json:"number"`
	// Title is the merge request title.
	Title string `json:"title"`
	// URL is the merge request's page on the provider.
	URL string `json:"url"`
	// Draft reports whether the merge request is a draft.
	Draft bool `json:"draft"`
	// RefinerCommentID is the refiner hand-off comment's id; zero when
	// no hand-off was posted (a complete implementation).
	RefinerCommentID int64 `json:"refiner_comment_id,omitempty"`
}

// SubmitMergeRequest opens the merge request carrying one implementation:
// a regular MR when the implementation is complete, a draft MR when it is
// partial. A partial implementation is then handed to the refiner agent
// by tagging it on the draft MR, which starts a refinement round through
// the refinement agent's merge request comment loop; a complete
// implementation never involves the refiner. It runs as a Temporal
// activity so the provider API calls stay out of the workflow body.
func (a *Activities) SubmitMergeRequest(ctx context.Context, in SubmitMergeRequestInput) (SubmitMergeRequestResult, error) {
	if err := a.requireProvider("SubmitMergeRequest"); err != nil {
		return SubmitMergeRequestResult{}, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return SubmitMergeRequestResult{}, err
	}
	impl := in.Implementation
	if !isImplementationBranch(impl.Branch) {
		return SubmitMergeRequestResult{}, fmt.Errorf(
			"submit merge request: branch %q is not an implementation branch (%s*) this agent owns",
			impl.Branch, backlog.ImplementationBranchPrefix)
	}
	if strings.TrimSpace(in.Base) == "" {
		return SubmitMergeRequestResult{}, fmt.Errorf("submit merge request: base branch is required")
	}
	if backlog.IsAgentBranch(in.Base) {
		return SubmitMergeRequestResult{}, fmt.Errorf(
			"submit merge request: base branch %q is an agent branch; the MR must target the branch the task commit landed on",
			in.Base)
	}

	content := MergeRequestContent{
		Implementation: impl,
		Repository:     in.Repository,
		Branch:         in.Base,
		CommitSHA:      in.CommitSHA,
		CommitMessage:  in.CommitMessage,
		CommitAuthor:   in.CommitAuthor,
		ProviderURL:    in.ProviderURL,
	}
	draft := !impl.Complete
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		outcome := "complete"
		if draft {
			outcome = "partial"
		}
		logger.Info("opening implementation merge request",
			"repository", in.Repository, "task", impl.TaskID,
			"head", impl.Branch, "base", in.Base, "outcome", outcome, "draft", draft)
	}

	mr, err := a.provider.CreateMergeRequest(ctx, in.Repository,
		buildMergeRequestTitle(content), buildMergeRequestDescription(content),
		impl.Branch, in.Base, draft)
	if err != nil {
		return SubmitMergeRequestResult{}, fmt.Errorf("open merge request %s -> %s in %s: %w",
			impl.Branch, in.Base, in.Repository, err)
	}

	result := SubmitMergeRequestResult{
		Number: mr.Number,
		Title:  mr.Title,
		URL:    mr.URL,
		Draft:  mr.Draft,
	}

	// The refiner hand-off: a partial implementation is routed back to
	// the refinement agent with the open questions, so the task does not
	// end as a dead end. A complete one never involves the refiner.
	if handOff := buildRefinerHandOff(content); handOff != "" {
		comment, err := a.provider.AddMergeRequestComment(ctx, in.Repository, mr.Number, handOff)
		if err != nil {
			return SubmitMergeRequestResult{}, fmt.Errorf("hand off merge request %d to the refiner in %s: %w",
				mr.Number, in.Repository, err)
		}
		result.RefinerCommentID = comment.ID
	}

	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("implementation merge request opened",
			"repository", in.Repository, "task", impl.TaskID,
			"mr_number", mr.Number, "draft", result.Draft,
			"refiner_comment", result.RefinerCommentID)
	}
	return result, nil
}
