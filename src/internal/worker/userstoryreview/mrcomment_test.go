// Tests for the MR comment loop (mrcomment.go, mrcomment_activities.go):
// the trigger filter for agent-created merge requests, the correlation of
// comments to the refinement run that created the MR, the LLM
// interpretation of inline and summary comments, the update validation,
// and the workflows' happy path, error handling, and sequential per-MR
// processing.

package userstoryreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/event"
	"minas-tirith/internal/llm"
	"minas-tirith/internal/provider"
)

// The refinement scenario the comment-loop tests arrange on the fake
// provider: the story on main, two ticket files on the refine branch,
// and the refinement MR proposing them. The branch name embeds the
// short SHA of the commit the originating review workflow ran on.
const (
	mrRepo         = "root/BeanieGenie"
	mrNumber       = int64(1)
	mrTitle        = "Refine TASK-001: Add user login"
	refineBranch   = "refine/task-1-8ef0de217a18"
	originShortSHA = "8ef0de217a18"
	storyPath      = "backlog/tasks/task-1.md"
	ticket2Path    = "backlog/tasks/task-002 - Add-login-endpoint.md"
	ticket3Path    = "backlog/tasks/task-003 - Add-login-tests.md"
)

// Ticket bodies before and after the feedback the tests apply.
const (
	ticket2Body = "## Context\nNeed auth.\n\n## What needs to happen\nAdd POST /login returning a JWT.\n\n## Acceptance Criteria\n- [ ] POST /login returns 200 with JWT"
	ticket2MFA  = "## Context\nNeed auth.\n\n## What needs to happen\nAdd POST /login returning a JWT. Require MFA.\n\n## Acceptance Criteria\n- [ ] POST /login returns 200 with JWT\n- [ ] Login requires MFA"
	// ticket2MFAJWT extends ticket2MFA (not ticket2Body): a second comment
	// on the same ticket is interpreted against the content the first
	// comment's commit wrote.
	ticket2MFAJWT = "## Context\nNeed auth.\n\n## What needs to happen\nAdd POST /login returning a JWT. Require MFA. Document the 24 hour JWT lifetime.\n\n## Acceptance Criteria\n- [ ] POST /login returns 200 with JWT\n- [ ] Login requires MFA\n- [ ] JWT lifetime is documented"
	ticket3Body   = "## Context\nCoverage for login.\n\n## What needs to happen\nAdd unit tests for POST /login.\n\n## Acceptance Criteria\n- [ ] All tests pass"
	ticket3Rate   = "## Context\nCoverage for login.\n\n## What needs to happen\nAdd unit tests for POST /login.\n\n## Acceptance Criteria\n- [ ] All tests pass\n- [ ] Tests cover rate limiting"
)

// ticketFile renders one ticket file the way CommitTicket commits them,
// so updates validate against realistic content.
func ticketFile(id int, title, body string) string {
	return renderTicketFile(id, Ticket{Title: title, Description: body}, "2026-10-01 10:00")
}

// seedRefinement arranges the refinement scenario on the fake provider.
func seedRefinement(pf *provider.Fake) error {
	pf.SeedFiles(mrRepo, "main", map[string]string{
		storyPath: "---\nid: TASK-001\ntitle: Add user login\n---\n\n## Description\n\nAdd user login with JWT.\n",
	})
	pf.SeedFiles(mrRepo, refineBranch, map[string]string{
		storyPath:   "---\nid: TASK-001\ntitle: Add user login\n---\n\n## Description\n\nAdd user login with JWT.\n",
		ticket2Path: ticketFile(2, "Add login endpoint", ticket2Body),
		ticket3Path: ticketFile(3, "Add login tests", ticket3Body),
	})
	_, err := pf.CreateMergeRequest(context.Background(), mrRepo, mrTitle, "refinement description", refineBranch, "main")
	return err
}

// newRefinementFake returns a fake provider carrying the refinement
// scenario.
func newRefinementFake(t *testing.T) *provider.Fake {
	t.Helper()
	pf := provider.NewFake(mrRepo)
	if err := seedRefinement(pf); err != nil {
		t.Fatalf("seed refinement scenario: %v", err)
	}
	return pf
}

// commentInput returns the workflow input for one comment on the
// refinement MR.
func commentInput(id int64, body string) MrCommentInput {
	return MrCommentInput{
		Provider:      "forgejo",
		Project:       "BeanieGenie",
		Repository:    mrRepo,
		MRNumber:      mrNumber,
		MRTitle:       mrTitle,
		CommentID:     id,
		CommentBody:   body,
		CommentAuthor: "root",
	}
}

// commentEvent returns a dispatched comment event on the refinement MR.
func commentEvent(title, body string) event.Event {
	return event.Event{
		Kind:       event.KindComment,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: mrRepo,
		Comment: &event.Comment{
			ID:     10,
			Body:   body,
			Author: event.Person{Username: "root"},
			MR:     &event.MergeRequest{Number: mrNumber, Title: title},
		},
	}
}

// interpretReply scripts the LLM's interpretation of one comment.
func interpretReply(classification, response string, updates ...TicketUpdate) llm.Response {
	b, err := json.Marshal(CommentInterpretation{
		Classification: classification,
		Response:       response,
		Updates:        updates,
	})
	if err != nil {
		panic(err)
	}
	return llm.Response{Content: string(b), Model: "test-model"}
}

// --- Trigger filtering and input (AC1, AC2) ---

func TestMrCommentInputFromEvent(t *testing.T) {
	// A comment on a refinement-titled MR yields workflow input, with
	// the comment's inline position when the provider reported one.
	ev := commentEvent(mrTitle, "Please require MFA.")
	ev.Comment.Position = &event.CommentPosition{File: ticket2Path, Line: 12}
	in, ok := MrCommentInputFromEvent(ev)
	if !ok {
		t.Fatal("MrCommentInputFromEvent declined a comment on a refinement MR")
	}
	want := MrCommentInput{
		Provider:      "forgejo",
		Project:       "BeanieGenie",
		Repository:    mrRepo,
		MRNumber:      mrNumber,
		MRTitle:       mrTitle,
		CommentID:     10,
		CommentBody:   "Please require MFA.",
		CommentAuthor: "root",
		PositionFile:  ticket2Path,
		PositionLine:  12,
	}
	if in != want {
		t.Errorf("input = %+v, want %+v", in, want)
	}
}

func TestMrCommentInputFromEventDeclines(t *testing.T) {
	tests := []struct {
		name string
		ev   event.Event
	}{
		{"commit event", event.Event{Kind: event.KindCommit, Provider: "forgejo", Repository: mrRepo,
			Commit: &event.Commit{SHA: "8ef0de217a18"}}},
		{"comment without payload", event.Event{Kind: event.KindComment, Provider: "forgejo", Repository: mrRepo}},
		{"comment without merge request", event.Event{Kind: event.KindComment, Provider: "forgejo", Repository: mrRepo,
			Comment: &event.Comment{ID: 10, Body: "hi"}}},
		{"merge request without number", func() event.Event {
			ev := commentEvent(mrTitle, "hi")
			ev.Comment.MR = &event.MergeRequest{Title: mrTitle}
			return ev
		}()},
		{"comment without id", func() event.Event {
			ev := commentEvent(mrTitle, "hi")
			ev.Comment.ID = 0
			return ev
		}()},
		{"comment without body", func() event.Event {
			ev := commentEvent(mrTitle, "   ")
			return ev
		}()},
		{"merge request the agent did not create", commentEvent("Add login screen", "hi")},
		{"the agent's own response", commentEvent(mrTitle, responseMarkerFor(10)+"\n\nThanks for the feedback.")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if in, ok := MrCommentInputFromEvent(tt.ev); ok {
				t.Errorf("MrCommentInputFromEvent(%+v) = %+v, want declined", tt.ev, in)
			}
		})
	}
}

func TestMrCommentInputFromEventReviewEvent(t *testing.T) {
	// A review-submission event (Forgejo pull_request_comment with action
	// "reviewed") carries no comment and possibly an empty review body;
	// it still passes, flagged as a review event, so the workflow can
	// resolve the review's actual comments through the provider API.
	ev := commentEvent(mrTitle, "")
	ev.Comment.Review = true
	ev.Comment.ID = 424242 // synthetic stand-in from the adapter
	in, ok := MrCommentInputFromEvent(ev)
	if !ok {
		t.Fatal("MrCommentInputFromEvent declined a review event with an empty body")
	}
	want := MrCommentInput{
		Provider:      "forgejo",
		Project:       "BeanieGenie",
		Repository:    mrRepo,
		MRNumber:      mrNumber,
		MRTitle:       mrTitle,
		CommentID:     424242,
		CommentAuthor: "root",
		ReviewEvent:   true,
	}
	if in != want {
		t.Errorf("input = %+v, want %+v", in, want)
	}

	// A review event on a merge request the agent did not create is still
	// declined by the title filter.
	userMR := commentEvent("Add login screen", "")
	userMR.Comment.Review = true
	if _, ok := MrCommentInputFromEvent(userMR); ok {
		t.Error("MrCommentInputFromEvent accepted a review event on a user MR")
	}
}

func TestIsRefinementTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{mrTitle, true},
		{"Refine user story: Add login", true},
		{"  Refine TASK-001: Add user login  ", true}, // trimmed
		{"Refine", false},                             // prefix needs the space
		{"refine TASK-001: Add user login", false},    // case-sensitive
		{"Add login", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsRefinementTitle(tt.title); got != tt.want {
			t.Errorf("IsRefinementTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestIsAgentResponse(t *testing.T) {
	if !IsAgentResponse(responseMarkerFor(42)) {
		t.Error("IsAgentResponse declined the agent's own response marker")
	}
	if !IsAgentResponse("  " + responseMarkerFor(42) + "\n\nThanks.") {
		t.Error("IsAgentResponse declined a marked response body")
	}
	if IsAgentResponse("Please refine this further.") {
		t.Error("IsAgentResponse accepted a user comment")
	}
	if IsAgentResponse("") {
		t.Error("IsAgentResponse accepted an empty body")
	}
	// The marker the response builder emits is exactly what the
	// response to that comment id is recognized by.
	body := buildResponseComment(commentInput(42, "hi"),
		CommentInterpretation{Classification: ClassificationNone, Response: "ack"}, nil, MrCommentResult{})
	if !strings.HasPrefix(body, responseMarkerFor(42)) {
		t.Errorf("response comment does not start with the marker:\n%s", body)
	}
}

func TestMrCommentInputValidate(t *testing.T) {
	valid := commentInput(10, "Please require MFA.")
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate(valid input) = %v, want nil", err)
	}
	for name, mutate := range map[string]func(*MrCommentInput){
		"no repository":   func(in *MrCommentInput) { in.Repository = "" },
		"no mr number":    func(in *MrCommentInput) { in.MRNumber = 0 },
		"no comment id":   func(in *MrCommentInput) { in.CommentID = 0 },
		"no comment body": func(in *MrCommentInput) { in.CommentBody = "  " },
	} {
		in := valid
		mutate(&in)
		if err := in.Validate(); err == nil {
			t.Errorf("%s: Validate accepted an invalid input", name)
		}
	}

	// A review event may arrive with an empty body: the real comment is
	// resolved from the provider API before interpretation.
	review := valid
	review.ReviewEvent = true
	review.CommentBody = ""
	if err := review.Validate(); err != nil {
		t.Errorf("Validate(review event without body) = %v, want nil", err)
	}
}

func TestMrCommentProcessorID(t *testing.T) {
	in := commentInput(10, "hi")
	id := mrCommentProcessorID(in)
	want := "mrCommentProcessorWorkflow-forgejo|root/BeanieGenie|mr|1"
	if id != want {
		t.Errorf("mrCommentProcessorID = %q, want %q", id, want)
	}
	// The same MR always maps to the same slot, whatever the comment.
	if mrCommentProcessorID(commentInput(11, "hi again")) != id {
		t.Error("two comments on one MR map to different processor ids")
	}
	other := in
	other.MRNumber = 2
	if mrCommentProcessorID(other) == id {
		t.Error("different merge requests share a processor id")
	}
}

// --- Correlation helpers (AC9) ---

func TestRefinementShortSHA(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{refineBranch, originShortSHA},
		{"refine/task-009-track-watering-history-per-plant-8395b2548be1", "8395b2548be1"},
		{"refine/task-1-abc1234", "abc1234"}, // 7 chars, the shortest short SHA
		{"refine/task-1", ""},                // no sha segment
		{"refine/task-1-abc123", ""},         // too short
		{"refine/task-1-1234567890abc", ""},  // too long
		{"refine/task-1-8ef0de217a1Z", ""},   // not hexadecimal
		{"feature/task-1-8ef0de217a18", ""},  // not a refinement branch
		{"", ""},
	}
	for _, tt := range tests {
		if got := refinementShortSHA(tt.branch); got != tt.want {
			t.Errorf("refinementShortSHA(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

func TestRefinementWorkflowIDPrefix(t *testing.T) {
	if got := refinementWorkflowIDPrefix("forgejo", mrRepo, ""); got != "" {
		t.Errorf("refinementWorkflowIDPrefix with no sha = %q, want empty", got)
	}
	got := refinementWorkflowIDPrefix("forgejo", mrRepo, originShortSHA)
	want := "userStoryReviewWorkflow-forgejo|root/BeanieGenie|commit|8ef0de217a18"
	if got != want {
		t.Errorf("refinementWorkflowIDPrefix = %q, want %q", got, want)
	}
}

func TestRefinementWorkflowIDPrefixMatchesBridge(t *testing.T) {
	// The prefix must match the workflow ID the event bridge derives
	// for the commit that started the refinement: the comment loop's
	// correlation is only as good as this prefix.
	ev := event.Event{
		Kind:       event.KindCommit,
		Provider:   "forgejo",
		Project:    "BeanieGenie",
		Repository: mrRepo,
		Commit:     &event.Commit{SHA: "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0"},
	}
	bridgeID := WorkflowName + "-" + ev.DedupeKey()
	prefix := refinementWorkflowIDPrefix("forgejo", mrRepo, originShortSHA)
	if !strings.HasPrefix(bridgeID, prefix) {
		t.Errorf("bridge workflow id %q does not start with the refinement prefix %q", bridgeID, prefix)
	}
}

// --- FindRefinement activity (AC9) ---

func TestFindRefinement(t *testing.T) {
	pf := newRefinementFake(t)
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider:   "forgejo",
		Repository: mrRepo,
		MRNumber:   mrNumber,
		CommentID:  100,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if !out.IsRefinement {
		t.Error("IsRefinement = false for the agent's refinement MR")
	}
	if out.RefinementBranch != refineBranch {
		t.Errorf("RefinementBranch = %q, want %q", out.RefinementBranch, refineBranch)
	}
	if out.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want main", out.BaseBranch)
	}
	if out.OriginCommitShortSHA != originShortSHA {
		t.Errorf("OriginCommitShortSHA = %q, want %q", out.OriginCommitShortSHA, originShortSHA)
	}
	wantPrefix := "userStoryReviewWorkflow-forgejo|root/BeanieGenie|commit|8ef0de217a18"
	if out.RefinementWorkflowIDPrefix != wantPrefix {
		t.Errorf("RefinementWorkflowIDPrefix = %q, want %q", out.RefinementWorkflowIDPrefix, wantPrefix)
	}
	if out.AlreadyProcessed {
		t.Error("AlreadyProcessed = true for a comment never answered")
	}
}

func TestFindRefinementNotRefinement(t *testing.T) {
	// A user MR that happens to carry a refinement-like title is not
	// one of the agent's: the head branch decides.
	pf := provider.NewFake(mrRepo)
	if _, err := pf.CreateMergeRequest(t.Context(), mrRepo, mrTitle, "user MR", "feature/login", "main"); err != nil {
		t.Fatalf("seed user MR: %v", err)
	}
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 100,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if out.IsRefinement {
		t.Error("IsRefinement = true for a user MR")
	}
	if out.AlreadyProcessed {
		t.Error("AlreadyProcessed = true for a user MR")
	}
	if out.RefinementWorkflowIDPrefix != "" {
		t.Errorf("RefinementWorkflowIDPrefix = %q, want empty", out.RefinementWorkflowIDPrefix)
	}
}

func TestFindRefinementAlreadyProcessed(t *testing.T) {
	// A comment the agent already answered in an earlier run carries a
	// response referencing its id.
	pf := newRefinementFake(t)
	if _, err := pf.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(100)+"\n\nThanks for the feedback. MFA is now required."); err != nil {
		t.Fatalf("seed response comment: %v", err)
	}
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 100,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if !out.IsRefinement {
		t.Fatal("IsRefinement = false, want true")
	}
	if !out.AlreadyProcessed {
		t.Error("AlreadyProcessed = false for a comment an earlier run answered")
	}

	// A response to a different comment does not mark this one.
	out, err = acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 101,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if out.AlreadyProcessed {
		t.Error("AlreadyProcessed = true for a comment only answered by reference to another id")
	}
}

func TestFindRefinementErrors(t *testing.T) {
	// No provider configured.
	if _, err := (NewActivities(nil, llm.NewFake())).FindRefinement(t.Context(), FindRefinementInput{}); err == nil {
		t.Error("FindRefinement succeeded without a provider client")
	}

	// The merge request does not exist.
	pf := provider.NewFake(mrRepo)
	_, err := (NewActivities(pf, llm.NewFake())).FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: 99, CommentID: 100,
	})
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}

	// The provider API fails.
	pf = newRefinementFake(t)
	pf.Err = errors.New("forgejo unreachable")
	_, err = (NewActivities(pf, llm.NewFake())).FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "fetch merge request") {
		t.Errorf("error = %v, want fetch merge request failure", err)
	}

	// Listing the MR's comments fails: the provider surfaces it after
	// the merge request was fetched.
	pf = newRefinementFake(t)
	_, err = (NewActivities(failListCommentsClient{Client: pf}, llm.NewFake())).FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "list comments") {
		t.Errorf("error = %v, want list comments failure", err)
	}
}

// failListCommentsClient wraps a provider fake whose
// ListMergeRequestComments fails, to test the comment loop's
// already-processed detection error path.
type failListCommentsClient struct {
	provider.Client
}

func (failListCommentsClient) ListMergeRequestComments(context.Context, string, int64) ([]provider.Comment, error) {
	return nil, errors.New("forgejo refused the comment listing")
}

// failListReviewCommentsClient wraps a provider fake whose
// ListMergeRequestReviewComments fails, to test the comment loop's
// inline-comment resolution error path.
type failListReviewCommentsClient struct {
	provider.Client
}

func (failListReviewCommentsClient) ListMergeRequestReviewComments(context.Context, string, int64) ([]provider.Comment, error) {
	return nil, errors.New("forgejo refused the review comment listing")
}

func TestFindRefinementResolvesInlinePosition(t *testing.T) {
	// A comment event whose id is one of the MR's review comments gets its
	// inline position from the provider API: Forgejo webhooks do not carry
	// path/line, but the pulls reviews API does.
	pf := newRefinementFake(t)
	rc, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"This ticket must require MFA.", ticket2Path, 12)
	if err != nil {
		t.Fatalf("seed review comment: %v", err)
	}
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider:      "forgejo",
		Repository:    mrRepo,
		MRNumber:      mrNumber,
		CommentID:     rc.ID,
		CommentBody:   "This ticket must require MFA.",
		CommentAuthor: "root",
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if len(out.Comments) != 1 {
		t.Fatalf("Comments = %+v, want the one resolved comment", out.Comments)
	}
	resolved := out.Comments[0]
	if resolved.ID != rc.ID || resolved.Body != "This ticket must require MFA." {
		t.Errorf("resolved comment = %+v, want id %d with the event's body", resolved, rc.ID)
	}
	if resolved.PositionFile != ticket2Path || resolved.PositionLine != 12 {
		t.Errorf("resolved position = %q:%d, want %q:12", resolved.PositionFile, resolved.PositionLine, ticket2Path)
	}

	// A position the event itself carried wins over the API lookup.
	out, err = acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber,
		CommentID: rc.ID, CommentBody: "This ticket must require MFA.",
		PositionFile: storyPath, PositionLine: 3,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if len(out.Comments) != 1 ||
		out.Comments[0].PositionFile != storyPath || out.Comments[0].PositionLine != 3 {
		t.Errorf("resolved comment = %+v, want the event's position %q:3", out.Comments[0], storyPath)
	}

	// A summary comment (not among the review comments) resolves without a
	// position.
	pf = newRefinementFake(t)
	acts = NewActivities(pf, llm.NewFake())
	out, err = acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 100, CommentBody: "hi",
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if len(out.Comments) != 1 || out.Comments[0].PositionFile != "" {
		t.Errorf("summary comment resolved = %+v, want one comment with no position", out.Comments)
	}
}

