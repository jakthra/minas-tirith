---
id: TASK-20
title: >-
  Route backlog commits to one agent by classifying task vs user story before
  the workflow starts
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-02 13:28'
updated_date: '2026-11-02 13:44'
labels: []
milestone: m-0
dependencies:
  - TASK-19
references:
  - 'TASK-19: Trigger implementation workflow when a backlog task is committed'
  - 'TASK-3: Autonomous implementation agent'
  - src/internal/worker/template.go
  - src/internal/worker/bridge.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/taskimplementation/activities.go
modified_files:
  - src/internal/worker/backlog/backlog.go
  - src/internal/worker/backlog/activities.go
  - src/internal/worker/backlog/backlog_test.go
  - src/internal/worker/template.go
  - src/internal/worker/bridge.go
  - src/internal/worker/bridge_test.go
  - src/internal/worker/routing_test.go
  - src/internal/worker/starter.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/userstoryreview/workflow.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/worker/userstoryreview/userstoryreview_test.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/activities.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
  - src/cmd/worker/main.go
  - src/cmd/listener/main.go
  - src/README.md
type: feature
ordinal: 19000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-19 added `taskImplementationWorkflow` as a second commit-triggered agent, but the two agents now react to the same commit independently and neither knows what the other decides.

Today a commit that adds a backlog task under `backlog/tasks/` starts **both** `userStoryReviewWorkflow` (whose trigger accepts any added or modified backlog file) and `taskImplementationWorkflow`. The review agent then treats the task as a user story and generates refinement tickets from it. In the other direction, a commit that adds only a user story still starts `taskImplementationWorkflow`, which completes immediately with a no-op result.

The cause is that task-vs-story classification happens too late and in only one place: the `taskImplementation.detectTasks` activity runs *inside* `taskImplementationWorkflow`, after the start decision is already made. The review agent has no equivalent check, and the trigger `Input` functions cannot read file content (they only see the commit payload), so the frontmatter — the only reliable discriminator, `type: task` vs `type: feature` — is unavailable when the start decision is made.

This ticket makes the classification a single shared decision point so exactly one agent runs per backlog commit. It is a change to the trigger mechanism in `src/internal/worker`, not to either agent workflow body.

**Related information:**
- `src/internal/worker/template.go` — `Trigger.Input` is a pure function of the event; it reports `(input, bool)` and cannot perform I/O. Any content-based decision has to move either into a shared activity both workflows call, or into a routing layer that runs before a workflow starts.
- `src/internal/worker/bridge.go` — `EventBridge.HandleEvent` iterates every trigger whose kind matches and starts each one independently; it has no notion of "at most one workflow wins".
- `src/internal/worker/userstoryreview/template.go` — `InputFromEvent` accepts any added/modified `backlog/tasks/*.md` and only declines `refine/*` branches.
- `src/internal/worker/taskimplementation/template.go` — `InputFromEvent` accepts any added backlog file and only declines `implement/*` branches; the real task/story test lives in `activities.go` (`isTaskFile`, frontmatter `type` with a `TASK-`/`US-` id fallback).
- `src/internal/worker/bridge_test.go` and `src/internal/worker/taskimplementation/taskimplementation_test.go` — existing trigger-filter test patterns to extend.

