---
id: TASK-7
title: >-
  Implement user story detection and ticket generation in
  userStoryReviewWorkflow
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-27 10:43'
updated_date: '2026-10-28 15:06'
labels: []
dependencies:
  - TASK-5
  - TASK-9
  - TASK-10
references:
  - 'TASK-2: Autonomous refinement MR/PR creation agent'
  - 'TASK-5: Create Temporal worker for spawning autonomous agents on git events'
modified_files:
  - src/internal/worker/userstoryreview/workflow.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/worker/userstoryreview/template.go
  - src/internal/worker/userstoryreview/userstoryreview_test.go
  - src/cmd/worker/main.go
  - src/cmd/listener/main.go
type: feature
ordinal: 6000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-5 delivered the Temporal execution layer with a `userStoryReviewWorkflow` skeleton. The workflow is triggered by commit events, validates the payload, and runs an `AnalyzeCommit` activity — but both the activity and the workflow refinement step are stubs that always report "no refinement needed."

This task fills in that scaffolding to implement the first part of the TASK-2 autonomous refinement agent: detecting user stories in commits and generating implementation tickets from them. The MR/PR creation, comment handling, and merge flow remain for a follow-up task.

The commit event already carries `Added`, `Removed`, and `Modified` file lists from the Forgejo push payload. User stories and backlog tasks are stored as markdown files in `backlog/tasks/` using the backlog.md format. No git provider client library exists yet; a Forgejo/Gitea API client will need to be added to fetch file contents.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A commit that adds or modifies a file under `backlog/tasks/` (a user story) is detected by the AnalyzeCommit activity and reported as NeedsRefinement: true
- [x] #2 When a user story is detected, the workflow fetches the story content via a provider API activity and generates implementation tickets in backlog.md markdown format
- [x] #3 The workflow Result struct includes the list of generated tickets (title and description for each)
- [x] #4 Commits that do not touch story files complete with NeedsRefinement: false and no tickets generated
- [x] #5 All provider API calls (fetching file contents) run inside Temporal activities, not in the workflow body (determinism preserved)
- [x] #6 Unit tests cover story detection (file patterns), ticket generation, workflow happy path with mocked activities, and error handling for provider API failures
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Update Input struct to carry Added/Removed/Modified file lists from commit events
2. Update Analysis struct to include detected story paths; add Ticket type; update Result struct with generated tickets
3. Create Activities struct holding provider.Client + llm.Client with methods: AnalyzeCommit (story detection), FetchStory (provider API), GenerateTickets (LLM)
4. Update UserStoryReviewWorkflow: when NeedsRefinement, fetch story content + generate tickets via activities
5. Update template.go: Template(deps) accepts provider+llm clients, registers new activities
6. Update cmd/worker/main.go: construct provider+llm clients from config, pass to Template
7. Write comprehensive unit tests: story detection, ticket generation, workflow happy path with mocked activities, provider API failure handling
8. Run go test -race ./... and go vet, fix issues
9. Update backlog task with modified files and notes
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented user story detection and ticket generation in userStoryReviewWorkflow:

