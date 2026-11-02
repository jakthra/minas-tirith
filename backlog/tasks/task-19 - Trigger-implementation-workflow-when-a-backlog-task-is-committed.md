---
id: TASK-19
title: Trigger implementation workflow when a backlog task is committed
status: Done
assignee:
  - '@jakobt'
created_date: '2026-11-02 10:26'
updated_date: '2026-11-02 10:28'
labels: []
milestone: m-0
dependencies:
  - TASK-3
references:
  - 'TASK-3: Autonomous implementation agent'
  - 'TASK-5: Create Temporal worker for spawning autonomous agents on git events'
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/worker/template.go
modified_files:
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/activities.go
  - src/internal/worker/taskimplementation/template.go
  - src/cmd/worker/main.go
  - src/cmd/listener/main.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
  - src/README.md
type: feature
ordinal: 18000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-3 (Autonomous implementation agent) requires that a commit adding a backlog *task* — not a user story — starts an implementation agent that delivers the ticket as an MR. TASK-5 delivered the Temporal execution layer and its template/trigger mechanism, and TASK-7/8 built the user story review agent on top of it: commit events start `userStoryReviewWorkflow`, which reacts to any added/modified file under `backlog/tasks/`.

Nothing yet distinguishes a task from a user story, and no workflow exists for implementing a task. This ticket adds the trigger path only: detect that a commit adds a backlog task and start a new `taskImplementationWorkflow` through the existing template/trigger mechanism. The implementation logic itself is intentionally out of scope and left empty for now — a later ticket fills it in, mirroring how TASK-5 shipped a skeleton workflow that TASK-7/8 completed.

**Why a separate workflow rather than extending the review agent:** implementation is a different agent with different inputs, activities, and MR output. Reusing the template mechanism keeps the review agent untouched and lets the implementation agent evolve independently.

**Related information:**
- `src/internal/worker/template.go` — `Template`, `Workflow`, `Activity`, and `Trigger` interfaces; a new agent type is a registration, not a core change.
- `src/internal/worker/userstoryreview/template.go` — the reference implementation of a template: `Workflows()`, `Activities()`, `Triggers()`, and an `InputFromEvent` filter (note it already declines commits on `RefinementBranchPrefix` branches to avoid self-triggering).
- `src/internal/worker/userstoryreview/activities.go` — `analyzeCommit`/`isStoryFile` show how files under `backlog/tasks/` are recognized today, and `renderTicketFile` shows how generated tickets are written (frontmatter with `id: TASK-NNN`, `type:` field). Distinguishing a task from a user story will need to read that frontmatter (`type: task` vs `type: feature`) or an equivalent rule.
- `src/internal/worker/bridge.go` and `src/cmd/worker/main.go` — how triggers are wired to events and how templates are registered.
- `src/internal/provider` — client for fetching file contents and opening MRs, available to activities.

