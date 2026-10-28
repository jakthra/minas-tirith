---
id: TASK-8
title: Submit MR with refined implementation tickets from userStoryReviewWorkflow
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-27 10:44'
updated_date: '2026-10-28 20:55'
labels: []
dependencies:
  - TASK-7
  - TASK-9
references:
  - 'TASK-2: Autonomous refinement MR/PR creation agent'
  - >-
    TASK-7: Implement user story detection and ticket generation in
    userStoryReviewWorkflow
modified_files:
  - src/internal/worker/userstoryreview/workflow.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/userstoryreview/userstoryreview_test.go
  - src/README.md
  - scripts/commit-sample-story.sh
type: feature
ordinal: 7000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-7 will implement user story detection and ticket generation in the `userStoryReviewWorkflow`. Once tickets are generated, they need to be presented to the user in a reviewable format.

This task implements step 3 of the TASK-2 flow: taking the generated implementation tickets from the workflow result and submitting them as a Merge Request on the Forgejo provider. The MR serves as the entrypoint for the user to review, adjust, and refine the tickets before they are merged into the main branch.

The MR should contain the generated tickets as backlog.md-format markdown files under `backlog/tasks/`, plus a description summarizing the refinement and any questions the agent has for the user. The workflow needs to track the MR it created so that future comment events (step 4 of TASK-2) can be correlated back to the refinement run.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 When the userStoryReviewWorkflow generates implementation tickets, a new branch is created on the target repository (e.g., `refine/<story-slug>-<short-sha>`)
- [x] #2 Generated tickets are written as backlog.md-format markdown files under `backlog/tasks/` on the MR branch
- [x] #3 A Merge Request is opened on the Forgejo provider targeting the branch the original commit landed on, with a description summarizing the refinement and listing any questions for the user
- [x] #4 The workflow Result includes the MR number and URL so downstream steps (comment handling) can correlate events
- [x] #5 The MR creation runs inside a Temporal activity (provider API call), preserving workflow determinism
- [x] #6 If MR creation fails, the workflow fails with a clear error (retryable via Temporal retry policy)
- [x] #7 Unit tests cover branch creation, file writing, MR opening with mocked provider API, and error handling
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. workflow.go: add activity type names (createRefinementBranch, commitTickets, openMergeRequest); new Submission type (story path/id/title, branch, MR number/URL/title, ticket files, questions) and Result.Submissions; extend the per-story loop: fetch story -> generate tickets+questions -> create refine branch (from the commit's branch) -> commit ticket files -> open MR targeting the commit's branch. Pure helpers: RefinementBranchName (refine/<story-slug>-<short-sha>), parseStoryMeta (frontmatter id/title), buildMRTitle/buildMRDescription (summary + ticket list + questions).
2. activities.go: extend GenerateTickets to also return agent questions (LLM JSON schema gains questions; AC3); new activities: CreateRefinementBranch (provider.CreateBranch), CommitTickets (ListFiles backlog/tasks for next free task ID, render backlog.md-format ticket files with frontmatter, provider.Commit; IDHint threads IDs across multi-story commits), OpenMergeRequest (provider.CreateMergeRequest). Nil-provider guards like FetchStory.
3. template.go: register the three new activities.
4. tests: update existing (GenerateTickets output shape, template activity count) and add: branch naming, story meta parsing, MR description building, ticket file rendering + ID assignment, per-activity success/error, workflow submission happy path (assert branch/files/MR on the fake provider), MR-open failure (AC6), zero-tickets skip, multi-story ID threading.
5. go test -race ./..., go vet, gofmt; verify live with just commit-sample-story.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented MR submission in userStoryReviewWorkflow:

**Changes:**
- workflow.go: three new activity type names (createRefinementBranch, commitTickets, openMergeRequest); RefinementBranchPrefix constant; Submission type (story, branch, MR number/title/URL, ticket files, questions); Result.Submissions; workflow loop extended per story: fetch -> generate tickets+questions -> create refine branch (from the commit's branch) -> commit ticket files -> open MR. Pure helpers: RefinementBranchName (refine/<story-slug>-<short-sha>), parseStoryMeta (backlog.md frontmatter id/title), buildMRTitle/buildMRDescription (summary + ticket list + questions).
- activities.go: GenerateTickets now returns GenerateTicketsOutput{Tickets, Questions} (LLM JSON schema gains questions; AC3). New activities: CreateRefinementBranch (provider.CreateBranch; 409 conflict tolerated: reuse existing branch from a partial run), CommitTickets (ListFiles backlog/tasks for the next free task id, renders backlog.md-format ticket files with frontmatter, provider.Commit; IDHint threads ids across multi-story commits), OpenMergeRequest (provider.CreateMergeRequest). Ticket rendering as pure functions: ticketFilePath, titleSlug, renderTicketFile, taskFileID, maxTaskID.
- template.go: registers the three new activities; InputFromEvent declines commits on refine/* branches (self-trigger guard).
- Self-trigger loop fix (found during live verification): the agent's ticket commit on the refine branch fired a push webhook, which started a new workflow that refined the agent's own tickets -> cascade (10 MRs in ~1 min). Fixed at two levels: InputFromEvent declines refine/* branch commits (no workflow starts), and analyzeCommit refuses refine/* branches too (guards manual workflow starts). Terminated the cascade workflows, closed the 8 cascade MRs, deleted the 11 cascade branches; the dev repo now has only the legitimate refinement MRs (#2, #11).
- scripts/commit-sample-story.sh: fixed octal parsing bug (( 10#$n )) that broke the next-id computation once task-008 existed (bash reads 008 as invalid octal).
- src/README.md: userStoryReviewWorkflow section updated from the TASK-5 skeleton description to the actual refinement+MR flow.

**Verification:**
- go build, go vet, gofmt clean; go test -race ./... all 11 packages green; 39 test functions in userstoryreview (up from 20).
- Live end-to-end (dev Forgejo + Temporal dev server + listener + worker rebuilt with new code): just commit-sample-story pushed task-010 story -> exactly one workflow ran and completed -> MR #11 'Refine TASK-010: Track watering history per plant' opened (refine/task-010-track-watering-history-per-plant-848def04d985 -> main, 4 ticket files, 3 questions in the description). No cascade: the ticket commit on the refine branch fired a webhook and was correctly declined by the new trigger guard.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented MR submission in userStoryReviewWorkflow (TASK-8): commit-sample-story now opens a refinement merge request.

**What changed:**
- src/internal/worker/userstoryreview/workflow.go: RefinementBranchPrefix constant; Submission type; Result.Submissions; per-story loop extended with CreateRefinementBranch -> CommitTickets -> OpenMergeRequest activities; pure helpers (RefinementBranchName, parseStoryMeta, buildMRTitle, buildMRDescription).
- src/internal/worker/userstoryreview/activities.go: GenerateTickets returns tickets plus agent questions; three new activities: CreateRefinementBranch (409-tolerant), CommitTickets (next free backlog task id, backlog.md-format ticket files, IDHint threading), OpenMergeRequest.
- src/internal/worker/userstoryreview/template.go: registers the new activities; InputFromEvent declines commits on refine/* branches.
- src/internal/worker/userstoryreview/userstoryreview_test.go: 39 test functions (was 20) covering branch creation, ticket file rendering, ID assignment, MR opening, MR/branch failure paths, refine-branch self-trigger guard.
- src/README.md: workflow section updated to the real flow. scripts/commit-sample-story.sh: octal next-id bug fixed.

**Why:** TASK-7 generated tickets but presented them nowhere. This task takes step 3 of TASK-2: the tickets become a reviewable Forgejo MR (branch refine/<story-slug>-<short-sha> with backlog.md ticket files, description summarizing the refinement and the agent's questions, base = the commit's branch). Result.Submissions records MR number/URL for TASK-11 comment correlation.

**Design decisions:**
- Three separate activities (branch, commit, MR) so Temporal retries only the failed step; CreateRefinementBranch tolerates 409 (reuses a branch from a partial run).
- Ticket ids continue the repository's next free backlog task id; IDHint threads ids when one commit refines several stories.
- Story metadata (id/title) parsed from backlog.md frontmatter for the MR title/description; falls back to the file name.
- Self-trigger guard at two levels (found during live verification): the agent's ticket commit fires a push webhook that previously cascaded (the agent refined its own tickets). InputFromEvent declines refine/* branch commits, and analyzeCommit refuses them too.

**Verification:**
- go build, go vet, gofmt clean; go test -race ./... green (all 11 packages).
- Live end-to-end on the dev stack: commit-sample-story -> one workflow -> MR #11 'Refine TASK-010: Track watering history per plant' (refine/task-010-track-watering-history-per-plant-848def04d985 -> main, 4 ticket files, 3 questions). The cascade incident during the first live run was cleaned up (8 MRs closed, 11 branches deleted) and the re-run confirms no re-trigger.

**Risks/follow-ups:**
- The ticket-generation prompt quality is still a starting point (tickets are LLM-generated; review in the MR is the intended loop).
- Merge commits landing ticket files on main would read as added story files (Forgejo push payloads list them per commit); TASK-11's merge flow should decide whether merged tickets get re-refined (e.g. decline by author or a merge marker).
- The running listener/worker were restarted with the new code during verification (background instances); the user should restart their own just run-listener / just run-worker terminals if they want them back in the foreground.
<!-- SECTION:FINAL_SUMMARY:END -->
