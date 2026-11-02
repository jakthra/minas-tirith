---
id: TASK-25
title: >-
  Sample implementation task is routed to the refinement agent instead of the
  implementation agent
status: Done
assignee:
  - '@jakobt'
created_date: '2026-11-02 13:50'
updated_date: '2026-11-02 14:07'
labels: []
milestone: m-0
dependencies: []
references:
  - src/internal/worker/backlog/backlog.go
  - src/internal/worker/backlog/backlog_test.go
  - scripts/commit-sample-task.sh
  - src/internal/worker/taskimplementation/workflow.go
  - TASK-20
  - TASK-24
modified_files:
  - groma/systems/minas-tirith/components/backlog-activities.md
  - groma/systems/minas-tirith/components/backlog.md
  - groma/systems/minas-tirith/components/mrcomment-activities.md
  - groma/systems/minas-tirith/components/mrcomment.md
  - groma/systems/minas-tirith/components/taskimplementation-activities.md
  - groma/systems/minas-tirith/components/template.md
  - groma/systems/minas-tirith/components/workflow.md
  - src/internal/worker/backlog/backlog.go
  - src/internal/worker/backlog/testdata/sample-task.md
  - src/internal/worker/backlog/backlog_test.go
  - src/internal/worker/routing_test.go
  - src/README.md
  - scripts/commit-sample-task.sh
  - src/internal/worker/template.go
  - src/internal/worker/starter.go
  - src/internal/worker/bridge.go
  - src/internal/worker/backlog/activities.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/bridge_test.go
  - src/internal/worker/starter_test.go
  - src/internal/worker/runner_test.go
  - src/internal/worker/userstoryreview/userstoryreview_test.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
type: bug
ordinal: 24000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The `just commit-sample-task` helper (added with the sample implementation task) commits a task to `backlog/tasks/` in the template repository, but the commit starts `userStoryReviewWorkflow`, not `taskImplementationWorkflow`. The implementation agent never sees the task, so the sample cannot exercise the implementation flow it exists for.

**Observed (2026-11-02):** `just commit-sample-task` pushed `eed8465` (`backlog/tasks/task-014 - Add-a-plant-to-the-garden.md`) to `root/BeanieGenie` `main`. The Temporal dev server recorded exactly one workflow for that commit:

```
userStoryReviewWorkflow-forgejo|root/BeanieGenie|commit|eed846591b5abdabe555147ccfe3241bbfb4609d   COMPLETED
```

No `taskImplementationWorkflow` was started for the commit.

**Cause:** `backlog.Classify` (`src/internal/worker/backlog/backlog.go`) decides task vs. story from the frontmatter `type`, falling back to the `TASK-` id prefix only when `type` is absent. The sample task is written with `type: feature`, so it is classified as a story and routed to the refinement agent. The same is true for any task a human or agent writes with a backlog type other than `task`: the id says `TASK-014`, but the type wins. TASK-20 built the routing on this classification, so the routing itself works as designed — the sample task content is what misfires.

**Why this matters beyond the sample:** the sample is the documented way to try the implementation flow end to end. A new user following it sees the refinement agent generate tickets from their task instead of the implementation agent implementing it, which is a confusing first impression and hides whether the implementation flow works at all.

**Related information:**
- `scripts/commit-sample-task.sh` — writes the sample task with `type: feature`; this is the immediate trigger.
- `src/internal/worker/backlog/backlog.go` — `Classify`, the single task-vs-story decision point; the `type`-before-id precedence is deliberate and documented there.
- `src/internal/worker/backlog/backlog_test.go` — classification tests, including the `type`/id precedence.
- `src/internal/worker/taskimplementation/workflow.go` — the workflow that should have started; still a skeleton (TASK-22/23/24 pending), so even a correct route would not implement the task yet.
- `justfile` — `commit-sample-task` recipe added alongside the script.
- TASK-20 — introduced the routing this bug surfaces; TASK-15 — the analogous `commit-sample-story` script, which is correct because a story is `type: feature`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The sample task committed by `just commit-sample-task` is classified as a task and starts `taskImplementationWorkflow`, not `userStoryReviewWorkflow`
- [x] #2 A regression test covers the sample task content end to end through the shared classification, asserting it routes to the implementation agent
- [x] #3 The classification rule for a `TASK-` id with a non-task `type` is decided explicitly: either the id prefix wins for `TASK-` files, or the behaviour is documented and the sample task content is corrected to match it
- [x] #4 The sample task still drives the implementation flow once TASK-22 to TASK-24 land, with no further change to the sample script
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Rework after live failure: the sample-content fix was correct but the routing layer itself never worked at runtime. The unit tests fake the classifier in-process, so three runtime-only bugs in the activity-trigger path (TASK-19/20) went unnoticed: (1) Starter.ExecuteActivity sends the classification activity to the base task queue, which no worker polls — the runner starts workers only on per-template queues — so classification times out and no workflow ever starts; (2) the bridge passes event.Event to an activity that takes backlog.Commit, so after Temporal's JSON decode the sha and changed-file lists are empty and classification errors; (3) the activity result is decoded into an untyped any, yielding map[string]any, so each agent's Route type assertion on backlog.Classification fails and both agents decline.