**Scope boundary:** this ticket covers detection and workflow start only. The workflow body should be an empty skeleton (validate input, return a placeholder result) with seams marked for the follow-up implementation ticket.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A commit that adds a backlog task file (not a user story) under `backlog/tasks/` starts `taskImplementationWorkflow` via a registered trigger
- [x] #2 Commits that add or modify only user story files, or no backlog files, do not start `taskImplementationWorkflow`
- [x] #3 The workflow input carries enough context to act on the task: repository, branch, commit SHA, and the task file path(s)
- [x] #4 The workflow is registered through the existing worker template mechanism without modifying core worker startup or event wiring
- [x] #5 The workflow body is an empty skeleton that validates its input and returns a placeholder result, with the implementation logic left for a follow-up ticket
- [x] #6 Commits on the agent’s own branches do not re-trigger the workflow
- [x] #7 Unit tests cover task-vs-story detection, the trigger filter, and a workflow execution via the Temporal test environment
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. New template package src/internal/worker/taskimplementation: Template() (Name taskimplementation, Workflows: taskImplementationWorkflow, Activities: taskImplementation.detectTasks), Input (provider/project/repository/branch/sha/message/url/author/added/removed/modified + TaskPaths), Result placeholder.
2. Detection: DetectTasks activity reads frontmatter of each file the commit ADDED under backlog/tasks/ via provider.GetFile at the commit SHA; selects files whose type frontmatter is not a user story (type: task; missing/invalid frontmatter falls back to the backlog.md id rule: TASK-NNN = task, US-NNN = story). Modified/removed files and story files are ignored.
3. Trigger: Trigger() returns a commit trigger whose InputFromEvent declines non-commit events, missing SHA, agent-owned branches (refine/ and implement/), and commits that add no backlog/tasks markdown file. The frontmatter check runs in the workflow's activity (provider access stays out of the trigger), so the workflow returns a no-op placeholder result for story-only commits.
4. Workflow body: validate input, run detection activity, return placeholder Result summarizing detected task paths; seams marked for the follow-up implementation ticket.
5. Register the template in cmd/worker (Runner) and cmd/listener (Starter) with one Register call each; no core wiring changes.
6. Tests: task-vs-story detection (frontmatter + fallback), trigger filter (task commit, story-only, non-backlog, agent branches, non-commit), workflow execution via Temporal TestWorkflowEnvironment with provider fake, template registration/trigger wiring.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented the task implementation trigger path as a second worker template (src/internal/worker/taskimplementation):
- template.go: Template(provider.Client) — Name taskimplementation (task queue <base>-taskimplementation), workflow taskImplementationWorkflow, activity taskImplementation.detectTasks, commit trigger. InputFromEvent declines non-commit events, nil commit, empty SHA, implement/* branches (agent's own work), and commits that add no markdown file directly under backlog/tasks/. It carries provider/project/repository/branch/SHA/message/url/author and the added backlog paths as TaskPaths.
- activities.go: DetectTasks reads each added backlog file at the commit SHA via provider.GetFile and keeps tasks, not user stories: frontmatter type == task (case-insensitive) wins; without a usable type it falls back to the backlog.md id convention (TASK-NNN = task, US-NNN = story). Non-backlog/nested paths and duplicates are skipped; agent implement/* branches are declined again as a guard for manual starts.
- workflow.go: TaskImplementationWorkflow validates input (repository, SHA, at least one task path), runs DetectTasks with a retry policy, and returns a placeholder Result{Implemented, Status, TaskPaths, MergeRequestURL(empty)}; TODO seam marks where the follow-up ticket implements tasks and opens the MR.
- Registration: one Register call each in cmd/worker (Runner, provider client) and cmd/listener (Starter, nil client) — no core worker/bridge changes.
- Tests (taskimplementation_test.go): isBacklogFile/isTaskFile (type task/feature/bug, case, id fallback, no/unparseable frontmatter), detectTasks table (task, story, mixed, non-backlog, duplicate, agent branch) with the provider fake, provider error and nil-provider error, InputFromEvent table (task add, task among other files, modified-only, non-backlog, nested, no files, implement/* branch, empty SHA, nil commit, comment event) incl. input context assertions, template registration through worker.Starter (trigger kind/workflow, duplicate rejection), and workflow runs via the Temporal TestWorkflowEnvironment (task detected/placeholder result, story-only no-op, invalid input fails). go test -race ./... green, gofmt/vet clean.
- README: documented the new template, the detection rule, and a manual start example.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added the task implementation trigger path as a second worker template, src/internal/worker/taskimplementation (TASK-3 implementation agent, TASK-19 scope: detection and workflow start only).

What changed:
- template.go: Template(provider.Client) registers taskImplementationWorkflow and the taskImplementation.detectTasks activity on task queue <base>-taskimplementation, plus a commit trigger. InputFromEvent declines non-commit events, nil commit, empty SHA, commits on implement/* (the agent's own branches), and commits that add no markdown file directly under backlog/tasks/. The input carries provider, project, repository, branch, SHA, message, url, author, and the added backlog paths as task_paths.
- activities.go: DetectTasks reads each added backlog file at the commit SHA through the provider client and keeps tasks, not user stories: frontmatter type == task decides (case-insensitive); without a usable type the backlog.md id convention applies (TASK-NNN = task, US-NNN = story). Non-backlog/nested paths and duplicates are skipped; implement/* branches are declined again as a guard for manual starts.
- workflow.go: TaskImplementationWorkflow validates its input, runs DetectTasks with a retry policy, and returns a placeholder Result (Implemented, Status, TaskPaths; MergeRequestURL empty). A TODO seam marks where the follow-up ticket implements tasks and opens the MR.
- Registration: one Register call each in cmd/worker (Runner, provider client) and cmd/listener (Starter, nil client); no changes to the worker core, bridge, or event wiring.
- README: documented the template, the detection rule, and a manual start example.

Verification: go test -race ./... green, gofmt and go vet clean. New tests cover task-vs-story detection (frontmatter type task/feature/bug, case-insensitivity, id fallback, missing/unparseable frontmatter), the detectTasks table with the provider fake (task, story, mixed, non-backlog, duplicate, agent branch), provider and nil-provider errors, the trigger filter table (task add, task among other files, modified-only, non-backlog, nested, no files, implement/* branch, empty SHA, nil commit, comment event) with input context assertions, template registration through worker.Starter (trigger kind/workflow, duplicate rejection), and workflow execution via the Temporal TestWorkflowEnvironment (task detected with placeholder result, story-only no-op, invalid input fails).

Risks/follow-ups: the trigger cannot read file content, so a story-only commit still starts the workflow; it completes immediately with a no-op result (AC2 is satisfied at the workflow level, not by suppressing the start). The provider fake shares branch state across SHAs, so reading the file at the commit SHA is exercised but not distinguished from other refs in tests. The implementation logic, MR output, draft MR/refiner hand-off (TASK-3 AC2/AC3), and the listener's own implement/* branch naming are follow-up work.
<!-- SECTION:FINAL_SUMMARY:END -->