func TestFindRefinementReviewEvent(t *testing.T) {
	// A review submission names no comment: the activity resolves every
	// review comment no earlier run answered, oldest first.
	pf := newRefinementFake(t)
	first, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"Not needed, leave it out.", ticket3Path, 8)
	if err != nil {
		t.Fatalf("seed first review comment: %v", err)
	}
	second, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"This ticket must require MFA.", ticket2Path, 12)
	if err != nil {
		t.Fatalf("seed second review comment: %v", err)
	}
	// The first review comment was already answered by an earlier run.
	if _, err := pf.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(first.ID)+"\n\nRemoved it."); err != nil {
		t.Fatalf("seed response comment: %v", err)
	}
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider:    "forgejo",
		Repository:  mrRepo,
		MRNumber:    mrNumber,
		CommentID:   424242, // synthetic stand-in from the webhook payload
		ReviewEvent: true,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if out.AlreadyProcessed {
		t.Error("AlreadyProcessed = true, want false (one review comment is unanswered)")
	}
	if len(out.Comments) != 1 {
		t.Fatalf("Comments = %+v, want the one unanswered review comment", out.Comments)
	}
	if out.Comments[0].ID != second.ID || out.Comments[0].Body != "This ticket must require MFA." {
		t.Errorf("resolved comment = %+v, want the unanswered review comment %d", out.Comments[0], second.ID)
	}
	if out.Comments[0].PositionFile != ticket2Path || out.Comments[0].PositionLine != 12 {
		t.Errorf("resolved position = %q:%d, want %q:12",
			out.Comments[0].PositionFile, out.Comments[0].PositionLine, ticket2Path)
	}

	// When every review comment is already answered and the review body is
	// empty, there is nothing new to process.
	if _, err := pf.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(second.ID)+"\n\nMFA added."); err != nil {
		t.Fatalf("seed second response: %v", err)
	}
	out, err = acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 424242, ReviewEvent: true,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if !out.AlreadyProcessed || len(out.Comments) != 0 {
		t.Errorf("AlreadyProcessed/Comments = %v/%+v, want true/none (all review comments answered)",
			out.AlreadyProcessed, out.Comments)
	}

	// A review with a summary body but no comments resolves the body as a
	// summary comment under the synthetic id.
	pf2 := newRefinementFake(t)
	acts2 := NewActivities(pf2, llm.NewFake())
	summaryInput := FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 777, ReviewEvent: true,
		CommentBody: "Finished review: tighten the scope.", CommentAuthor: "root",
	}
	out, err = acts2.FindRefinement(t.Context(), summaryInput)
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if len(out.Comments) != 1 || out.Comments[0].ID != 777 ||
		out.Comments[0].Body != "Finished review: tighten the scope." {
		t.Errorf("resolved comments = %+v, want the review summary under the synthetic id", out.Comments)
	}
	if out.Comments[0].PositionFile != "" {
		t.Errorf("review summary resolved with position %q, want none", out.Comments[0].PositionFile)
	}

	// A redelivered comment-less review whose summary was already answered
	// resolves nothing: the synthetic id's response marks it.
	if _, err := pf2.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(777)+"\n\nTightened the scope."); err != nil {
		t.Fatalf("seed summary response: %v", err)
	}
	out, err = acts2.FindRefinement(t.Context(), summaryInput)
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if !out.AlreadyProcessed || len(out.Comments) != 0 {
		t.Errorf("AlreadyProcessed/Comments = %v/%+v, want true/none (the summary was answered)",
			out.AlreadyProcessed, out.Comments)
	}
}

func TestFindRefinementReviewCommentListingFailure(t *testing.T) {
	pf := newRefinementFake(t)
	_, err := (NewActivities(failListReviewCommentsClient{Client: pf}, llm.NewFake())).FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber, CommentID: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "list review comments") {
		t.Errorf("error = %v, want list review comments failure", err)
	}
}

func TestFindRefinementReviewEventMultipleComments(t *testing.T) {
	// A review submission carrying several inline comments resolves every
	// unanswered one, oldest first — not just the oldest (TASK-18).
	pf := newRefinementFake(t)
	var seeded []int64
	for _, tc := range []struct {
		body string
		path string
		line int
	}{
		{"No need for this.", ticket3Path, 8},
		{"Leave out the notes.", ticket2Path, 4},
		{"This ticket must require MFA.", ticket2Path, 12},
	} {
		rc, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber, tc.body, tc.path, tc.line)
		if err != nil {
			t.Fatalf("seed review comment: %v", err)
		}
		seeded = append(seeded, rc.ID)
	}
	// The middle comment was already answered by an earlier run: it is
	// skipped by its real comment id.
	if _, err := pf.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(seeded[1])+"\n\nRemoved the notes."); err != nil {
		t.Fatalf("seed response comment: %v", err)
	}
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber,
		CommentID: 424242, ReviewEvent: true,
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if out.AlreadyProcessed {
		t.Error("AlreadyProcessed = true, want false (two review comments are unanswered)")
	}
	if len(out.Comments) != 2 {
		t.Fatalf("Comments = %+v, want the two unanswered review comments", out.Comments)
	}
	first, last := out.Comments[0], out.Comments[1]
	if first.ID != seeded[0] || first.Body != "No need for this." ||
		first.PositionFile != ticket3Path || first.PositionLine != 8 {
		t.Errorf("Comments[0] = %+v, want the oldest unanswered comment %d on %s:8",
			first, seeded[0], ticket3Path)
	}
	if last.ID != seeded[2] || last.Body != "This ticket must require MFA." ||
		last.PositionFile != ticket2Path || last.PositionLine != 12 {
		t.Errorf("Comments[1] = %+v, want the newest unanswered comment %d on %s:12",
			last, seeded[2], ticket2Path)
	}
}

func TestFindRefinementRescansReviewComments(t *testing.T) {
	// Comments left on a review after its submission event fired never get
	// a webhook of their own (forgejo#7966): any later comment event on
	// the same merge request rescans and resolves them, before the
	// event's own comment.
	pf := newRefinementFake(t)
	pending, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"Leave out the notes.", ticket2Path, 4)
	if err != nil {
		t.Fatalf("seed pending review comment: %v", err)
	}
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber,
		CommentID: 100, CommentBody: "Please require MFA.", CommentAuthor: "root",
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if out.AlreadyProcessed {
		t.Error("AlreadyProcessed = true, want false (a review comment is pending)")
	}
	if len(out.Comments) != 2 {
		t.Fatalf("Comments = %+v, want the pending review comment and the event's comment", out.Comments)
	}
	if out.Comments[0].ID != pending.ID || out.Comments[0].Body != "Leave out the notes." ||
		out.Comments[0].PositionFile != ticket2Path || out.Comments[0].PositionLine != 4 {
		t.Errorf("Comments[0] = %+v, want the pending review comment %d first (oldest first)",
			out.Comments[0], pending.ID)
	}
	if out.Comments[1].ID != 100 || out.Comments[1].Body != "Please require MFA." ||
		out.Comments[1].PositionFile != "" {
		t.Errorf("Comments[1] = %+v, want the event's comment last, without a position", out.Comments[1])
	}

	// The event's comment may itself be one of the review comments (a
	// single inline comment fires issue_comment): it is processed once,
	// with its position, not duplicated by the rescan.
	out, err = acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber,
		CommentID: pending.ID, CommentBody: "Leave out the notes.",
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if len(out.Comments) != 1 || out.Comments[0].ID != pending.ID ||
		out.Comments[0].PositionFile != ticket2Path || out.Comments[0].PositionLine != 4 {
		t.Errorf("Comments = %+v, want the review comment once, with its position", out.Comments)
	}

	// An already-answered event comment does not stop the rescan: pending
	// review comments still resolve.
	if _, err := pf.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(100)+"\n\nMFA added."); err != nil {
		t.Fatalf("seed response comment: %v", err)
	}
	out, err = acts.FindRefinement(t.Context(), FindRefinementInput{
		Provider: "forgejo", Repository: mrRepo, MRNumber: mrNumber,
		CommentID: 100, CommentBody: "Please require MFA.",
	})
	if err != nil {
		t.Fatalf("FindRefinement: %v", err)
	}
	if out.AlreadyProcessed || len(out.Comments) != 1 || out.Comments[0].ID != pending.ID {
		t.Errorf("Comments = %+v (AlreadyProcessed %v), want the still-pending review comment",
			out.Comments, out.AlreadyProcessed)
	}
}

// --- CollectTickets activity (AC4) ---

func TestCollectTickets(t *testing.T) {
	pf := newRefinementFake(t)
	acts := NewActivities(pf, llm.NewFake())

	tickets, err := acts.CollectTickets(t.Context(), CollectTicketsInput{
		Repository:       mrRepo,
		RefinementBranch: refineBranch,
		BaseBranch:       "main",
	})
	if err != nil {
		t.Fatalf("CollectTickets: %v", err)
	}
	// The MR's tickets are the files the refinement branch added over
	// the base branch; the story file lives on both, so it is excluded.
	if len(tickets) != 2 {
		t.Fatalf("tickets = %d, want 2", len(tickets))
	}
	if tickets[0].Path != ticket2Path || tickets[1].Path != ticket3Path {
		t.Errorf("ticket paths = %q, %q, want %q, %q", tickets[0].Path, tickets[1].Path, ticket2Path, ticket3Path)
	}
	if tickets[0].Title != "Add login endpoint" {
		t.Errorf("tickets[0].Title = %q, want Add login endpoint", tickets[0].Title)
	}
	if !strings.Contains(tickets[0].Content, "POST /login") {
		t.Errorf("tickets[0].Content misses the current content:\n%s", tickets[0].Content)
	}
	if !strings.Contains(tickets[1].Content, "unit tests") {
		t.Errorf("tickets[1].Content misses the current content:\n%s", tickets[1].Content)
	}
}

