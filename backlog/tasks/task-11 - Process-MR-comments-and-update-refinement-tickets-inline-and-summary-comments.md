---
id: TASK-11
title: >-
  Process MR comments and update refinement tickets (inline and summary
  comments)
status: Done
assignee:
  - '@agent'
created_date: '2026-10-27 10:54'
updated_date: '2026-11-02 09:39'
labels: []
dependencies:
  - TASK-8
  - TASK-9
  - TASK-10
references:
  - 'TASK-2: Autonomous refinement MR/PR creation agent'
  - >-
    TASK-8: Submit MR with refined implementation tickets from
    userStoryReviewWorkflow
  - userstoryreview
  - event
  - forgejo-adapter
  - src/internal/worker/userstoryreview/mrcomment.go
  - src/internal/worker/userstoryreview/mrcomment_activities.go
modified_files:
  - src/internal/event/event.go
  - src/internal/source/webhook/forgejo.go
  - src/internal/worker/userstoryreview/mrcomment.go
  - src/internal/worker/userstoryreview/mrcomment_activities.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/userstoryreview/workflow.go
  - src/internal/worker/userstoryreview/mrcomment_test.go
  - src/internal/source/webhook/forgejo_test.go
type: feature
ordinal: 10000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-8 creates an MR with refined implementation tickets. Step 4 of the TASK-2 flow requires the agent to respond to user comments on that MR — both inline comments (on specific ticket files) and summary comments (on the MR overall). The user uses the MR as the entrypoint for adjusting the refinement; the agent must apply that feedback and update the tickets.

This task implements the comment-handling loop: a new workflow triggered by comment events on MRs created by the refinement agent. The workflow interprets the comment (using the LLM client from TASK-10), updates the relevant tickets, commits the changes to the MR branch, and posts a response comment summarizing what changed.