**Changes:**
- workflow.go: Input now carries Added/Removed/Modified file lists; Analysis includes StoryPaths; new Ticket type; Result includes Tickets. Workflow calls activities by name (string) for proper DI. When NeedsRefinement, iterates story paths: FetchStory -> GenerateTickets per story, aggregates tickets.
- activities.go: New Activities struct holding provider.Client + llm.Client. Methods: AnalyzeCommit (detects backlog/tasks/*.md in Added/Modified, dedupes, returns StoryPaths), FetchStory (provider.GetFile), GenerateTickets (llm.ChatJSON with system prompt asking for backlog.md-format tickets). Core logic extracted to pure functions (analyzeCommit) for unit testing without Temporal context. Nil-client guards for listener registration.
- template.go: Template(provider, llm) now accepts clients, returns template wired to Activities. InputFromEvent passes through Added/Removed/Modified.
- cmd/worker/main.go: Constructs ForgejoClient + ChatCompletionsClient from config, passes to Template.
- cmd/listener/main.go: Passes nil clients (listener only starts workflows, never executes activities).

**Tests (all pass with -race, go vet clean, gofmt clean):**
- TestIsStoryFile: 10 cases covering backlog/tasks/*.md patterns, nested paths, non-md, wrong dirs
- TestAnalyzeCommitDetectsStoryFiles: 9 cases (added, modified, multiple, dedup, non-story, removed-only, mixed, nested, empty)
- TestFetchStory / TestFetchStoryProviderError / TestFetchStoryNotFound: provider API success + error paths
- TestGenerateTickets / TestGenerateTicketsLLMError / TestGenerateTicketsInvalidJSON: LLM success + error paths, verifies JSON mode + prompt content
- TestWorkflowNoStoryFiles (AC4): non-story commit -> NeedsRefinement=false, no tickets
- TestWorkflowWithStoryFile (AC1-3): story commit -> fetch + generate -> 2 tickets in Result
- TestWorkflowMultipleStoryFiles: 2 stories -> 2 tickets aggregated
- TestWorkflowProviderAPIFailure (AC6): provider error -> workflow fails with fetch story error
- TestWorkflowLLMFailure: LLM error -> workflow fails with generate tickets error
- TestWorkflowRejectsInvalidInput, TestInputValidate, TestInputFromEvent (with file lists), TestInputFromEventDeclines, TestTemplate (3 activities), TestAnalyzeCommitActivityEnv
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented user story detection and ticket generation in userStoryReviewWorkflow (TASK-7).

**What changed:**
- src/internal/worker/userstoryreview/workflow.go: Input carries Added/Removed/Modified file lists from commit events; Analysis includes StoryPaths; new Ticket type (title+description); Result includes Tickets. Workflow calls activities by name (string) enabling dependency injection. When a story is detected, iterates story paths: FetchStory activity -> GenerateTickets activity per story, aggregating all tickets into Result.
- src/internal/worker/userstoryreview/activities.go: New Activities struct holding provider.Client + llm.Client (injected via template). Three activity methods: AnalyzeCommit (detects backlog/tasks/*.md files in Added/Modified lists, dedupes paths, returns StoryPaths), FetchStory (provider.GetFile), GenerateTickets (llm.ChatJSON with system prompt requesting backlog.md-format tickets as JSON). Core detection logic extracted to pure function analyzeCommit() for unit testing without Temporal context. Nil-client guards allow listener registration without real clients.
- src/internal/worker/userstoryreview/template.go: Template(provider, llm) accepts clients, wires Activities struct methods as Temporal activities. InputFromEvent passes through file lists from commit events.
- src/cmd/worker/main.go: Constructs ForgejoClient + ChatCompletionsClient from WorkerConfig, passes to userstoryreview.Template.
- src/cmd/listener/main.go: Passes nil clients (listener only starts workflows via Starter, never executes activities).

**Why:** TASK-5 delivered the workflow skeleton with stub activities. This task fills in the first part of the TASK-2 autonomous refinement agent: detecting user stories in commits and generating implementation tickets. MR/PR creation and comment handling remain for TASK-8/11.

**Design decisions:**
- Activities struct pattern for dependency injection (provider + LLM clients) — the standard Temporal Go approach, keeps API calls in activities (determinism preserved, AC5)
- Workflow calls activities by name string (not function reference) so the registered activity implementations can be bound to injected dependencies
- Pure function extraction (analyzeCommit) enables fast unit tests without Temporal test environment overhead
- isStoryFile: markdown files directly under backlog/tasks/ only (no subdirectories, no non-md files)

**Verification:**
- go build ./... clean
- go test -race ./... all green (11 packages)
- go vet ./... clean
- gofmt clean
- 17 test functions covering all 6 acceptance criteria:
  - AC1: TestAnalyzeCommitDetectsStoryFiles (9 cases), TestWorkflowWithStoryFile
  - AC2: TestWorkflowWithStoryFile (fetch + generate activities called)
  - AC3: TestWorkflowWithStoryFile (Result.Tickets with title+description)
  - AC4: TestWorkflowNoStoryFiles (NeedsRefinement=false, no tickets)
  - AC5: Code review — all provider/LLM calls in activity methods, workflow uses activity names
  - AC6: TestIsStoryFile, TestGenerateTickets, TestWorkflow* (happy path), TestWorkflowProviderAPIFailure, TestFetchStoryProviderError, TestWorkflowLLMFailure

**Risks/follow-ups:**
- MR/PR creation, comment handling, and merge flow are TASK-8/11 (not in scope)
- The ticket generation prompt is a starting point; may need tuning based on real LLM output quality
- FORGEJO_TOKEN and LLM_API_KEY are now required at worker startup (fail-fast on missing config)
<!-- SECTION:FINAL_SUMMARY:END -->