func TestCollectTicketsErrors(t *testing.T) {
	if _, err := (NewActivities(nil, llm.NewFake())).CollectTickets(t.Context(), CollectTicketsInput{}); err == nil {
		t.Error("CollectTickets succeeded without a provider client")
	}

	pf := newRefinementFake(t)
	pf.Err = errors.New("forgejo unreachable")
	_, err := (NewActivities(pf, llm.NewFake())).CollectTickets(t.Context(), CollectTicketsInput{
		Repository: mrRepo, RefinementBranch: refineBranch, BaseBranch: "main",
	})
	if err == nil || !strings.Contains(err.Error(), "list backlog/tasks/") {
		t.Errorf("error = %v, want list failure", err)
	}
}

// --- InterpretComment activity (AC3, AC8) ---

func TestInterpretCommentInline(t *testing.T) {
	update := TicketUpdate{
		Path:          ticket2Path,
		Content:       ticketFile(2, "Add login endpoint", ticket2MFA),
		ChangeSummary: "Require MFA on the login endpoint.",
	}
	lf := llm.NewFake(interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.", update))
	acts := NewActivities(provider.NewFake(), lf)

	in := InterpretCommentInput{
		Repository: mrRepo,
		MRNumber:   mrNumber,
		MRTitle:    mrTitle,
		Comment: CommentFeedback{
			ID:           100,
			Body:         "This ticket must require MFA.",
			Author:       "root",
			PositionFile: ticket2Path,
			PositionLine: 12,
		},
		Tickets: []RefinementTicket{
			{Path: ticket2Path, Title: "Add login endpoint", Content: ticketFile(2, "Add login endpoint", ticket2Body)},
			{Path: ticket3Path, Title: "Add login tests", Content: ticketFile(3, "Add login tests", ticket3Body)},
		},
	}
	out, err := acts.InterpretComment(t.Context(), in)
	if err != nil {
		t.Fatalf("InterpretComment: %v", err)
	}
	if out.Classification != ClassificationInline {
		t.Errorf("Classification = %q, want inline", out.Classification)
	}
	if out.Response != "Updated the endpoint ticket to require MFA." {
		t.Errorf("Response = %q, want the scripted prose", out.Response)
	}
	if len(out.Updates) != 1 || out.Updates[0].Path != ticket2Path {
		t.Errorf("Updates = %+v, want one for %s", out.Updates, ticket2Path)
	}

	// The LLM was called once, in JSON mode, with the comment (and its
	// inline position) and the current tickets in the prompt.
	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	if !calls[0].JSON {
		t.Error("llm call was not in JSON mode")
	}
	prompt := calls[0].Messages[0].Content
	for _, want := range []string{
		"This ticket must require MFA.",
		"(The comment was posted on file " + ticket2Path + ", line 12.)",
		ticket2Body,
		ticket3Body,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt misses %q:\n%s", want, prompt)
		}
	}
}

func TestInterpretCommentSummary(t *testing.T) {
	// A summary comment has no position and may update any ticket.
	reply := interpretReply(ClassificationSummary, "Applied the JWT policy to both tickets.",
		TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "JWT policy."},
		TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "JWT policy."},
	)
	lf := llm.NewFake(reply)
	acts := NewActivities(provider.NewFake(), lf)

	out, err := acts.InterpretComment(t.Context(), InterpretCommentInput{
		Repository: mrRepo,
		MRNumber:   mrNumber,
		MRTitle:    mrTitle,
		Comment:    CommentFeedback{ID: 100, Body: "The JWT should expire after 24 hours everywhere.", Author: "root"},
		Tickets:    []RefinementTicket{{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2Body)}},
	})
	if err != nil {
		t.Fatalf("InterpretComment: %v", err)
	}
	if out.Classification != ClassificationSummary {
		t.Errorf("Classification = %q, want summary", out.Classification)
	}
	if len(out.Updates) != 2 {
		t.Errorf("Updates = %d, want 2", len(out.Updates))
	}
	// No position was reported, so the prompt carries no position line.
	if prompt := lf.Calls()[0].Messages[0].Content; strings.Contains(prompt, "posted on file") {
		t.Errorf("summary prompt carries an inline position:\n%s", prompt)
	}
}

func TestInterpretCommentNone(t *testing.T) {
	lf := llm.NewFake(interpretReply(ClassificationNone, "Happy to clarify: the JWT expires after 24 hours."))
	acts := NewActivities(provider.NewFake(), lf)

	out, err := acts.InterpretComment(t.Context(), InterpretCommentInput{
		Comment: CommentFeedback{ID: 100, Body: "When does the JWT expire?", Author: "root"},
	})
	if err != nil {
		t.Fatalf("InterpretComment: %v", err)
	}
	if out.Classification != ClassificationNone {
		t.Errorf("Classification = %q, want none", out.Classification)
	}
	if len(out.Updates) != 0 {
		t.Errorf("Updates = %+v, want none", out.Updates)
	}
	if !strings.Contains(out.Response, "24 hours") {
		t.Errorf("Response = %q, want the clarifying prose", out.Response)
	}
}

func TestInterpretCommentErrors(t *testing.T) {
	// No LLM configured.
	if _, err := (NewActivities(provider.NewFake(), nil)).InterpretComment(t.Context(), InterpretCommentInput{}); err == nil {
		t.Error("InterpretComment succeeded without an llm client")
	}

	// The LLM API fails.
	lf := llm.NewFake()
	lf.Err = errors.New("llm rate limited")
	if _, err := (NewActivities(provider.NewFake(), lf)).InterpretComment(t.Context(), InterpretCommentInput{}); err == nil ||
		!strings.Contains(err.Error(), "llm rate limited") {
		t.Errorf("error = %v, want llm failure", err)
	}

	// The LLM returns a classification the loop does not know.
	lf = llm.NewFake(interpretReply("angry", "no"))
	if _, err := (NewActivities(provider.NewFake(), lf)).InterpretComment(t.Context(), InterpretCommentInput{}); err == nil ||
		!strings.Contains(err.Error(), "unknown classification") {
		t.Errorf("error = %v, want unknown classification failure", err)
	}
}

// --- Update validation (AC4, AC6) ---

func TestValidatedUpdates(t *testing.T) {
	tickets := []RefinementTicket{
		{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2Body)},
		{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Body)},
	}
	goodUpdate := TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA)}
	otherUpdate := TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate)}

	tests := []struct {
		name            string
		interpretation  CommentInterpretation
		in              MrCommentInput
		wantUpdates     int
		wantErrContains string
	}{
		{
			name:           "non-actionable comment updates nothing",
			interpretation: CommentInterpretation{Classification: ClassificationNone, Response: "ack"},
			wantUpdates:    0,
		},
		{
			name:           "actionable comment without updates changes nothing",
			interpretation: CommentInterpretation{Classification: ClassificationInline},
			wantUpdates:    0,
		},
		{
			name:            "unknown classification fails the run",
			interpretation:  CommentInterpretation{Classification: "angry", Updates: []TicketUpdate{goodUpdate}},
			wantErrContains: "unknown comment classification",
		},
		{
			name: "update for a file that is not one of the MR's tickets",
			interpretation: CommentInterpretation{Classification: ClassificationInline, Updates: []TicketUpdate{
				{Path: "backlog/tasks/task-009 - Nope.md", Content: "x"},
			}},
			wantErrContains: "is not a ticket of this merge request",
		},
		{
			name:            "duplicate update for one ticket",
			interpretation:  CommentInterpretation{Classification: ClassificationInline, Updates: []TicketUpdate{goodUpdate, goodUpdate}},
			wantErrContains: "duplicate update",
		},
		{
			name: "empty update content",
			interpretation: CommentInterpretation{Classification: ClassificationInline, Updates: []TicketUpdate{
				{Path: ticket2Path, Content: "  "},
			}},
			wantErrContains: "is empty",
		},
		{
			name:            "inline position constrains the update to the positioned ticket",
			interpretation:  CommentInterpretation{Classification: ClassificationInline, Updates: []TicketUpdate{otherUpdate}},
			in:              MrCommentInput{PositionFile: ticket2Path},
			wantErrContains: "comment is positioned on",
		},
		{
			name:           "position on a file that is not a ticket does not constrain",
			interpretation: CommentInterpretation{Classification: ClassificationInline, Updates: []TicketUpdate{goodUpdate}},
			in:             MrCommentInput{PositionFile: "src/main.go", PositionLine: 3},
			wantUpdates:    1,
		},
		{
			name:           "inline update passes",
			interpretation: CommentInterpretation{Classification: ClassificationInline, Updates: []TicketUpdate{goodUpdate}},
			wantUpdates:    1,
		},
		{
			name:           "summary may update several tickets",
			interpretation: CommentInterpretation{Classification: ClassificationSummary, Updates: []TicketUpdate{goodUpdate, otherUpdate}},
			wantUpdates:    2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updates, err := validatedUpdates(tt.interpretation, tickets, tt.in)
			if tt.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatedUpdates: %v", err)
			}
			if len(updates) != tt.wantUpdates {
				t.Errorf("updates = %d, want %d", len(updates), tt.wantUpdates)
			}
		})
	}
}

func TestUpdatedTicketContent(t *testing.T) {
	original := ticketFile(2, "Add login endpoint", ticket2Body)

	// A good rewrite keeps the frontmatter and the id; the activity
	// stamps updated_date.
	updated := ticketFile(2, "Add login endpoint", ticket2MFA)
	content, err := updatedTicketContent(original, updated, "2026-11-02 09:00")
	if err != nil {
		t.Fatalf("updatedTicketContent: %v", err)
	}
	if !strings.Contains(content, "id: TASK-002") {
		t.Errorf("content lost the ticket id:\n%s", content)
	}
	if !strings.Contains(content, "updated_date: '2026-11-02 09:00'") {
		t.Errorf("content misses the bumped updated_date:\n%s", content)
	}
	if strings.Contains(content, "Require MFA") != strings.Contains(updated, "Require MFA") {
		t.Error("content does not carry the update's body")
	}

	// A rewrite that drops the frontmatter is rejected.
	if _, err := updatedTicketContent(original, "no frontmatter anymore", "now"); err == nil ||
		!strings.Contains(err.Error(), "lost the frontmatter block") {
		t.Errorf("error = %v, want lost frontmatter", err)
	}
	// A ticket without frontmatter cannot be updated.
	if _, err := updatedTicketContent("no frontmatter at all", updated, "now"); err == nil ||
		!strings.Contains(err.Error(), "carries no frontmatter") {
		t.Errorf("error = %v, want no frontmatter", err)
	}
	// A ticket without an id cannot be updated.
	if _, err := updatedTicketContent("---\ntitle: x\n---\nbody", updated, "now"); err == nil ||
		!strings.Contains(err.Error(), "carries no id") {
		t.Errorf("error = %v, want no id", err)
	}
	// A rewrite that changes the id is rejected.
	renamed := strings.Replace(updated, "id: TASK-002", "id: TASK-009", 1)
	if _, err := updatedTicketContent(original, renamed, "now"); err == nil ||
		!strings.Contains(err.Error(), "changed the id") {
		t.Errorf("error = %v, want changed id", err)
	}
}