**Design constraint:** keep the existing template registration model. A new agent type or routing rule should still be a registration, not a rewrite of worker startup or event wiring (TASK-5 AC2, TASK-19 AC4).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A commit that adds a backlog task starts `taskImplementationWorkflow` and does not start `userStoryReviewWorkflow`
- [x] #2 A commit that adds or modifies a user story starts `userStoryReviewWorkflow` and does not start `taskImplementationWorkflow`
- [x] #3 A commit that adds both a task and a user story starts each agent exactly once, for its own file kind
- [x] #4 Commits that add or modify no backlog file, and commits on the agents own `refine/*` or `implement/*` branches, start neither workflow
- [x] #5 Task-vs-story classification reads file frontmatter and has a single implementation shared by both agents, not duplicated per agent
- [x] #6 The routing is registered through the existing template/trigger mechanism without modifying core worker startup or event wiring
- [x] #7 Unit tests cover the routing decision for task-only, story-only, mixed, non-backlog, and agent-branch commits, plus workflow-level tests for both agents
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add a shared backlog routing package (internal/worker/backlog) holding the one classification implementation: backlog file recognition, frontmatter parsing, task-vs-story classification, and the agent-branch rules.
2. Add a worker.ActivityTrigger to the trigger mechanism: a Trigger that classifies the event inside the worker with a registered activity (kind backlog.classifyCommit) and yields one workflow input per routed agent. The bridge still iterates triggers; the event bridge only learns to execute a classification and start every routed workflow.
3. Re-register both agents through ActivityTrigger: userStoryReviewWorkflow gets the story paths, taskImplementationWorkflow the task paths, so exactly one agent reacts per backlog file kind. The classification activity belongs to the userstoryreview template (it already holds the provider client) and is registered by both binaries unchanged.
4. Remove the per-agent duplicates: taskimplementation's DetectTasks activity and the workflow call (the shared routing now supplies TaskPaths), and userstoryreview's analyzeCommit file scan (the shared routing supplies StoryPaths; analyzeCommit becomes a pure validation step).
5. Tests: routing decision table (task-only, story-only, mixed, non-backlog, refine/implement branches), ActivityTrigger unit tests, bridge tests with a fake classifier, and workflow-level tests for both agents fed from the routing decision.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented the shared routing mechanism; one integration test remains red (see notes below).

Fixed the last red integration test: the agent-branch rules now live in the shared backlog package (backlog.IsAgentBranch over refine/* and implement/*, with both agents' prefix constants aliased to it), and both agents' trigger InputFromEvent, routing-check activities, and workflow guards decline commits on ANY agent branch. Verified: TestBacklogCommitsOnAgentBranchesStartNeitherAgent, TestBacklogCommitsRouteToOneAgent, TestIsAgentBranch, and the cross-branch route/CheckCommit/analyzeCommit cases all pass; go build/vet/gofmt clean; full FAIL	./... [setup failed]
FAIL green.

Finalization verification: go build ./... and go vet ./... clean; gofmt -l . empty; go test -count=1 ./... all packages ok (worker, worker/backlog, worker/taskimplementation, worker/userstoryreview). Routing evidence: TestBacklogCommitsRouteToOneAgent subtests task-only -> taskImplementationWorkflow only, story-only and modified-story -> userStoryReviewWorkflow only, mixed -> each agent once, non-backlog -> neither; TestBacklogCommitsOnAgentBranchesStartNeitherAgent and TestRoutedInputsCarryOnlyOwnFiles pass. AC5: single classification in internal/worker/backlog (Classify/IsFile/IsAgentBranch); taskimplementation's DetectTasks is gone (no references remain) and userstoryreview's file scan plus frontmatter parser delegate to the shared package. AC6: only worker/template.go (Trigger.Classify/Route) and worker/bridge.go (shared classification execution) changed in the core; starter.go gained only trigger validation, runner.go and the two binaries' startup flow are unchanged apart from registering the taskimplementation template. Documentation: updated src/README.md, which still described the removed detectTasks activity and add-only commits.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Backlog commits now route to exactly one agent. A new shared package internal/worker/backlog owns the single task-vs-user-story classification (frontmatter type with a TASK- id fallback), backlog file recognition, and the agent-branch rules (refine/* and implement/*). The worker trigger mechanism gained an activity trigger (Trigger.Classify + Trigger.Route); the event bridge runs the backlog.classifyCommit activity once per event and each agent's trigger starts only for its own file kind. Both agents register through the existing template model, and the per-agent duplicates (taskimplementation detectTasks, userstoryreview's file scan) were removed in favour of the shared StoryPaths/TaskPaths. Verified: build, vet and gofmt clean; full go test ./... green, including routing tests for task-only, story-only, modified-story, mixed, non-backlog and agent-branch commits, ActivityTrigger/bridge tests, and workflow-level tests for both agents. README updated to describe the shared routing. Follow-up: the taskImplementationWorkflow body remains a skeleton (TASK-3 follow-up).
<!-- SECTION:FINAL_SUMMARY:END -->
