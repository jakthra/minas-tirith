---
id: TASK-26
title: Give the implementation agent repository access for context selection
status: To Do
assignee: []
created_date: '2026-11-02 14:17'
labels: []
milestone: m-0
dependencies:
  - TASK-23
references:
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/implementation.go
  - src/internal/worker/taskimplementation/repository.go
  - src/internal/llm/llm.go
type: feature
ordinal: 25000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-23 shipped the implementation agent, but its repository context is chosen by a heuristic in the workflow body: `relevantFiles` reads the files whose paths appear verbatim in the task, then fills the rest of the budget from the top of the listing. The model never asks for anything itself. When the task does not name its files — which is the normal case for a refined ticket — the agent sees an arbitrary slice of the repository, and when it needs a file outside that slice it can only report a partial outcome. That is why partial results are expected to be common even for tasks that are perfectly implementable.

This ticket makes context selection a capability of the agent instead of a guess made before the model runs. The model should be able to inspect the repository itself: search and list it, read any file it needs, and keep looking until it has what the task requires, before it proposes changes. The tooling activities already exist (TASK-22: `ListRepository`, `ReadFiles`, `ReadTask`) and the provider client already serves them; what is missing is a loop that lets the model drive them.

The open question for the implementer is how the loop is expressed. The LLM client only offers single-shot `Chat`/`ChatJSON` (no tool calling), so a tool-calling conversation needs either an extension to the client or a protocol where the model answers with a structured request ("read these files", "search for this") that the workflow executes and feeds back. Either way the loop must stay deterministic in the workflow body, with every provider and LLM call inside an activity, and it must be bounded (a maximum number of rounds and a context budget) so a wandering model cannot run forever or blow the model context window. The heuristics from TASK-23 remain useful as the seed for the first round, not as the whole strategy.

**Related information:**
- `src/internal/worker/taskimplementation/workflow.go` — `implementTask` and `relevantFiles`; the heuristic this ticket replaces.
- `src/internal/worker/taskimplementation/repository.go` — `ReadTask`, `ListRepository`, `ReadFiles`: the existing tooling activities the loop should drive.
- `src/internal/worker/taskimplementation/implementation.go` — `GenerateImplementation`, the single-shot reasoning step; the prompt and reply shape live in `prompt.go`.
- `src/internal/llm/llm.go` — `Client.Chat`/`ChatJSON`; the test double is `src/internal/llm/fake.go`.
- `src/internal/provider/provider.go` — `GetFile`/`ListFiles`; a search operation may need a new provider method or a listing-based search.
- TASK-23 — delivered the implementation agent and its current context heuristic.
- TASK-24 — consumes the complete/partial outcome; better context should reduce spurious partial implementations.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The model can request repository context itself (list a directory, search the repository, read specific files) and receives the results before it proposes changes
- [ ] #2 The workflow executes the model's context requests through the existing repository tooling activities, keeping every provider and LLM call inside an activity and the workflow body deterministic
- [ ] #3 The context loop is bounded by a maximum number of rounds and a context budget, and a run that hits either bound still produces a structured outcome (complete or partial with a reason) rather than failing
- [ ] #4 The initial context is seeded from the task (the TASK-23 heuristic), so a task that names its files needs no extra rounds
- [ ] #5 A partial outcome caused by missing context is only reported after the model has had the opportunity to request the missing files
- [ ] #6 Activity and workflow-level tests use the provider and LLM fakes to cover a model that requests extra files across several rounds and one that hits the round bound
- [ ] #7 The implementation prompt and README document the context protocol and its bounds
<!-- AC:END -->