func TestBumpUpdatedDate(t *testing.T) {
	// An existing updated_date is replaced.
	content, ok := bumpUpdatedDate("---\nid: TASK-002\nupdated_date: '2026-10-01 10:00'\n---\nbody", "2026-11-02 09:00")
	if !ok {
		t.Fatal("bumpUpdatedDate reported no frontmatter")
	}
	if strings.Contains(content, "2026-10-01 10:00") || !strings.Contains(content, "updated_date: '2026-11-02 09:00'") {
		t.Errorf("content = %q, want the replaced updated_date", content)
	}

	// A missing updated_date is inserted before the closing ---.
	content, ok = bumpUpdatedDate("---\nid: TASK-002\n---\nbody", "2026-11-02 09:00")
	if !ok {
		t.Fatal("bumpUpdatedDate reported no frontmatter")
	}
	want := "---\nid: TASK-002\nupdated_date: '2026-11-02 09:00'\n---\nbody"
	if content != want {
		t.Errorf("content = %q, want %q", content, want)
	}

	// No frontmatter, no bump.
	if _, ok := bumpUpdatedDate("no frontmatter", "now"); ok {
		t.Error("bumpUpdatedDate accepted content without frontmatter")
	}
}

// --- ApplyTicketUpdates and PostResponseComment activities (AC6, AC7) ---

func TestApplyTicketUpdates(t *testing.T) {
	pf := newRefinementFake(t)
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.ApplyTicketUpdates(t.Context(), ApplyTicketUpdatesInput{
		Repository: mrRepo,
		MRNumber:   mrNumber,
		Branch:     refineBranch,
		CommentID:  100,
		Updates: []TicketUpdate{{
			Path:          ticket2Path,
			Content:       ticketFile(2, "Add login endpoint", ticket2MFA),
			ChangeSummary: "Require MFA.",
		}},
	})
	if err != nil {
		t.Fatalf("ApplyTicketUpdates: %v", err)
	}
	if len(out.UpdatedFiles) != 1 || out.UpdatedFiles[0] != ticket2Path {
		t.Errorf("UpdatedFiles = %v, want [%s]", out.UpdatedFiles, ticket2Path)
	}
	if out.CommitSHA == "" {
		t.Error("CommitSHA is empty")
	}
	// The update landed on the MR's branch, with updated_date bumped.
	file, err := pf.GetFile(t.Context(), mrRepo, ticket2Path, refineBranch)
	if err != nil {
		t.Fatalf("updated ticket not on the branch: %v", err)
	}
	if !strings.Contains(string(file.Content), "Require MFA") {
		t.Errorf("branch content misses the update:\n%s", file.Content)
	}
	if strings.Contains(string(file.Content), "updated_date: '2026-10-01 10:00'") {
		t.Errorf("updated_date was not bumped:\n%s", file.Content)
	}
	// The base branch never carried the ticket.
	if _, err := pf.GetFile(t.Context(), mrRepo, ticket2Path, "main"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("base branch lookup error = %v, want ErrNotFound", err)
	}

	// Empty updates commit nothing.
	out, err = acts.ApplyTicketUpdates(t.Context(), ApplyTicketUpdatesInput{
		Repository: mrRepo, Branch: refineBranch,
	})
	if err != nil {
		t.Fatalf("ApplyTicketUpdates with no updates: %v", err)
	}
	if len(out.UpdatedFiles) != 0 || out.CommitSHA != "" {
		t.Errorf("out = %+v, want nothing committed", out)
	}
}

func TestApplyTicketUpdatesErrors(t *testing.T) {
	if _, err := (NewActivities(nil, llm.NewFake())).ApplyTicketUpdates(t.Context(), ApplyTicketUpdatesInput{}); err == nil {
		t.Error("ApplyTicketUpdates succeeded without a provider client")
	}

	pf := newRefinementFake(t)
	acts := NewActivities(pf, llm.NewFake())

	// A ticket the branch does not carry fails the update.
	_, err := acts.ApplyTicketUpdates(t.Context(), ApplyTicketUpdatesInput{
		Repository: mrRepo, Branch: refineBranch,
		Updates: []TicketUpdate{{Path: "backlog/tasks/task-009 - Nope.md", Content: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "fetch ticket") {
		t.Errorf("error = %v, want fetch ticket failure", err)
	}

	// A rewrite that loses the frontmatter id fails before committing.
	_, err = acts.ApplyTicketUpdates(t.Context(), ApplyTicketUpdatesInput{
		Repository: mrRepo, Branch: refineBranch,
		Updates: []TicketUpdate{{
			Path:    ticket2Path,
			Content: strings.Replace(ticketFile(2, "Add login endpoint", ticket2MFA), "id: TASK-002", "id: TASK-009", 1),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "changed the id") {
		t.Errorf("error = %v, want changed id failure", err)
	}

	// A provider commit failure surfaces.
	pf = newRefinementFake(t)
	_, err = (NewActivities(failCommitClient{Client: pf}, llm.NewFake())).ApplyTicketUpdates(t.Context(), ApplyTicketUpdatesInput{
		Repository: mrRepo, Branch: refineBranch,
		Updates: []TicketUpdate{{
			Path:    ticket2Path,
			Content: ticketFile(2, "Add login endpoint", ticket2MFA),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "commit ticket updates") {
		t.Errorf("error = %v, want commit failure", err)
	}
}

func TestPostResponseComment(t *testing.T) {
	pf := newRefinementFake(t)
	acts := NewActivities(pf, llm.NewFake())

	out, err := acts.PostResponseComment(t.Context(), PostResponseCommentInput{
		Repository: mrRepo,
		MRNumber:   mrNumber,
		Body:       responseMarkerFor(100) + "\n\nThanks for the feedback.",
	})
	if err != nil {
		t.Fatalf("PostResponseComment: %v", err)
	}
	if out.CommentID == 0 {
		t.Error("CommentID is empty")
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "Thanks for the feedback.") {
		t.Errorf("comments = %+v, want the response", comments)
	}

	// A merge request that does not exist cannot be commented on.
	if _, err := acts.PostResponseComment(t.Context(), PostResponseCommentInput{
		Repository: mrRepo, MRNumber: 99, Body: "hi",
	}); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}

	if _, err := (NewActivities(nil, llm.NewFake())).PostResponseComment(t.Context(), PostResponseCommentInput{}); err == nil {
		t.Error("PostResponseComment succeeded without a provider client")
	}
}

// --- The comment workflows (AC1, AC3-AC8) ---

func TestMrCommentWorkflowInlineComment(t *testing.T) {
	// AC4: an inline comment on one ticket file updates that ticket.
	pf := newRefinementFake(t)
	in := commentInput(100, "This ticket must require MFA for the login endpoint.")
	in.PositionFile = ticket2Path
	in.PositionLine = 12
	update := TicketUpdate{
		Path:          ticket2Path,
		Content:       ticketFile(2, "Add login endpoint", ticket2MFA),
		ChangeSummary: "Require MFA on the login endpoint.",
	}
	lf := llm.NewFake(interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.", update))
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, in)

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Processed || !result.ResponsePosted {
		t.Errorf("result = %+v, want processed with a response", result)
	}
	if result.Classification != ClassificationInline {
		t.Errorf("Classification = %q, want inline", result.Classification)
	}
	if result.Status != "inline feedback applied to 1 ticket(s)" {
		t.Errorf("Status = %q, want inline feedback status", result.Status)
	}
	if len(result.UpdatedFiles) != 1 || result.UpdatedFiles[0] != ticket2Path {
		t.Errorf("UpdatedFiles = %v, want [%s]", result.UpdatedFiles, ticket2Path)
	}
	if result.CommitSHA == "" {
		t.Error("CommitSHA is empty")
	}

	// AC9: the run correlated the comment to the refinement that
	// created the MR.
	if result.MRNumber != mrNumber {
		t.Errorf("MRNumber = %d, want %d", result.MRNumber, mrNumber)
	}
	if result.RefinementBranch != refineBranch || result.BaseBranch != "main" {
		t.Errorf("branches = %q/%q, want %q/main", result.RefinementBranch, result.BaseBranch, refineBranch)
	}
	if result.OriginCommitShortSHA != originShortSHA {
		t.Errorf("OriginCommitShortSHA = %q, want %q", result.OriginCommitShortSHA, originShortSHA)
	}
	wantPrefix := "userStoryReviewWorkflow-forgejo|root/BeanieGenie|commit|8ef0de217a18"
	if result.RefinementWorkflowIDPrefix != wantPrefix {
		t.Errorf("RefinementWorkflowIDPrefix = %q, want %q", result.RefinementWorkflowIDPrefix, wantPrefix)
	}

	// AC6: the update landed on the MR's own branch, never a new one.
	file, err := pf.GetFile(t.Context(), mrRepo, ticket2Path, refineBranch)
	if err != nil {
		t.Fatalf("updated ticket not on the refinement branch: %v", err)
	}
	if !strings.Contains(string(file.Content), "Require MFA") {
		t.Errorf("branch content misses the MFA update:\n%s", file.Content)
	}
	if strings.Contains(string(file.Content), "updated_date: '2026-10-01 10:00'") {
		t.Errorf("updated_date was not bumped:\n%s", file.Content)
	}

	// AC7: the response comment summarizes what changed and why.
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want the one response", len(comments))
	}
	body := comments[0].Body
	for _, want := range []string{
		responseMarkerFor(100),
		"Thanks for the feedback, **@root**.",
		"feedback on specific tickets",
		"Updated the endpoint ticket to require MFA.",
		"## Ticket changes",
		"`" + ticket2Path + "`: Require MFA on the login endpoint.",
		"Committed to `" + refineBranch + "` (this merge request's branch) as `" + ShortSHA(result.CommitSHA) + "`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response comment misses %q:\n%s", want, body)
		}
	}

	// AC8: the LLM ran once, in JSON mode, inside the activity.
	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	if !calls[0].JSON {
		t.Error("llm call was not in JSON mode")
	}
	if prompt := calls[0].Messages[0].Content; !strings.Contains(prompt, "(The comment was posted on file "+ticket2Path+", line 12.)") {
		t.Errorf("prompt misses the inline position:\n%s", prompt)
	}
}

func TestMrCommentWorkflowSummaryComment(t *testing.T) {
	// AC5: a summary comment updates tickets across the refinement.
	pf := newRefinementFake(t)
	lf := llm.NewFake(interpretReply(ClassificationSummary, "Applied the JWT policy to both tickets.",
		TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "JWT expiry policy."},
		TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "JWT expiry policy."},
	))
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "The JWT should expire after 24 hours everywhere."))

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Processed {
		t.Fatal("result.Processed = false, want true")
	}
	if result.Classification != ClassificationSummary {
		t.Errorf("Classification = %q, want summary", result.Classification)
	}
	if len(result.UpdatedFiles) != 2 {
		t.Errorf("UpdatedFiles = %v, want both tickets", result.UpdatedFiles)
	}
	for _, path := range []string{ticket2Path, ticket3Path} {
		file, err := pf.GetFile(t.Context(), mrRepo, path, refineBranch)
		if err != nil {
			t.Fatalf("ticket %s not on the branch: %v", path, err)
		}
		if strings.Contains(string(file.Content), "updated_date: '2026-10-01 10:00'") {
			t.Errorf("ticket %s was not updated:\n%s", path, file.Content)
		}
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "feedback on the overall refinement") {
		t.Errorf("comments = %+v, want a summary response", comments)
	}
}

func TestMrCommentWorkflowNonActionableComment(t *testing.T) {
	// A comment that is not ticket feedback gets a plain answer; no
	// ticket is touched.
	pf := newRefinementFake(t)
	lf := llm.NewFake(interpretReply(ClassificationNone, "Happy to clarify: the JWT expires after 24 hours."))
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "When does the JWT expire?"))

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Processed {
		t.Fatal("result.Processed = false, want true")
	}
	if result.Classification != ClassificationNone {
		t.Errorf("Classification = %q, want none", result.Classification)
	}
	if len(result.UpdatedFiles) != 0 || result.CommitSHA != "" {
		t.Errorf("result = %+v, want nothing committed", result)
	}
	// The tickets are untouched.
	file, err := pf.GetFile(t.Context(), mrRepo, ticket2Path, refineBranch)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(file.Content) != ticketFile(2, "Add login endpoint", ticket2Body) {
		t.Errorf("ticket was modified:\n%s", file.Content)
	}
	// But the question was answered on the MR.
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want the one response", len(comments))
	}
	for _, want := range []string{"No ticket changes were needed.", "Happy to clarify: the JWT expires after 24 hours."} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("response comment misses %q:\n%s", want, comments[0].Body)
		}
	}
}

