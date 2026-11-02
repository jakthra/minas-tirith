---
id: TASK-22
title: Add implementation-agent repository tooling for reading and writing code
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-02 13:44'
updated_date: '2026-11-02 14:10'
labels: []
milestone: m-0
dependencies:
  - TASK-21
references:
  - src/internal/worker/taskimplementation/activities.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/provider/provider.go
  - src/internal/provider/fake.go
  - src/internal/worker/backlog/backlog.go
modified_files:
  - src/internal/worker/taskimplementation/repository.go
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
  - src/internal/worker/taskimplementation/repository_test.go
  - src/internal/provider/fake.go
type: feature
ordinal: 21000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-3 requires the agent to "implement and execute the details of the ticket", which means writing source code, not just backlog markdown. The refinement agent (TASK-2) only ever reads a story and writes ticket files, so the tooling it uses is too narrow for implementation: there is no way to read the files an agent is about to change, no way to inspect the repository layout it must fit into, and no guard against writing outside the intended branch.

The provider client already exposes the primitives (`GetFile`, `ListFiles`, `CreateBranch`, `Commit`), so this ticket adds the implementation-specific layer on top of them as Temporal activities in `src/internal/worker/taskimplementation`, following the same activity pattern as `userstoryreview`. The goal is a small, testable toolset the implementation workflow can call — not a general-purpose coding agent.

**Related information:**
- `src/internal/worker/taskimplementation/activities.go` — currently holds only `CheckCommit`; the new activities belong here (or a sibling file in the same package).
- `src/internal/worker/userstoryreview/activities.go` — the existing activity pattern to follow: dependency-injected `Activities` struct, provider calls inside activities, pure logic split out for tests.
- `src/internal/provider/provider.go` — `GetFile`, `ListFiles`, `CreateBranch`, `Commit`; the primitives these activities wrap.
- `src/internal/provider/fake.go` — the in-memory provider the activity tests use; `SeedFiles` arranges repository content.
- `src/internal/worker/taskimplementation/template.go` — the template registers activities; new ones must be added to `Activities()`.
- `src/internal/worker/backlog/backlog.go` — `FrontmatterBlock` is the existing frontmatter parser; reuse it rather than adding another.
- TASK-9 — delivered the provider primitives.
- TASK-20 — delivered the routing that supplies `TaskPaths` to this agent.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 An activity reads the backlog task file the agent must implement and returns its content and parsed frontmatter (id, title, description, acceptance criteria) from the commit the workflow started on
- [x] #2 An activity lists the repository files on the task branch, so the agent can discover the layout and existing code it must fit into, with a bounded result size
- [x] #3 An activity reads one or more source files at a given ref, returning content and reporting a missing file distinctly from a provider failure
- [x] #4 An activity creates the implementation branch from the task commit and commits the agent-produced file changes to it, refusing paths outside the repository and refusing to write to any agent branch it does not own
- [x] #5 Each activity is registered on the taskimplementation template and runs as a Temporal activity, keeping provider calls out of the workflow body
- [x] #6 Unit tests exercise every activity against the provider Fake, including missing files, provider errors, and rejected paths
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add repository tooling activities in a new sibling file src/internal/worker/taskimplementation/repository.go: ReadTask (task file content + parsed frontmatter from the task commit), ListRepository (bounded file listing on the task branch), ReadFiles (one or more files at a ref, per-file ErrNotFound), CreateImplementationBranch (implement/<slug>-<short-sha> from the task commit, reusing an existing owned branch), CommitImplementation (path validation, owned-branch guard, commit).
2. Pure helpers: task frontmatter parsing built on backlog.FrontmatterBlock, path validation, branch ownership/name derivation, listing bound.
3. Register the new activities on the taskimplementation template (template.go) with activity type names in workflow.go.
4. Unit tests against provider.Fake in repository_test.go: happy paths, missing files, provider errors, rejected paths, branch ownership, template registration, activity test environment.
5. Run gofmt, go vet and the package tests, then the full test suite.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Added the repository tooling activities in src/internal/worker/taskimplementation/repository.go: ReadTask (task content + frontmatter/description/acceptance criteria parsed via backlog.FrontmatterBlock), ListRepository (bounded at 2000 entries, reports Total/Truncated), ReadFiles (per-file missing files skipped, provider failures fail the call, paths validated), CreateImplementationBranch (always implement/<task-slug>-<short-sha>, reuses an existing branch) and CommitImplementation (rejects non-implement/* branches and absolute/../empty/directory/duplicate paths). Registered all five on the taskimplementation template with activity type names in workflow.go. Tests in repository_test.go cover happy paths, missing files, provider errors, rejected paths/branches, and the activity test environment. Fixed provider.Fake.Commit to snapshot branch state per commit SHA so reads at a commit return that commit's content (previously the SHA alias pointed at the live branch).
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added the implementation agent's repository tooling as Temporal activities in src/internal/worker/taskimplementation/repository.go, registered on the taskimplementation template (workflow.go activity names, template.go Activities()).

Activities:
- ReadTask: reads the task file at the workflow's task commit and returns content plus parsed frontmatter (id, title, status), description section, and acceptance criteria (parsed with backlog.FrontmatterBlock; section markers and criterion numbers stripped). Rejects non-backlog paths; missing file surfaces as provider.ErrNotFound.
- ListRepository: lists files on the task branch, sorted by path, bounded to 2000 entries with Total/Truncated reported.
- ReadFiles: reads one or more files at a ref; missing files are skipped (distinct from failure) while any other provider error fails the call; absolute, dot-segment, directory, and empty paths are rejected before reading.
- CreateImplementationBranch: names the branch itself (implement/<task-slug>-<short-sha>) from the task commit, so it cannot create branches outside the agent namespace; reuses an existing branch so retries succeed.
- CommitImplementation: commits agent-produced changes as one commit, refusing any branch that is not implement/* and any absolute/../empty/directory/duplicate path.

Also fixed provider.Fake.Commit: it aliased a commit SHA to the live branch, so reads at an old SHA returned later content; it now snapshots branch state per commit, matching the Client contract that SHAs are valid refs.

Verification: go build ./..., go vet ./..., gofmt clean; full go test ./... passes, plus go test -race on the taskimplementation and provider packages. 24 focused tests in repository_test.go cover happy paths, missing files, provider errors, rejected paths and branches, branch reuse, and execution through the Temporal activity test environment; TestTemplateRegistration asserts all six activities are registered.

Risk/follow-up: the workflow body itself (TASK-23) still needs to call these activities; the branch name derivation and path guards are shared helpers it should reuse.
<!-- SECTION:FINAL_SUMMARY:END -->