Plan:
1. Starter: record an activity-name-to-task-queue map at Register (reject ambiguous duplicate activity names) and resolve the queue in ExecuteActivity instead of using the base queue; unregistered activity names fail loudly.
2. Make the shared classification activity take event.Event (the type both sides of the Temporal boundary share) and derive backlog.Commit inside it.
3. Give activity triggers a ClassifyResult factory (pointer to the activity's result type); the classifier interface decodes into it, so Route receives the typed value again. Validate its presence at Register.
4. Update both agent templates (routes assert the decoded classification), the bridge, and all test fakes; the fakes now emulate the serialization boundary (JSON round-trip) so regressions like these are caught in unit tests.
5. Regression-verify end to end on the dev stack (Temporal dev server + Forgejo + listener + worker): just commit-sample-task must start only taskImplementationWorkflow.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
The classification rule is unchanged: the frontmatter type decides, "type: task" is a task and anything else a user story. The id cannot decide because the backlog CLI names every item TASK-NNN, stories included; that is now stated in the Classify doc, the package doc, and the README backlog-routing section.

The actual defect was the sample content: scripts/commit-sample-task.sh wrote "type: feature", so the sample was classified as a story. It now writes "type: task", and its header comment explains that the type, not the TASK-NNN id, is what makes it a task. The script is otherwise unchanged.

Added src/internal/worker/backlog/testdata/sample-task.md, verified byte-identical to the file the script writes (sed-extracted heredoc with id=14), and two regression tests over it: TestClassifySampleTask (shared classification returns a task) and TestSampleTaskCommitStartsTheImplementationAgent (commit through the event bridge starts only taskImplementationWorkflow with the sample path).

Verified: go build/vet clean, gofmt -l empty, go test -count=1 ./... all packages ok, bash -n on the script clean.

Rework after live failure. The previous content fix (type: task) was correct but not sufficient: the activity-trigger routing layer from TASK-19/20 had never actually executed at runtime, so no backlog commit reached any agent. Live evidence that it was dead: of the five sample-task commits pushed with the reworked tree (task-015..task-018, both type: feature and type: task), Temporal recorded no workflow at all — only task-014 (pushed while the pre-TASK-20 binaries ran) had a userStoryReviewWorkflow.

Three runtime-only bugs, all invisible to the unit tests because the test fakes called the classification activity in-process, bypassing Temporal's queue routing and serialization:
1. Wrong task queue: Starter.ExecuteActivity sent backlog.classifyCommit to the base queue (minas-tirith), which no worker polls — the runner starts workers only on per-template queues. Classification timed out after 30s and nothing started. Fix: Starter.Register now records an activity-name-to-task-queue map (duplicate activity names across templates are rejected as ambiguous), and ExecuteActivity resolves the queue from it, failing loudly for unregistered activity names.
2. Arg shape mismatch: the bridge passed event.Event to an activity taking backlog.Commit; after the SDK's JSON decode the sha and changed-file lists (nested under commit) were empty, so classification always errored. Fix: ClassifyCommit takes event.Event — the type both sides of the boundary share — and derives the Commit inside.
3. Result type loss: handle.Get into an untyped any decoded the result to map[string]any, so both agents' Route type assertions on backlog.Classification always failed. Fix: activity triggers declare a ClassifyResult factory (a pointer to the result type, validated at Register); the classifier decodes into it and Route receives the typed pointer.

The test fakes now emulate the serialization boundary (JSON round-trip) instead of passing values straight through, so this class of bug is caught in unit tests. New coverage: activity queue resolution and unregistered-activity failure (starter), duplicate activity names, missing result factory (Register + bridge), and the sample-task regression test now crosses the typed boundary.

Live verification on the dev stack (Forgejo + Temporal dev server + rebuilt listener/worker): just commit-sample-task pushed ecee146 (backlog/tasks/task-019 - Add-a-plant-to-the-garden.md, type: task) and exactly one workflow started for it: taskImplementationWorkflow COMPLETED with result task_paths [backlog/tasks/task-019 - Add-a-plant-to-the-garden.md]. No userStoryReviewWorkflow for that commit. just commit-sample-story pushed 8ddca73 (type: feature) and started userStoryReviewWorkflow (running its LLM refinement) — the story route still works, no regression from the shared-machinery changes.

Verified: go build/vet clean, gofmt -l empty, go test -count=1 ./... all packages ok.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
The sample implementation task now reaches the implementation agent, verified live end to end. The classification rule is unchanged — the frontmatter type decides task vs. user story, and the id does not, because the backlog CLI names every item TASK-NNN — and the sample content fix (type: task in scripts/commit-sample-task.sh) stands.

The previous fix attempt failed because the routing layer itself had never executed at runtime: the unit tests faked the classification in-process, hiding three Temporal-boundary bugs in the TASK-19/20 activity-trigger path. (1) ExecuteActivity sent the classification to the base task queue, which no worker polls, so classification timed out and no workflow ever started — the live Temporal history showed zero workflows for every backlog commit pushed with the reworked tree. (2) The activity received event.Event but expected backlog.Commit, so the decoded sha and file lists were empty. (3) The activity result was decoded into an untyped any, so the routes' type assertions always failed. Fixes: the starter now resolves an activity's task queue from what templates registered (and rejects ambiguous duplicate activity names and unregistered activities loudly), the classification activity takes event.Event — the type both sides of the boundary share — and activity triggers declare a ClassifyResult factory the classifier decodes into, so Route sees the typed value. Test fakes now cross the same serialization boundary (JSON round-trip), making these bugs visible in unit tests.

Live verification on the dev stack: just commit-sample-task (ecee146) started exactly taskImplementationWorkflow, completed with task_paths [backlog/tasks/task-019 - Add-a-plant-to-the-garden.md] and no review workflow; just commit-sample-story (8ddca73) started userStoryReviewWorkflow — both routes of the shared classification work. Unit coverage includes the sample fixture end to end (TestClassifySampleTask, TestSampleTaskCommitStartsTheImplementationAgent), queue resolution, and the new registration rules. go build/vet clean, gofmt -l empty, go test -count=1 ./... all ok.

Follow-up unchanged: the implementation workflow body is TASK-22/23/24; the sample task drives it with no further change to the script.
<!-- SECTION:FINAL_SUMMARY:END -->