func TestMrCommentWorkflowCommentAlreadyAnswered(t *testing.T) {
	// A comment an earlier run already answered is skipped, not
	// applied twice.
	pf := newRefinementFake(t)
	if _, err := pf.AddMergeRequestComment(t.Context(), mrRepo, mrNumber,
		responseMarkerFor(100)+"\n\nThanks for the feedback. MFA is now required."); err != nil {
		t.Fatalf("seed earlier response: %v", err)
	}
	lf := llm.NewFake() // any scripted reply would fail the test if used
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Please require MFA."))

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if result.Processed {
		t.Error("result.Processed = true for an already answered comment")
	}
	if !result.AlreadyProcessed {
		t.Error("result.AlreadyProcessed = false, want true")
	}
	if len(result.UpdatedFiles) != 0 || result.CommitSHA != "" {
		t.Errorf("result = %+v, want nothing committed", result)
	}
	if len(lf.Calls()) != 0 {
		t.Errorf("llm calls = %d, want none", len(lf.Calls()))
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("comments = %d, want only the earlier response", len(comments))
	}
}

func TestMrCommentWorkflowNotAgentMergeRequest(t *testing.T) {
	// A user MR titled like a refinement: the trigger's title filter
	// passes it, the workflow's branch check declines it.
	pf := provider.NewFake(mrRepo)
	if _, err := pf.CreateMergeRequest(t.Context(), mrRepo, mrTitle, "user MR", "feature/login", "main"); err != nil {
		t.Fatalf("seed user MR: %v", err)
	}
	lf := llm.NewFake() // any scripted reply would fail the test if used
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Looks like a refinement."))

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if result.Processed {
		t.Error("result.Processed = true for a user MR")
	}
	if result.Status != "merge request is not a refinement created by the agent" {
		t.Errorf("Status = %q, want the not-a-refinement status", result.Status)
	}
	if len(lf.Calls()) != 0 {
		t.Errorf("llm calls = %d, want none", len(lf.Calls()))
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("comments = %d, want none", len(comments))
	}
}

func TestMrCommentWorkflowRejectsInvalidInput(t *testing.T) {
	env := newTestEnvWithClients(t, newRefinementFake(t), llm.NewFake())

	env.ExecuteWorkflow(MrCommentWorkflow, MrCommentInput{})

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "invalid "+MrCommentWorkflowName+" input") {
		t.Errorf("error = %v, want invalid "+MrCommentWorkflowName+" input", err)
	}
}

func TestMrCommentProcessorWorkflowRejectsInvalidInput(t *testing.T) {
	// The processor validates its own input when started directly.
	env := newTestEnvWithClients(t, newRefinementFake(t), llm.NewFake())

	env.ExecuteWorkflow(MrCommentProcessorWorkflow, MrCommentInput{})

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "invalid "+MrCommentProcessorWorkflowName+" input") {
		t.Errorf("error = %v, want invalid "+MrCommentProcessorWorkflowName+" input", err)
	}
}

// --- The comment workflows' error handling (AC11) ---

func TestMrCommentWorkflowFindRefinementFailure(t *testing.T) {
	pf := newRefinementFake(t)
	pf.Err = errors.New("forgejo unreachable")
	env := newTestEnvWithClients(t, pf, llm.NewFake())

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Please require MFA."))

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "find refinement") {
		t.Errorf("error = %v, want find refinement failure", err)
	}
}

func TestMrCommentWorkflowCollectTicketsFailure(t *testing.T) {
	// The provider fails while listing the refinement branch's files:
	// the workflow fails collecting the MR's tickets.
	pf := newRefinementFake(t)
	lf := llm.NewFake()
	env := newTestEnvWithClients(t, failListFilesClient{Client: pf}, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Please require MFA."))

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "collect tickets") {
		t.Errorf("error = %v, want collect tickets failure", err)
	}
}

// failListFilesClient wraps a provider fake whose ListFiles fails, to
// test the comment loop's error handling while collecting tickets.
type failListFilesClient struct {
	provider.Client
}

func (failListFilesClient) ListFiles(context.Context, string, string, string) ([]provider.FileInfo, error) {
	return nil, errors.New("forgejo refused the file listing")
}

func TestMrCommentWorkflowInterpretationFailure(t *testing.T) {
	// The LLM API fails while interpreting the comment.
	pf := newRefinementFake(t)
	lf := llm.NewFake()
	lf.Err = errors.New("llm rate limited")
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Please require MFA."))

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "interpret comment") {
		t.Errorf("error = %v, want interpret comment failure", err)
	}
}

func TestMrCommentWorkflowUpdateForUnknownFile(t *testing.T) {
	// The LLM proposes rewriting a file that is not one of the MR's
	// tickets: the workflow's validation fails the run instead of
	// committing to an arbitrary path.
	pf := newRefinementFake(t)
	lf := llm.NewFake(interpretReply(ClassificationInline, "Renamed the ticket.",
		TicketUpdate{Path: "backlog/tasks/task-009 - Nope.md", Content: ticketFile(9, "Nope", "x")}))
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Move this to a new ticket."))

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "is not a ticket of this merge request") {
		t.Errorf("error = %v, want not-a-ticket validation failure", err)
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("comments = %d, want none (nothing was committed)", len(comments))
	}
}

func TestMrCommentWorkflowApplyFailure(t *testing.T) {
	// The provider refuses the commit that carries the updates.
	pf := newRefinementFake(t)
	lf := llm.NewFake(interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.",
		TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA)}))
	env := newTestEnvWithClients(t, failCommitClient{Client: pf}, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "Please require MFA."))

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "apply ticket updates") {
		t.Errorf("error = %v, want apply ticket updates failure", err)
	}
}

// failCommitClient wraps a provider fake whose Commit fails, to test
// the comment loop's error handling while applying ticket updates.
type failCommitClient struct {
	provider.Client
}

func (failCommitClient) Commit(context.Context, string, string, string, []provider.FileChange) (provider.CommitResult, error) {
	return provider.CommitResult{}, errors.New("forgejo refused the commit")
}

// --- Sequential per-MR processing (AC10) ---

func TestMrCommentWorkflowSequentialCommentsOnOneMR(t *testing.T) {
	// Two comments on one MR, handled one after the other: each run
	// completes before the next starts, and the per-MR processor
	// workflow ID is reused (completed executions, ALLOW_DUPLICATE).
	pf := newRefinementFake(t)
	lf := llm.NewFake(
		interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.",
			TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "Require MFA."}),
		interpretReply(ClassificationInline, "Extended the test ticket to cover rate limiting.",
			TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "Cover rate limiting."}),
	)
	env := newTestEnvWithClients(t, pf, lf)
	env.RegisterWorkflow(twoCommentsInSequence)

	env.ExecuteWorkflow(twoCommentsInSequence, [2]MrCommentInput{
		commentInput(101, "Please require MFA."),
		commentInput(102, "Also cover rate limiting in the tests."),
	})

	var results [2]MrCommentResult
	if err := env.GetWorkflowResult(&results); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	for i, r := range results {
		if !r.Processed {
			t.Errorf("results[%d] = %+v, want processed", i, r)
		}
	}
	if len(results[0].UpdatedFiles) != 1 || results[0].UpdatedFiles[0] != ticket2Path {
		t.Errorf("first result = %+v, want the MFA update applied", results[0])
	}
	if len(results[1].UpdatedFiles) != 1 || results[1].UpdatedFiles[0] != ticket3Path {
		t.Errorf("second result = %+v, want the rate limiting update applied", results[1])
	}

	// The second run saw the first run's commit: its interpretation
	// prompt carried the MFA-updated ticket.
	calls := lf.Calls()
	if len(calls) != 2 {
		t.Fatalf("llm calls = %d, want 2", len(calls))
	}
	if prompt := calls[1].Messages[0].Content; !strings.Contains(prompt, "Require MFA") {
		t.Errorf("second prompt misses the first comment's applied update:\n%s", prompt)
	}

	// Both responses are on the MR, each referencing its comment.
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("comments = %d, want two responses", len(comments))
	}
	bodies := comments[0].Body + "\n" + comments[1].Body
	for _, marker := range []string{responseMarkerFor(101), responseMarkerFor(102)} {
		if !strings.Contains(bodies, marker) {
			t.Errorf("responses miss %q:\n%s", marker, bodies)
		}
	}
}