The workflow must filter for only agent-created MRs (not all MRs in the repository) and correlate comment events back to the original refinement workflow execution for state tracking.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A new workflow (e.g., `mrCommentWorkflow`) is registered in a new or existing template and triggered by comment events on agent-created MRs
- [x] #2 The trigger filters for only MRs created by the refinement agent (via label, author, or title pattern)
- [x] #3 The workflow distinguishes inline comments (on specific files/lines) from summary comments (on the MR overall) and handles each appropriately
- [x] #4 For inline comments: the workflow identifies the ticket file referenced by the comment, fetches its current content, and updates it based on the feedback
- [x] #5 For summary comments: the workflow interprets the feedback as applying to the overall refinement and updates tickets accordingly
- [x] #6 Updated tickets are committed to the same MR branch (not a new branch)
- [x] #7 The workflow posts a response comment on the MR summarizing what was changed and why
- [x] #8 The workflow uses the LLM client (TASK-10) inside a Temporal activity to interpret comments and generate updates (determinism preserved)
- [x] #9 The workflow correlates the comment event to the original refinement workflow execution (via MR number stored in TASK-8 result)
- [x] #10 Multiple comments on the same MR are handled sequentially (no concurrent updates to the same branch)
- [x] #11 Unit tests cover comment parsing (inline vs summary), ticket update logic with mocked LLM, workflow happy path, and error handling
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Extend the userstoryreview template with the MR comment loop (TASK-2 step 4): 1) event model: add optional Position (file/line) to event.Comment — the provider-agnostic inline-comment location; Forgejo webhooks deliver no inline comment locations today (forgejo#7935/#7966), so the adapter parses the fields only when present and inline-ness is otherwise interpreted from the comment body. 2) New workflows in the userstoryreview package: mrCommentWorkflow (triggered per comment event, per-comment workflow ID from the bridge, so redelivery stays idempotent) which acquires a per-MR serialization slot and delegates to mrCommentProcessorWorkflow (child workflow, per-MR workflow ID, ALLOW_DUPLICATE reuse, already-started retry loop) — Temporal workflow-ID uniqueness guarantees sequential handling of comments on the same MR, no concurrent branch updates. 3) Activities (provider/LLM calls out of the workflow body, determinism preserved): findRefinement (correlates the comment event to the TASK-8 refinement run via MR number: GetMergeRequest, verify refine/* head branch, recover the original commit SHA prefix and workflow-ID prefix from the branch name; also lists MR comments to skip comments the agent already responded to), collectTickets (list backlog/tasks on the refinement branch minus the base branch = the MR's ticket files; fetch current content), interpretComment (LLM: classify inline vs summary vs non-actionable, produce per-ticket full-content updates + response prose), applyTicketUpdates (validate updates against the ticket set and preserved frontmatter ids, bump updated_date, commit to the SAME refinement branch), postResponseComment (deterministically composed summary carrying the agent-response marker so the trigger declines the agent's own comments). 4) Template: register both workflows + 5 activities + a comment-event trigger whose input builder filters for agent-created MRs (title pattern 'Refine ' from buildMRTitle) and declines agent-marker comments. 5) Unit tests: trigger filtering, inline/summary parsing and interpretation with mocked LLM, update validation, workflow happy path, error handling, already-processed guard, and sequential processing (same-MR reuse + mutex conflict path).
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Resumed after token exhaustion; implementation files were already in place, what was missing is now complete: (1) Fixed a correlation bug in refinementWorkflowIDPrefix — the prefix omitted the 'commit' segment of the event bridge's workflow-ID format (<workflow>-<provider>|<repo>|commit|<sha> from bridge.go + Event.DedupeKey), so the reconstructed refinement workflow-ID prefix could never match the originating review execution. (2) Added the full comment-loop unit test suite (mrcomment_test.go, 40 tests): trigger filtering (refinement title + agent-response marker), inline/summary/none interpretation with the mocked LLM (incl. prompt assertions for inline positions), update validation (validatedUpdates, updatedTicketContent, bumpUpdatedDate), workflow happy paths (inline/summary/non-actionable), error handling (findRefinement, collectTickets, interpret, apply failures; update for non-ticket file; invalid inputs), the already-processed guard, and sequential per-MR processing — both same-MR processor reuse and the concurrent mutex-conflict path (the Temporal test env returns WorkflowExecutionAlreadyStarted for the second comment's processor; it waits with backoff and runs strictly after the first, proven by the second interpretation prompt carrying the first comment's committed update). (3) Added forgejo adapter tests for inline-comment position parsing (path/line parsed only when a delivery carries them; summary comments carry no position). Verified: go build, go vet, go test ./... all green; go test -race -count=2 on the touched packages green.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Completed the MR comment loop for refinement tickets (TASK-2 step 4). The implementation (event.Comment.Position model, Forgejo position parsing, MrCommentWorkflow + per-MR MrCommentProcessorWorkflow with slot acquisition/backoff, the findRefinement/collectTickets/interpretComment/applyTicketUpdates/postResponseComment activities, and the template's comment trigger) was already in place from the interrupted session; this session fixed one bug and supplied the missing test coverage.

Bug fixed: refinementWorkflowIDPrefix omitted the 'commit' segment of the bridge's workflow-ID format, so the correlation prefix (AC9) could never match the originating review execution's ID (<workflow>-<provider>|<repo>|commit|<sha>).

Tests added (mrcomment_test.go, 40 tests; forgejo_test.go position tests): trigger filtering for agent-created MRs (title pattern + agent-response marker), inline/summary/none interpretation with the mocked LLM (prompt carries the inline position and current ticket contents), deterministic update validation (only the MR's tickets, id/frontmatter preserved, updated_date bumped, inline position constrains the target), workflow happy paths, error handling for every activity failure, the already-processed guard against double application, and sequential per-MR processing — including the concurrent mutex-conflict path, where the test env's WorkflowExecutionAlreadyStarted forces the second comment to wait and its interpretation provably runs after the first comment's commit landed.

Verification: go build ./..., go vet ./..., go test ./... all green; go test -race -count=2 on userstoryreview and source/webhook green. Risks/follow-ups: Forgejo webhooks still deliver no inline positions (forgejo#7935/#7966), so inline-ness is inferred from the comment body until upstream ships the fields; the slot wait limit (1h) bounds a wedged processor — redelivery re-runs failed comment workflows via the per-comment workflow ID.
<!-- SECTION:FINAL_SUMMARY:END -->
