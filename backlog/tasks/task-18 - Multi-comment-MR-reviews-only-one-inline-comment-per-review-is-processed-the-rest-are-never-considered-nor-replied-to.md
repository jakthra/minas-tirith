---
id: TASK-18
title: >-
  Multi-comment MR reviews: only one inline comment per review is processed; the
  rest are never considered nor replied to
status: Done
assignee:
  - '@agent'
created_date: '2026-11-02 10:07'
updated_date: '2026-11-02 10:19'
labels: []
dependencies:
  - TASK-17
references:
  - TASK-17
  - forgejo#7966 (no webhook events for individual review comments)
  - forgejo#7935 (review webhook payload lacks comment content/position)
  - 'http://localhost:3000/root/BeanieGenie/pulls/13 (live repro)'
modified_files:
  - src/internal/worker/userstoryreview/mrcomment_activities.go
  - src/internal/worker/userstoryreview/mrcomment.go
  - src/internal/worker/userstoryreview/mrcomment_test.go
  - src/README.md
  - src/internal/event/event.go
type: bug
ordinal: 17000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Symptom (live repro on the dev Forgejo, MR !13 "Refine TASK-012", review 2 with 5 inline comments): only comments 44 and 52 were considered and replied to (agent responses are issue comments 49 and 53; branch commits 0944a05b and e3a86961). Inline comments 45 ("no need for this", on backlog/tasks/task-014), 46 ("leave out notes", task-015) and 47 ("leave out notes", task-016) got NO response and NO ticket change. Root cause is two-fold: (1) Forgejo fires exactly ONE review-submission webhook per review and NO per-comment webhook for comments left during the review (forgejo#7966), and the review payload names no comments (forgejo#7935); (2) the TASK-17 implementation resolves only the single oldest unanswered review comment per review event, so every other comment in the same review is left unconsidered. Single-comment flows work since TASK-17 (MR !12 review comments 37/39/41 all answered), so this is specifically the multi-comment review case plus comments added to a review after its submission event fired. The agent must pick up ALL unanswered inline review comments on a refinement MR — e.g. when a review event arrives, process every unanswered review comment (not just the oldest), and when any later comment/review event arrives on the same MR, rescan and process review comments that arrived after the last event (no webhook will ever fire for them).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A review submission carrying multiple inline comments results in every one of them being considered: each gets an agent response comment whose marker references that comment id (responseMarkerFor)
- [x] #2 Each considered inline comment produces its ticket updates committed to the MR branch, constrained to the commented ticket file when the comment has a position
- [x] #3 Inline review comments added after the review-submission event fired (no further webhook, forgejo#7966) are still processed when any later comment or review event arrives on the same MR
- [x] #4 Redelivery of a review event is idempotent: already-answered review comments are skipped via the real comment id
- [x] #5 Sequential per-MR processing still holds: no concurrent updates to the same branch while looping over several comments
- [x] #6 Unit tests cover multi-comment review events, the rescan path, and idempotency; go build, go vet, go test ./... green
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1) FindRefinement resolves ALL comments one event must process instead of one: FindRefinementOutput.Comment *ResolvedComment becomes Comments []ResolvedComment. For a review event: every review comment no earlier run answered, oldest first; the review summary body stays the fallback for reviews that carry no review comments at all, now guarded by answered(synthetic id) so redelivery stays idempotent. For a plain comment event: every unanswered review comment (the rescan path — Forgejo fires no per-comment webhook for review comments, forgejo#7966) oldest first, then the event's comment with its position looked up among the review comments (the event's own position, when the provider reported one, wins — PositionFile/Line move into FindRefinementInput). Empty list = AlreadyProcessed.

2) MrCommentProcessorWorkflow loops over the resolved comments strictly one after another inside the per-MR processor child workflow (per-MR serialization, AC5): per comment CollectTickets (re-reads the branch so a later comment sees the earlier comment's commit), InterpretComment, validatedUpdates (position constrains the update target), ApplyTicketUpdates, PostResponseComment with the marker for that comment id. A failure mid-loop retries the run; answered comments are skipped on the retry (AC4).

3) MrCommentResult aggregates the run: new ProcessedComments []int64 lists the answered comment ids, UpdatedFiles is the union in order, CommitSHA the last commit, Classification the last comment's, Status via a runStatus helper (single-comment runs keep classificationStatus).