// twoCommentsInSequence is a test-only workflow that processes two
// comments on one MR one after the other, the way queued comment
// events would: the second comment workflow starts only after the
// first completed.
func twoCommentsInSequence(ctx workflow.Context, ins [2]MrCommentInput) ([2]MrCommentResult, error) {
	var out [2]MrCommentResult
	if err := workflow.ExecuteChildWorkflow(ctx, MrCommentWorkflow, ins[0]).Get(ctx, &out[0]); err != nil {
		return out, err
	}
	if err := workflow.ExecuteChildWorkflow(ctx, MrCommentWorkflow, ins[1]).Get(ctx, &out[1]); err != nil {
		return out, err
	}
	return out, nil
}

// twoCommentsAtOnce is a test-only workflow that starts two comment
// workflows concurrently, the way two comment events on one MR arrive
// together: both run at the same time, and the per-MR processing slot
// must serialize them.
func twoCommentsAtOnce(ctx workflow.Context, ins [2]MrCommentInput) ([2]MrCommentResult, error) {
	var out [2]MrCommentResult
	first := workflow.ExecuteChildWorkflow(ctx, MrCommentWorkflow, ins[0])
	second := workflow.ExecuteChildWorkflow(ctx, MrCommentWorkflow, ins[1])
	if err := first.Get(ctx, &out[0]); err != nil {
		return out, err
	}
	if err := second.Get(ctx, &out[1]); err != nil {
		return out, err
	}
	return out, nil
}

func TestMrCommentWorkflowConcurrentCommentsOnOneMR(t *testing.T) {
	// Two comments on one MR arriving at once: the second must wait
	// for the per-MR processing slot (its processor workflow ID is
	// taken) and run strictly after the first, never concurrently —
	// so both updates land, and the second run sees the first's.
	pf := newRefinementFake(t)
	lf := llm.NewFake(
		interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.",
			TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "Require MFA."}),
		interpretReply(ClassificationInline, "Extended the test ticket to cover rate limiting.",
			TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "Cover rate limiting."}),
	)
	env := newTestEnvWithClients(t, pf, lf)
	env.RegisterWorkflow(twoCommentsAtOnce)

	env.ExecuteWorkflow(twoCommentsAtOnce, [2]MrCommentInput{
		commentInput(101, "Please require MFA."),
		commentInput(102, "Also cover rate limiting in the tests."),
	})

	var results [2]MrCommentResult
	if err := env.GetWorkflowResult(&results); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	for i, r := range results {
		if !r.Processed {
			t.Errorf("results[%d] = %+v, want processed", i, r)
		}
	}

	// Serialized: whichever comment ran first was interpreted before the
	// second — the second's prompt carries the first's applied update.
	// (Which of the two concurrently started comments wins the slot first
	// is not deterministic; the order-independence is what matters.)
	calls := lf.Calls()
	if len(calls) != 2 {
		t.Fatalf("llm calls = %d, want 2", len(calls))
	}
	first := calls[0].Messages[0].Content
	second := calls[1].Messages[0].Content
	otherComment, firstChange := "Also cover rate limiting in the tests.", "rate limiting"
	if strings.Contains(first, "Please require MFA.") {
		otherComment, firstChange = "Also cover rate limiting in the tests.", "rate limiting"
	} else if strings.Contains(first, "Also cover rate limiting in the tests.") {
		otherComment, firstChange = "Please require MFA.", "Require MFA"
	} else {
		t.Fatalf("first prompt carries neither comment:\n%s", first)
	}
	if !strings.Contains(second, otherComment) {
		t.Errorf("second prompt = %q, want the other comment %q", second, otherComment)
	}
	if !strings.Contains(second, firstChange) {
		t.Errorf("second prompt misses the first comment's applied update (not serialized):\n%s", second)
	}

	// Both updates landed on the MR's branch, and both responses were
	// posted.
	for path, want := range map[string]string{
		ticket2Path: "Require MFA",
		ticket3Path: "rate limiting",
	} {
		file, err := pf.GetFile(t.Context(), mrRepo, path, refineBranch)
		if err != nil {
			t.Fatalf("ticket %s not on the branch: %v", path, err)
		}
		if !strings.Contains(string(file.Content), want) {
			t.Errorf("ticket %s misses %q:\n%s", path, want, file.Content)
		}
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("comments = %d, want two responses", len(comments))
	}
	bodies := comments[0].Body + "\n" + comments[1].Body
	for _, marker := range []string{responseMarkerFor(101), responseMarkerFor(102)} {
		if !strings.Contains(bodies, marker) {
			t.Errorf("responses miss %q:\n%s", marker, bodies)
		}
	}
}

// --- Review-submission events (inline diff comments) ---

func TestMrCommentWorkflowReviewEvent(t *testing.T) {
	// A review submission arrives as a webhook event with action
	// "reviewed" and no comment in the payload (synthetic id, empty body).
	// The run resolves the review's inline comment through the provider
	// API — real id, body, and file position — and applies it to the
	// commented ticket.
	pf := newRefinementFake(t)
	rc, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"This ticket must require MFA for the login endpoint.", ticket2Path, 12)
	if err != nil {
		t.Fatalf("seed review comment: %v", err)
	}

	in := commentInput(424242, "") // synthetic stand-in id, empty review body
	in.ReviewEvent = true
	update := TicketUpdate{
		Path:          ticket2Path,
		Content:       ticketFile(2, "Add login endpoint", ticket2MFA),
		ChangeSummary: "Require MFA on the login endpoint.",
	}
	lf := llm.NewFake(interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.", update))
	env := newTestEnvWithClients(t, pf, lf)

	env.ExecuteWorkflow(MrCommentWorkflow, in)

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Processed || !result.ResponsePosted {
		t.Errorf("result = %+v, want processed with a response", result)
	}
	if result.Classification != ClassificationInline {
		t.Errorf("Classification = %q, want inline", result.Classification)
	}
	if len(result.UpdatedFiles) != 1 || result.UpdatedFiles[0] != ticket2Path {
		t.Errorf("UpdatedFiles = %v, want [%s]", result.UpdatedFiles, ticket2Path)
	}

	// The interpretation saw the resolved review comment: its real id,
	// body, and inline position.
	calls := lf.Calls()
	if len(calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(calls))
	}
	prompt := calls[0].Messages[0].Content
	for _, want := range []string{
		fmt.Sprintf("Comment #%d by", rc.ID),
		"This ticket must require MFA for the login endpoint.",
		"(The comment was posted on file " + ticket2Path + ", line 12.)",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt misses %q:\n%s", want, prompt)
		}
	}

	// The ticket update landed on the MR's branch.
	file, err := pf.GetFile(t.Context(), mrRepo, ticket2Path, refineBranch)
	if err != nil {
		t.Fatalf("updated ticket not on the refinement branch: %v", err)
	}
	if !strings.Contains(string(file.Content), "Require MFA") {
		t.Errorf("branch content misses the MFA update:\n%s", file.Content)
	}

	// The response references the real review comment id, so a redelivered
	// review event is recognized as already processed.
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want the one response", len(comments))
	}
	if !strings.Contains(comments[0].Body, responseMarkerFor(rc.ID)) {
		t.Errorf("response misses %q:\n%s", responseMarkerFor(rc.ID), comments[0].Body)
	}
}

