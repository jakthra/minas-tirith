---
id: TASK-24
title: >-
  Submit the implementation MR, or a draft MR with refiner hand-off when the
  task cannot be fulfilled
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-02 13:45'
updated_date: '2026-11-02 14:19'
labels: []
milestone: m-0
dependencies:
  - TASK-23
references:
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/provider/provider.go
  - src/internal/worker/userstoryreview/mrcomment.go
  - src/internal/worker/userstoryreview/template.go
  - src/README.md
modified_files:
  - src/internal/worker/taskimplementation/mr.go
  - src/internal/worker/taskimplementation/mrsubmission.go
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
  - src/internal/worker/taskimplementation/mr_test.go
  - src/README.md
type: feature
ordinal: 23000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
This ticket delivers TASK-3 AC1–AC3, the visible output of the implementation agent: an MR tagged with the ticket number when the implementation is complete, and a *draft* MR plus a refiner hand-off when it is not. TASK-23 produces the implementation and reports whether it is complete or partial; this ticket turns that outcome into the merge request a human sees.

The refiner hand-off is the part that makes a partial implementation useful rather than a dead end. TASK-3 requires the agent to "include the refiner agent for further refinement if condition of AC2 is met", so a partial implementation must be routed back to the TASK-2 refinement agent with the agent's open questions, rather than left for a human to notice. The refinement agent already reacts to comments on merge requests (TASK-11) and generates tickets from a story (TASK-7), so the hand-off should reuse that path instead of inventing a second refinement mechanism.

**Related information:**
- `src/internal/worker/taskimplementation/workflow.go` — `Result.MergeRequestURL` is the field this ticket fills in; TASK-23 supplies the complete/partial outcome and branch.
- `src/internal/worker/userstoryreview/activities.go` — `OpenMergeRequest`, `renderTicketFile` and the MR title/description builders are the existing MR-submission pattern to follow.
- `src/internal/provider/provider.go` — `CreateMergeRequest`, extended with draft support by TASK-21.
- `src/internal/worker/userstoryreview/mrcomment.go` — the refinement agent's comment loop (TASK-11); the refiner hand-off should reach this agent through it.
- `src/internal/worker/userstoryreview/template.go` — the review agent's triggers, including the comment trigger that starts a refinement round.
- TASK-8 — delivered the refinement agent's MR submission, which this mirrors.
- TASK-3 AC1/AC2/AC3 — the acceptance criteria this ticket completes.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A complete implementation opens a non-draft MR from the `implement/*` branch to the branch the task commit landed on, with the ticket number in the title and the implemented task linked in the description
- [x] #2 A partial implementation opens a draft MR containing the work done so far, with the reason it could not be fulfilled and the open questions in the description
- [x] #3 A partial implementation hands the task off to the refiner agent by tagging it on the draft MR, so a refinement round starts for the open questions
- [x] #4 A complete implementation does not involve the refiner agent
- [x] #5 The workflow result carries the MR URL for both outcomes, and logs which outcome was chosen
- [x] #6 Workflow-level tests cover a complete implementation producing a non-draft MR and a partial one producing a draft MR with the refiner tagged, asserting draft state, title, and description
- [x] #7 The README documents the implementation agent end to end: trigger, prompt, branch, MR, and the draft/refiner fallback
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add the MR submission step to taskimplementation: a SubmitMergeRequest activity that opens a regular MR for a complete implementation and a draft MR for a partial one (title carries the task id and title, description links the task file and, when partial, states the reason and the open questions).
2. Add the refiner hand-off: for a partial implementation the activity posts a comment on the draft MR that tags the refiner agent (RefinerAgentHandle) with the open questions, reusing the refinement agent's existing MR comment loop (TASK-11) instead of a second refinement mechanism. Complete implementations post no comment.
3. Build the MR title/description in a pure, tested function (mr.go) following the refinement agent's buildMRTitle/buildMRDescription pattern; base the MR on the branch the task commit landed on.
4. Workflow: after implementing all tasks, open one MR per implementation (the first implementation's MR URL fills Result.MergeRequestURL), log the chosen outcome (complete/draft) per task, and report the MR in TaskImplementation.
5. Register the new activity in template.go; extend tests: pure title/description tests plus Temporal test-environment tests for a complete implementation (non-draft MR, no refiner comment) and a partial one (draft MR, refiner tagged, reason and questions in the description), asserting draft state, title and description.
6. Update src/README.md: the implementation agent end to end (trigger, prompt, branch, MR, draft/refiner fallback).
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented the implementation MR step: mr.go (pure MR title/description/refiner hand-off builders), mrsubmission.go (SubmitMergeRequest activity: regular MR for complete, draft MR + refiner tag comment for partial), wired into the workflow (one MR per implementation, Result.MergeRequestURL, per-task MergeRequest, outcome logging) and template. Added mr_test.go: pure builder tests, activity tests, and Temporal test-env workflow tests for complete (non-draft, no refiner comment) and partial (draft, refiner tagged, reason+questions in description). Documented the agent end to end in src/README.md. go build/vet/test ./... pass.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
The implementation agent now submits the MR a human sees. New mr.go builds the MR title/description and the refiner hand-off (pure, tested); new mrsubmission.go adds the SubmitMergeRequest activity, which opens a regular MR from the implement/* branch to the task commit's branch for a complete implementation (title 'Implement <ID>: <title>', description links the task file) and a draft MR for a partial one (description states the reason and open questions), then tags the refiner agent (@refiner) on the draft MR with the open questions so the existing MR comment loop (TASK-11) starts a refinement round. Complete implementations post no comment. The workflow opens one MR per implementation after committing, records it on TaskImplementation.MergeRequest, fills Result.MergeRequestURL, and logs the chosen outcome per task; template.go registers the activity and the workflow input gained an optional ProviderURL for the task link.

Verification: go build ./..., go vet ./..., and go test ./... all pass. New tests: pure builder tests, SubmitMergeRequest activity tests against provider.Fake (draft state, refiner comment, validation), and Temporal test-environment workflow tests for a complete implementation (non-draft MR, correct head/base, ticket number in title, task in description, no comments) and a partial one (draft MR, reason and questions in description, partial work on the branch, refiner tagged with the questions). src/README.md documents the agent end to end: trigger, prompt, branch, MR, and the draft/refiner fallback.

Risk/follow-up: the refiner tag starts a refinement round only if the provider delivers the agent's own comment as a webhook and the refinement agent's comment loop accepts comments on implement/* MRs (its FindRefinement currently proceeds only for refine/* head branches); the hand-off reuses that path as the ticket requires, but end-to-end wiring of the loop for implement/* MRs is not part of this ticket.
<!-- SECTION:FINAL_SUMMARY:END -->
