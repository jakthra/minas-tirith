---
id: TASK-2
title: Autonomous refinement MR/PR creation agent
status: Done
assignee: []
created_date: '2026-10-20 07:57'
updated_date: '2026-11-02 10:23'
labels: []
milestone: m-0
dependencies: []
modified_files:
  - src/internal/worker/userstoryreview/workflow.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/userstoryreview/mrcomment.go
  - src/internal/worker/userstoryreview/mrcomment_activities.go
  - src/internal/worker/userstoryreview/userstoryreview_test.go
  - src/internal/worker/userstoryreview/mrcomment_test.go
  - src/internal/worker/bridge.go
  - src/internal/worker/starter.go
  - src/internal/event/event.go
  - src/internal/event/dispatcher.go
  - src/internal/source/webhook/forgejo.go
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/cmd/listener/main.go
  - src/cmd/worker/main.go
  - src/README.md
type: feature
ordinal: 2000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The agent should generate MR/PRs automatically for refining technical stories into implementation tasks. The user must use the MR/PR as the entrypoint for adjusting the refinement of the provided user story. The flow must works as follows:

1. User commits new feature request into the respository
2. The agent picks up the feature requests and refines technical implementation tickets
3. The implementation tickets + any questions nececessary are provided in an MR format
4. The agent must change accordingly to user comments in the MR
5. Once user is satistfied, the MR can be merged, bringing the refined tickets into the main branch

For now `backlog.md` is utilized as the backlog store, such that all can be stored as .md files in the repository.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 New user stories trigger MR creation
- [x] #2 MR contains implementation tickets for the provided user story
- [x] #3 Agents change files using Inline comments
- [x] #4 Agents change files using generally summary comments
<!-- AC:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
The TASK-2 flow is implemented end to end (built up by TASK-4..TASK-11): a push webhook on a watched repository starts userStoryReviewWorkflow through the listener event bridge; the workflow detects added/modified story files under backlog/tasks/, generates implementation tickets and open questions with the LLM, commits the tickets as backlog.md task files on a refine/<story-slug>-<sha> branch, and opens a "Refine ..." merge request into the commit branch (AC1, AC2). Comment events on that MR start mrCommentWorkflow, which correlates the MR back to the refinement run, classifies each comment as inline or summary, applies the feedback to the ticket files on the same MR branch, and posts a response comment (AC3, AC4). Merging the MR brings the refined tickets into the main branch (flow step 5). Commits on refine/* branches are declined so the agent never re-triggers on its own work.

**Verification:** go build, go vet, gofmt clean; go test -count=1 -race ./... green in all 11 packages, including Temporal workflow test-env coverage (story commit -> refinement branch + ticket files + MR with questions; inline comment updates only the positioned ticket; summary comment updates across tickets; sequential per-MR comment handling; already-processed idempotency) and HTTP-level webhook tests (push -> commit event, issue_comment / pull_request_comment -> comment event, HMAC verify, dedupe).

**Risks/follow-ups:** Forgejo webhooks carry no inline comment positions and no per-comment events for review comments (forgejo#7935/#7966), so positions are resolved through the provider API in FindRefinement; end-to-end review-comment support and multi-comment reviews are tracked as TASK-17 and TASK-18. Merge commits landing ticket files on the main branch would read as added story files to the trigger; the refine/* guard covers the agent own commits, and re-refinement of merged tickets is left for a future task.
<!-- SECTION:FINAL_SUMMARY:END -->