func TestMrCommentWorkflowReviewEventMultipleComments(t *testing.T) {
	// A review submission carrying several inline comments considers every
	// one of them (TASK-18): each is interpreted, applied to its commented
	// ticket, and answered with a response whose marker references its
	// comment id. The comments are processed strictly in order, so a later
	// comment is interpreted against the content an earlier comment's
	// commit wrote.
	pf := newRefinementFake(t)
	var ids []int64
	for _, tc := range []struct {
		body string
		path string
		line int
	}{
		{"This ticket must require MFA.", ticket2Path, 12},
		{"Also cover rate limiting.", ticket3Path, 8},
		{"Document the JWT lifetime.", ticket2Path, 4},
	} {
		rc, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber, tc.body, tc.path, tc.line)
		if err != nil {
			t.Fatalf("seed review comment: %v", err)
		}
		ids = append(ids, rc.ID)
	}
	lf := llm.NewFake(
		interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.",
			TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "Require MFA on the login endpoint."}),
		interpretReply(ClassificationInline, "Extended the test ticket to cover rate limiting.",
			TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "Cover rate limiting."}),
		interpretReply(ClassificationInline, "Documented the JWT lifetime in the endpoint ticket.",
			TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFAJWT), ChangeSummary: "Document the JWT lifetime."}),
	)
	env := newTestEnvWithClients(t, pf, lf)

	in := commentInput(424242, "") // synthetic stand-in id, empty review body
	in.ReviewEvent = true
	env.ExecuteWorkflow(MrCommentWorkflow, in)

	var result MrCommentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !result.Processed || !result.ResponsePosted {
		t.Errorf("result = %+v, want processed with responses", result)
	}
	if fmt.Sprint(result.ProcessedComments) != fmt.Sprint(ids) {
		t.Errorf("ProcessedComments = %v, want %v (every review comment, oldest first)",
			result.ProcessedComments, ids)
	}
	if result.Classification != ClassificationInline {
		t.Errorf("Classification = %q, want inline", result.Classification)
	}
	wantFiles := []string{ticket2Path, ticket3Path, ticket2Path}
	if fmt.Sprint(result.UpdatedFiles) != fmt.Sprint(wantFiles) {
		t.Errorf("UpdatedFiles = %v, want %v", result.UpdatedFiles, wantFiles)
	}
	if result.CommitSHA == "" {
		t.Error("CommitSHA is empty")
	}
	if result.Status != "processed 3 comments: 3 inline" {
		t.Errorf("Status = %q, want the multi-comment summary", result.Status)
	}

	// Every comment was interpreted, in order, each against the branch as
	// the previous comment's commit left it — and each with its own
	// inline position, which constrains its update to that ticket.
	calls := lf.Calls()
	if len(calls) != 3 {
		t.Fatalf("llm calls = %d, want 3", len(calls))
	}
	prompts := []string{calls[0].Messages[0].Content, calls[1].Messages[0].Content, calls[2].Messages[0].Content}
	for i, want := range []string{
		"This ticket must require MFA.",
		"Also cover rate limiting.",
		"Document the JWT lifetime.",
	} {
		if !strings.Contains(prompts[i], want) {
			t.Errorf("prompt %d misses its comment %q:\n%s", i, want, prompts[i])
		}
	}
	for i, want := range []string{
		"(The comment was posted on file " + ticket2Path + ", line 12.)",
		"(The comment was posted on file " + ticket3Path + ", line 8.)",
		"(The comment was posted on file " + ticket2Path + ", line 4.)",
	} {
		if !strings.Contains(prompts[i], want) {
			t.Errorf("prompt %d misses the inline position %q:\n%s", i, want, prompts[i])
		}
	}
	if !strings.Contains(prompts[1], "Require MFA") {
		t.Errorf("second prompt misses the first comment's applied update:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[2], "Require MFA") || !strings.Contains(prompts[2], "rate limiting") {
		t.Errorf("third prompt misses the earlier comments' applied updates:\n%s", prompts[2])
	}

	// All updates landed on the MR's branch.
	for path, want := range map[string]string{
		ticket2Path: "JWT lifetime",
		ticket3Path: "rate limiting",
	} {
		file, err := pf.GetFile(t.Context(), mrRepo, path, refineBranch)
		if err != nil {
			t.Fatalf("ticket %s not on the branch: %v", path, err)
		}
		if !strings.Contains(string(file.Content), want) {
			t.Errorf("ticket %s misses %q:\n%s", path, want, file.Content)
		}
	}

	// One response per comment, each marker referencing that comment's real
	// id, and each naming its own commit.
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("comments = %d, want one response per review comment", len(comments))
	}
	shas := make(map[string]bool, len(comments))
	for i, c := range comments {
		if !strings.Contains(c.Body, responseMarkerFor(ids[i])) {
			t.Errorf("response %d misses the marker for comment %d:\n%s", i, ids[i], c.Body)
		}
		// "... as `<sha>`." names the commit this comment's updates went
		// in as; each comment got its own.
		if _, rest, ok := strings.Cut(c.Body, " as `"); ok {
			sha, _, _ := strings.Cut(rest, "`")
			if sha == "" {
				t.Errorf("response %d names no commit:\n%s", i, c.Body)
			}
			shas[sha] = true
		} else {
			t.Errorf("response %d names no commit:\n%s", i, c.Body)
		}
	}
	if len(shas) != len(comments) {
		t.Errorf("distinct commits named = %d, want %d (one commit per comment)", len(shas), len(comments))
	}
}

func TestMrCommentWorkflowLaterEventPicksUpPostReviewComments(t *testing.T) {
	// Inline comments added to a review after its submission event fired
	// get no webhook of their own (forgejo#7966): the next event on the
	// MR — here a plain conversation comment — rescans and processes
	// them, before the new comment itself.
	pf := newRefinementFake(t)
	early, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"This ticket must require MFA.", ticket2Path, 12)
	if err != nil {
		t.Fatalf("seed review comment: %v", err)
	}
	lf := llm.NewFake(
		interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.",
			TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "Require MFA."}),
		interpretReply(ClassificationInline, "Extended the test ticket to cover rate limiting.",
			TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "Cover rate limiting."}),
		interpretReply(ClassificationNone, "Happy to clarify: the JWT expires after 24 hours."),
	)

	// The review's own submission event processed its one comment.
	reviewIn := commentInput(424242, "")
	reviewIn.ReviewEvent = true
	env := newTestEnvWithClients(t, pf, lf)
	env.ExecuteWorkflow(MrCommentWorkflow, reviewIn)
	var first MrCommentResult
	if err := env.GetWorkflowResult(&first); err != nil {
		t.Fatalf("first workflow failed: %v", err)
	}
	if !first.Processed || len(first.ProcessedComments) != 1 || first.ProcessedComments[0] != early.ID {
		t.Fatalf("first result = %+v, want the review comment processed", first)
	}

	// A comment is then added to the review — Forgejo fires no webhook for
	// it. The next comment event on the MR must pick it up.
	late, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"Also cover rate limiting.", ticket3Path, 8)
	if err != nil {
		t.Fatalf("seed post-review comment: %v", err)
	}
	env = newTestEnvWithClients(t, pf, lf)
	env.ExecuteWorkflow(MrCommentWorkflow, commentInput(100, "When does the JWT expire?"))
	var second MrCommentResult
	if err := env.GetWorkflowResult(&second); err != nil {
		t.Fatalf("second workflow failed: %v", err)
	}
	if !second.Processed {
		t.Fatal("second result.Processed = false, want true")
	}
	if fmt.Sprint(second.ProcessedComments) != fmt.Sprint([]int64{late.ID, 100}) {
		t.Errorf("ProcessedComments = %v, want the pending review comment %d then the new comment 100",
			second.ProcessedComments, late.ID)
	}

	// The pending review comment ran first, with its inline position; the
	// plain comment after it, interpreted against the content the review
	// comment's commit wrote.
	calls := lf.Calls()
	if len(calls) != 3 {
		t.Fatalf("llm calls = %d, want 3", len(calls))
	}
	if prompt := calls[1].Messages[0].Content; !strings.Contains(prompt, "Also cover rate limiting.") ||
		!strings.Contains(prompt, "(The comment was posted on file "+ticket3Path+", line 8.)") {
		t.Errorf("the rescan prompt misses the pending review comment with its position:\n%s", prompt)
	}
	prompt := calls[2].Messages[0].Content
	if !strings.Contains(prompt, "When does the JWT expire?") {
		t.Errorf("the last prompt misses the new comment:\n%s", prompt)
	}
	if !strings.Contains(prompt, "rate limiting") {
		t.Errorf("the last prompt misses the rescan's applied update (not sequential):\n%s", prompt)
	}

	// Three responses on the MR, one per processed comment, each with its
	// own marker; the tickets carry both changes.
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("comments = %d, want three responses", len(comments))
	}
	bodies := comments[0].Body + "\n" + comments[1].Body + "\n" + comments[2].Body
	for _, marker := range []string{responseMarkerFor(early.ID), responseMarkerFor(late.ID), responseMarkerFor(100)} {
		if !strings.Contains(bodies, marker) {
			t.Errorf("responses miss %q:\n%s", marker, bodies)
		}
	}
	for path, want := range map[string]string{
		ticket2Path: "Require MFA",
		ticket3Path: "rate limiting",
	} {
		file, err := pf.GetFile(t.Context(), mrRepo, path, refineBranch)
		if err != nil {
			t.Fatalf("ticket %s not on the branch: %v", path, err)
		}
		if !strings.Contains(string(file.Content), want) {
			t.Errorf("ticket %s misses %q:\n%s", path, want, file.Content)
		}
	}
}

func TestMrCommentWorkflowReviewEventRedeliveryIsIdempotent(t *testing.T) {
	// A redelivered review event re-runs (after a failure, or once the
	// delivery dedupe window has passed): every review comment an earlier
	// run answered is skipped via its real comment id — no second
	// response, commit, or interpretation.
	pf := newRefinementFake(t)
	first, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"This ticket must require MFA.", ticket2Path, 12)
	if err != nil {
		t.Fatalf("seed first review comment: %v", err)
	}
	second, err := pf.AddMergeRequestReviewComment(t.Context(), mrRepo, mrNumber,
		"Also cover rate limiting.", ticket3Path, 8)
	if err != nil {
		t.Fatalf("seed second review comment: %v", err)
	}
	lf := llm.NewFake(
		interpretReply(ClassificationInline, "Updated the endpoint ticket to require MFA.",
			TicketUpdate{Path: ticket2Path, Content: ticketFile(2, "Add login endpoint", ticket2MFA), ChangeSummary: "Require MFA."}),
		interpretReply(ClassificationInline, "Extended the test ticket to cover rate limiting.",
			TicketUpdate{Path: ticket3Path, Content: ticketFile(3, "Add login tests", ticket3Rate), ChangeSummary: "Cover rate limiting."}),
	)
	in := commentInput(424242, "")
	in.ReviewEvent = true

	env := newTestEnvWithClients(t, pf, lf)
	env.ExecuteWorkflow(MrCommentWorkflow, in)
	var firstRun MrCommentResult
	if err := env.GetWorkflowResult(&firstRun); err != nil {
		t.Fatalf("first workflow failed: %v", err)
	}
	if !firstRun.Processed ||
		fmt.Sprint(firstRun.ProcessedComments) != fmt.Sprint([]int64{first.ID, second.ID}) {
		t.Fatalf("first result = %+v, want both review comments processed", firstRun)
	}

	// Redelivery of the same review event: everything is already answered.
	env = newTestEnvWithClients(t, pf, lf)
	env.ExecuteWorkflow(MrCommentWorkflow, in)
	var redelivered MrCommentResult
	if err := env.GetWorkflowResult(&redelivered); err != nil {
		t.Fatalf("redelivered workflow failed: %v", err)
	}
	if redelivered.Processed {
		t.Error("redelivered run processed a comment, want none")
	}
	if !redelivered.AlreadyProcessed {
		t.Error("redelivered run is not marked already processed")
	}
	if len(lf.Calls()) != 2 {
		t.Errorf("llm calls = %d, want 2 (nothing re-interpreted)", len(lf.Calls()))
	}
	comments, err := pf.ListMergeRequestComments(t.Context(), mrRepo, mrNumber)
	if err != nil {
		t.Fatalf("ListMergeRequestComments: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("comments = %d, want only the two earlier responses", len(comments))
	}
}
