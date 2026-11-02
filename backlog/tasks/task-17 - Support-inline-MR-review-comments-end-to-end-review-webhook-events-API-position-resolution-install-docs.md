---
id: TASK-17
title: >-
  Support inline MR review comments end-to-end (review webhook events, API
  position resolution, install docs)
status: Done
assignee:
  - '@agent'
created_date: '2026-11-02 09:53'
updated_date: '2026-11-02 10:04'
labels: []
dependencies:
  - TASK-11
modified_files:
  - src/internal/event/event.go
  - src/internal/source/webhook/forgejo.go
  - src/internal/source/webhook/forgejo_test.go
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/internal/provider/forgejo_test.go
  - src/internal/provider/fake.go
  - src/internal/worker/userstoryreview/mrcomment.go
  - src/internal/worker/userstoryreview/mrcomment_activities.go
  - src/internal/worker/userstoryreview/mrcomment_test.go
  - src/README.md
type: bug
ordinal: 16000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Inline (diff) comments on refinement MRs never reach the comment loop. Forgejo delivers a review submission as a pull_request_comment / pull_request_approved / pull_request_rejected webhook with action "reviewed" and a PullRequestPayload (no comment object, no position; review.content empty for comment-only reviews — forgejo#7935/#7966), which the adapter currently drops ("ignoring comment action action=reviewed", events=0). Individual inline comments left during a review fire no per-comment webhook at all; a single inline comment fires issue_comment without path/line, so its position is lost and the LLM misclassifies it as summary feedback (observed on BeanieGenie MR !12: comment 37 never processed, comment 39 processed with no position and 0 ticket changes). The fix: parse review events in the Forgejo adapter (flagged, stable synthetic id for dedupe), resolve real inline comments and their path/line through the pulls reviews API in the comment workflow, constrain inline updates to the commented file, and document that the repo webhook must enable both pull request comment and pull request review events.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Forgejo adapter parses review-submission deliveries (headers pull_request_comment/pull_request_approved/pull_request_rejected, action reviewed) into comment events flagged as review events, with a stable synthetic comment ID for dedupe
- [x] #2 Review events with an empty review body still dispatch (the workflow resolves the review comments via the provider API)
- [x] #3 Provider client lists MR review comments with path/line via the pulls reviews API; provider.Comment carries Path/Line
- [x] #4 The comment workflow resolves the inline position via the API for review events and for inline issue_comment events, and constrains inline updates to the commented ticket file
- [x] #5 For review-submission events the workflow processes the oldest unanswered review comment on the MR, with the already-processed guard keyed by the real comment id
- [x] #6 Unit tests cover review-event parsing, position resolution, and the new workflow paths; go build, go vet, go test ./... green
- [x] #7 src/README.md wiring docs list the required webhook events: push, issue_comment, pull_request_comment, and pull_request_review_comment/approved/rejected
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1) event model: add Comment.Review flag for review-submission deliveries. 2) webhook adapter: parse pull_request_comment/pull_request_approved/pull_request_rejected (action reviewed, PullRequestPayload with review{type,content}) into review-flagged comment events with a stable synthetic comment id (hash of the raw payload) for dedupe. 3) provider client: add ListMergeRequestReviewComments (pulls/{n}/reviews + reviews/{id}/comments, skipping dismissed) and Path/Line on Comment; implement in ForgejoClient and Fake. 4) comment workflow: MrCommentInput gains ReviewEvent; FindRefinement resolves the real comment via the API — inline position for known comment ids, oldest unanswered review comment for review events, review body as summary fallback — and the already-processed guard keys on the resolved id; the processor feeds the resolved comment (id/body/position) into interpretation, validation, apply, and the response marker. 5) Tests: adapter review parsing, provider review-comment listing, trigger/validate, FindRefinement resolution paths, workflow end-to-end review-event path. 6) Docs: src/README.md wiring section lists the required webhook events (push, issue_comment, pull_request_comment, pull_request_review_comment/approved/rejected) and notes inline positions are resolved via the API.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implementation: (1) event.Comment gained a Review flag for review-submission deliveries. (2) The Forgejo webhook adapter now routes the pull_request_comment / pull_request_approved / pull_request_rejected headers (action "reviewed", PullRequestPayload with a review object) to a new parser that emits a review-flagged comment event; since the payload has no comment id, the event gets a stable synthetic id (SHA-256 of the raw payload, sign bit cleared) so dedupe still collapses redeliveries. (3) provider.Client gained ListMergeRequestReviewComments (GET pulls/{n}/reviews + reviews/{id}/comments, dismissed reviews skipped, oldest first) and Comment gained Path/Line; implemented in ForgejoClient and Fake (AddMergeRequestReviewComment seeds inline comments in tests). (4) FindRefinement now resolves the real comment through the API: for a plain comment event it looks the id up among the MR review comments to fill the inline position; for a review event it picks the oldest unanswered review comment (or falls back to the review summary body); the already-processed guard keys on the resolved real id. The processor feeds the resolved id/body/position into interpretation, validation (inline position constrains the update target), the commit message, and the response marker. (5) src/README.md wiring section now requires push + issue_comment + pull_request_comment + pull_request_review_* events and documents the API-based position resolution. Verified live on the dev Forgejo (16.0.3): redelivered the review webhook for BeanieGenie MR !12 -> dispatched:1 (was events=0 with "ignoring comment action action=reviewed"), mrCommentWorkflow ran with the synthetic id, resolved review comment 37 ("Not needed, leave it out." on task-012 file, position via API), classified inline, committed 18e1a65 to the MR branch updating only that ticket, posted response comment 54 referencing comment 37. Also fixed a pre-existing flake in TestMrCommentWorkflowConcurrentCommentsOnOneMR: which of two concurrently started comments wins the per-MR slot is nondeterministic, so the assertions are now order-independent while still proving serialization (the second run sees the first commit).
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Inline MR review comments now flow through the comment loop end-to-end. Root cause: Forgejo delivers a review submission as a pull_request_comment/approved/rejected webhook with action "reviewed" and a review object — no comment object, no inline position (forgejo#7935/#7966) — and the adapter dropped anything but action=created, so the delivery produced events=0 ("ignoring comment action action=reviewed"). Individual inline comments left during a review fire no per-comment webhook at all; a single inline comment fires issue_comment without path/line, so its position was lost and the LLM misclassified it as summary feedback with 0 ticket changes. Changes: the Forgejo adapter parses review deliveries into review-flagged comment events with a stable synthetic comment id (payload hash) for dedupe; the provider client lists MR review comments with path/line via the pulls reviews API (ListMergeRequestReviewComments, Comment.Path/Line, Fake support); FindRefinement resolves the real comment through the API — inline position for known comment ids, oldest unanswered review comment for review events, review summary body as fallback — and the already-processed guard plus response marker use the real comment id; the trigger/validation accept review events with an empty body; src/README.md wiring docs now list the required webhook events (push, issue_comment, pull_request_comment, pull_request_review_comment/approved/rejected) and note the API-based position resolution. Verification: go build/vet/test ./... green, go test -race -count=2 on the touched packages green (plus -count=10 on the previously flaky concurrent test, whose order-dependent assertions were made order-independent). Live proof on the dev instance: redelivered MR !12 review webhook -> dispatched:1, workflow resolved inline comment 37 with its file position, classified inline, committed the ticket update to the MR branch (18e1a65) and posted a response referencing comment 37. Follow-up note: the LLM interpretation of "leave it out" appended a note instead of removing the ticket — interpretation quality, not a mechanism issue; and Forgejo still delivers no inline positions in webhooks, so resolution always goes through the API.
<!-- SECTION:FINAL_SUMMARY:END -->
