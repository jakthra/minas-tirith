---
id: TASK-23
title: Implement backlog tasks with the implementation agent prompt and workflow body
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-02 13:45'
updated_date: '2026-11-02 14:14'
labels: []
milestone: m-0
dependencies:
  - TASK-22
references:
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/activities.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/llm/llm.go
  - src/internal/llm/fake.go
  - src/internal/worker/userstoryreview/workflow.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
modified_files:
  - src/internal/worker/taskimplementation/prompt.go
  - src/internal/worker/taskimplementation/implementation.go
  - src/internal/worker/taskimplementation/activities.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
  - src/internal/worker/taskimplementation/repository_test.go
  - src/internal/worker/taskimplementation/implementation_test.go
  - src/internal/worker/taskimplementation/workflow_implementation_test.go
  - src/internal/worker/routing_test.go
  - src/cmd/listener/main.go
  - src/cmd/worker/main.go
  - src/README.md
type: feature
ordinal: 22000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
This is the core of TASK-3 and the piece the current code explicitly leaves as a `TODO(TASK-3 follow-up)`: `TaskImplementationWorkflow` validates its input, filters the task paths, and returns a placeholder result. Nothing reads the task, nothing reasons about the code, and nothing produces an implementation.

This ticket turns the skeleton into the agent itself: a prompt that tells the model how to implement a refined backlog task against the real repository, and a workflow body that drives it — read the task, inspect the code, generate the changes, commit them to an `implement/*` branch. It also decides whether the result is a complete implementation or a partial one, because that decision selects between the regular MR (TASK-24) and the draft MR with refiner hand-off (TASK-24 AC2/AC3). The model must be able to report "I cannot fulfill this" as a first-class outcome rather than being forced to guess.

**Related information:**
- `src/internal/worker/taskimplementation/workflow.go` — the skeleton and its `TODO(TASK-3 follow-up)` seam; `Input`/`Result` and `Validate` already exist.
- `src/internal/worker/userstoryreview/activities.go` — `ticketGenerationSystem` and `GenerateTickets` are the existing prompt/`ChatJSON` pattern to follow.
- `src/internal/llm/llm.go` — `Client.Chat` and `Client.ChatJSON`; the implementation prompt uses one of these, and `src/internal/llm/fake.go` is the test double.
- `src/internal/worker/taskimplementation/activities.go` — the tooling activities from TASK-22 that the prompt-driven step calls.
- `src/internal/worker/userstoryreview/workflow.go` — the workflow body pattern: activity options with a retry policy, sequential `ExecuteActivity` calls, deterministic workflow code.
- `src/internal/worker/taskimplementation/taskimplementation_test.go` — `TestWorkflowImplementsTask` and the Temporal test-environment pattern to extend.
- TASK-3 AC1 — an MR is created after an implementation task is added; this ticket produces the implementation that MR carries.
- TASK-10 — delivered the LLM client used for agent reasoning.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The implementation prompt exists as a versioned, reviewable artifact in the taskimplementation package, instructing the model to implement a refined backlog task against the repository code and to respect the task acceptance criteria
- [x] #2 The prompt requires the model to report a structured outcome that distinguishes a complete implementation from one it cannot fulfill, with a reason and the open questions when it cannot
- [x] #3 An activity sends the task content, the relevant repository files, and the prompt to the LLM and returns the proposed file changes plus the structured outcome
- [x] #4 The workflow reads the task, gathers repository context, generates the implementation, and commits the changes to an `implement/<task-slug>-<short-sha>` branch
- [x] #5 The workflow result reports whether the implementation is complete or partial, the branch, the changed files, and the reason when partial, replacing the placeholder status
- [x] #6 Workflow-level tests run through the Temporal test environment with the provider and LLM fakes, covering a complete implementation and a partial one, and assert the branch and commit contents
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add prompt.go: a versioned ImplementationPrompt system prompt (implemented task against the repo, respect acceptance criteria) and a structured ImplementationOutcome (complete/partial, reason, open questions, file changes).
2. Add an LLM client to the taskimplementation Activities; add GenerateImplementation activity that sends the prompt, the task content, the repository listing and the relevant files to ChatJSON and returns the proposed file changes plus the outcome.
3. Implement the workflow body: ReadTask at the task commit, ListRepository, ReadFiles for relevant files, GenerateImplementation, create the implement/<task-slug>-<short-sha> branch, commit the changes (or the task file alone when partial with no changes), and return a Result reporting complete/partial, branch, changed files and reason.
4. Wire the LLM client into Template() and both binaries; extend tests: activity tests with llm.Fake, workflow tests in the Temporal test env for a complete and a partial implementation asserting branch and commit contents; update README.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
prompt.go holds ImplementationPrompt + ImplementationPromptVersion (task-implementation/v1); the reply format is ImplementationReply with outcome complete|partial, reason, open_questions, changes. implementation.go adds GenerateImplementation: ChatJSON with the prompt, validates the reply (unknown outcome, partial without reason, invalid/duplicate paths fail the activity), returns Implementation with the prompt version and model. Activities now take an llm.Client; Template(p, l) and both binaries updated. The workflow body reads the task at the commit, lists the repository, reads the relevant files (files named in the task, then the rest of the listing, bounded to 40 files / 64 KiB each), generates the implementation, creates implement/<slug>-<short-sha>, and commits the changes; a partial outcome with no safe changes keeps the branch (no commit) so TASK-24 can open a draft MR. Result now carries Implementations []TaskImplementation (complete, reason, open questions, branch, changed files, commit SHA, prompt version) instead of the placeholder status. Tests: implementation_test.go (prompt, reply validation, activity with llm.Fake), workflow_implementation_test.go (Temporal test env with provider + LLM fakes: complete, partial with changes, partial without changes, LLM failure), plus updated mocked workflow tests and Template signature in routing_test.go. go test ./... and go vet ./... pass.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Turned the task implementation skeleton into the implementation agent. A versioned prompt (ImplementationPrompt, task-implementation/v1) instructs the model to implement the task against the real repository, respect its acceptance criteria, and answer with a structured outcome: complete, or partial with a reason and open questions. A new GenerateImplementation activity sends the task, the repository listing, and the relevant file contents to the LLM and validates the reply. The workflow now reads each task at the commit, gathers context, generates the implementation, and commits the changes to implement/<task-slug>-<short-sha>; the result reports per task whether it is complete or partial, with the branch, changed files, commit, and reason. Verified with the full test suite (go test ./..., go vet ./...) including Temporal test-environment runs with the provider and LLM fakes for complete, partial, and empty partial implementations. Follow-up: TASK-24 opens the MR from these results; the context selection is heuristic (named files first, then the listing, bounded), so large repositories may need a smarter retrieval step later.
<!-- SECTION:FINAL_SUMMARY:END -->