4) Tests: FindRefinement multi-comment resolution, rescan on plain comment events, summary-fallback idempotency; workflow end-to-end multi-comment review (one response per comment, each marker referencing its real comment id, updates constrained to the commented ticket, sequential interpretation seeing earlier commits); rescan end-to-end (comment added to a review after its event, picked up by a later comment event); redelivery idempotency (already-answered review comments skipped, no new LLM calls/commits/responses). src/README.md gains a sentence on the multi-comment behavior. go build, go vet, go test ./... green.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implementation: (1) FindRefinement resolves every comment one event must process instead of one: FindRefinementOutput.Comment became Comments []ResolvedComment. A review event resolves every review comment no earlier run answered, oldest first (provider listings are oldest-first); a plain comment event resolves those plus the event's comment — review comments first (they predate the event; Forgejo fires no per-comment webhook for comments left during a review, forgejo#7966, so any later event is the only trigger that can pick them up), the event's comment last, not duplicated when it is itself a review comment, and with the event's own position (now carried in FindRefinementInput) preferred over the API lookup. The review summary body stays the fallback for reviews that carry no review comments at all, now guarded by answered(synthetic id) — a pre-existing gap: a redelivered comment-less review whose summary was already responded used to re-process it. Empty resolution = AlreadyProcessed. (2) MrCommentProcessorWorkflow processes the resolved comments in a plain loop inside the per-MR processor child workflow (one execution per MR at a time, so AC5 serialization holds): per comment CollectTickets (re-reads the branch so a later comment is interpreted against the earlier comment's commit), InterpretComment, validatedUpdates (the position constrains the update target), ApplyTicketUpdates (one commit per comment, named in that comment's response), PostResponseComment with responseMarkerFor(rc.ID). A failure mid-loop fails the run for retry; on the retry, FindRefinement skips the comments whose responses landed. (3) MrCommentResult aggregates the run: ProcessedComments lists the answered ids, UpdatedFiles the union, CommitSHA the last commit, Classification the last comment's, Status via runStatus (a single-comment run keeps classificationStatus; several read 'processed N comments: ...'). Also a gofmt-only alignment fix in event.go, which TASK-17's Review field had left misaligned.

Verification: go build/vet/test ./... green; go test -race -count=2 on the userstoryreview package green. New tests: TestFindRefinementReviewEventMultipleComments (multi-comment resolution, answered middle comment skipped by real id), TestFindRefinementRescansReviewComments (plain-comment rescan, no duplication when the event comment is a review comment, answered event comment still rescans), TestMrCommentWorkflowReviewEventMultipleComments (end-to-end: 3 inline comments -> 3 responses each with its marker and its own commit, per-comment positions in the prompts, sequential interpretation seeing earlier commits), TestMrCommentWorkflowLaterEventPicksUpPostReviewComments (post-review comment picked up by a later plain comment event, rescan before the new comment), TestMrCommentWorkflowReviewEventRedeliveryIsIdempotent (redelivery -> AlreadyProcessed, no new calls/responses). Updated: TestFindRefinementReviewEvent (list shape + comment-less review summary now idempotent under redelivery), TestFindRefinementResolvesInlinePosition (event-carried position wins over the API lookup). Live: the fix was deployed and verified on the dev Forgejo repro (root/BeanieGenie MR !13, review 2: previously unanswered inline comments 45/46/47) by the user — confirmed working.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Multi-comment MR reviews are now processed in full: every unresolved inline review comment is considered, applied, and answered. Root cause: Forgejo fires exactly one review-submission webhook per review and names no comments in the payload (forgejo#7935/#7966), and the TASK-17 implementation resolved only the single oldest unanswered review comment per event — every other comment of the same review (and any comment added to a review after its event fired, which never gets a webhook at all) was left unconsidered. Changes: FindRefinement now returns the full list of comments one event must process (FindRefinementOutput.Comments) — for a review event every unanswered review comment, oldest first; for a plain comment event those plus the event's own comment (rescan before the new comment, no duplication when the event's comment is itself a review comment, event-carried position preferred over the API lookup via new FindRefinementInput position fields); the review summary body remains the fallback for reviews with no review comments, now guarded by the synthetic id's answered check so comment-less review redeliveries are idempotent too; an empty resolution is AlreadyProcessed. MrCommentProcessorWorkflow loops over the resolved comments inside the per-MR processor child workflow, so per-MR serialization is unchanged: each comment gets its own collect/interpret/validate/apply/respond round, with a fresh ticket collection per comment (a later comment is interpreted against the earlier comment's commit), its own commit named in its own response, and the response marker referencing its real comment id; a mid-loop failure retries the run and skips the comments whose responses already landed. MrCommentResult aggregates the run (ProcessedComments ids, UpdatedFiles union, last CommitSHA, runStatus for one or many). Verification: go build/vet/test ./... green, go test -race -count=2 green on the package; new unit tests cover multi-comment review events (3 inline comments -> 3 responses with per-comment markers, positions, and distinct commits, sequential prompts seeing earlier commits), the rescan path (post-review comment picked up by a later plain comment event), and redelivery idempotency (no re-interpretation, response, or commit); existing review-event, sequential, and concurrency tests updated and passing. The user deployed the fix and confirmed it live on the dev Forgejo repro (BeanieGenie MR !13): the previously unanswered inline comments 45/46/47 are now picked up and replied to. Also included: a gofmt-only alignment fix in event.go left misaligned by TASK-17's Review field.
<!-- SECTION:FINAL_SUMMARY:END -->
