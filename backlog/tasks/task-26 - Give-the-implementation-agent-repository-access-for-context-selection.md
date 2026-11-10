---
id: TASK-26
title: Give the implementation agent repository access for context selection
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-02 14:17'
updated_date: '2026-11-10 20:48'
labels: []
milestone: m-0
dependencies:
  - TASK-23
references:
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/implementation.go
  - src/internal/worker/taskimplementation/repository.go
  - src/internal/llm/client.go
modified_files:
  - src/internal/worker/taskimplementation/context.go
  - src/internal/worker/taskimplementation/context_test.go
  - src/internal/worker/taskimplementation/workflow.go
  - src/internal/worker/taskimplementation/implementation.go
  - src/internal/worker/taskimplementation/prompt.go
  - src/internal/worker/taskimplementation/repository.go
  - src/internal/worker/taskimplementation/template.go
  - src/internal/worker/taskimplementation/workflow_context_test.go
  - src/internal/worker/taskimplementation/implementation_test.go
  - src/internal/worker/taskimplementation/taskimplementation_test.go
  - src/README.md
type: feature
ordinal: 25000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-23 shipped the implementation agent, but its repository context is chosen by a heuristic in the workflow body: `relevantFiles` reads the files whose paths appear verbatim in the task, then fills the rest of the budget from the top of the listing. The model never asks for anything itself. When the task does not name its files — which is the normal case for a refined ticket — the agent sees an arbitrary slice of the repository, and when it needs a file outside that slice it can only report a partial outcome. That is why partial results are expected to be common even for tasks that are perfectly implementable.

This ticket makes context selection a capability of the agent instead of a guess made before the model runs. The model should be able to inspect the repository itself: search and list it, read any file it needs, and keep looking until it has what the task requires, before it proposes changes. The tooling activities already exist (TASK-22: `ListRepository`, `ReadFiles`, `ReadTask`) and the provider client already serves them; what is missing is a loop that lets the model drive them.

The open question for the implementer is how the loop is expressed. The LLM client only offers single-shot `Chat`/`ChatJSON` (no tool calling), so a tool-calling conversation needs either an extension to the client or a protocol where the model answers with a structured request ("read these files", "search for this") that the workflow executes and feeds back. Either way the loop must stay deterministic in the workflow body, with every provider and LLM call inside an activity, and it must be bounded (a maximum number of rounds and a context budget) so a wandering model cannot run forever or blow the model context window. The heuristics from TASK-23 remain useful as the seed for the first round, not as the whole strategy.

**Protocol choice — structured requests via `ChatJSON` (preferred).** The client is single-shot by design, not by limitation: its wire types (`src/internal/llm/client.go`) carry only `role`/`content` and request only `response_format`, and `validateHistory` accepts only `user` and `assistant` turns. Native tool calling is a small client change (add `tools`/`tool_calls`/`tool_call_id`, a `tool` role, and an extended `Fake`), but it buys provider-native function calling without simplifying the workflow: the loop still needs one activity per tool execution and a replay-safe conversation history either way. It also degrades on the self-hosted endpoints the README supports (Ollama, vLLM, LM Studio) where tool-calling support is uneven, while JSON mode — already hardened for fenced and prose-wrapped replies — works everywhere. So this ticket uses a structured request reply parsed by `ChatJSON`; native tool calling is a deliberate follow-up, not part of this scope.

**Related information:**
- `src/internal/worker/taskimplementation/workflow.go` — `implementTask` and `relevantFiles`; the heuristic this ticket replaces.
- `src/internal/worker/taskimplementation/repository.go` — `ReadTask`, `ListRepository`, `ReadFiles`: the existing tooling activities the loop should drive.
- `src/internal/worker/taskimplementation/implementation.go` — `GenerateImplementation`, the single-shot reasoning step; the prompt and reply shape live in `prompt.go`.
- `src/internal/llm/client.go` — `Client.Chat`/`ChatJSON` and the wire types; the test double is `src/internal/llm/fake.go`.
- `src/internal/provider/provider.go` — `GetFile`/`ListFiles`; a search operation may need a new provider method or a listing-based search.
- TASK-23 — delivered the implementation agent and its current context heuristic.
- TASK-24 — consumes the complete/partial outcome; better context should reduce spurious partial implementations.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The model can request repository context itself (list a directory, search the repository, read specific files) and receives the results before it proposes changes
- [x] #2 The workflow executes the model's context requests through the existing repository tooling activities (or a new search activity beside them), keeping every provider and LLM call inside an activity and the workflow body deterministic
- [x] #3 The context loop is bounded by a maximum number of rounds and an aggregate context budget (a new limit: today only per-file size and file count are bounded). Hitting the round bound produces a partial outcome whose reason names the bound. Hitting the budget bound produces a partial outcome that names the requests refused for budget, so a context-starved result is never misreported as missing information. Neither bound fails the run.
- [x] #4 The initial context is seeded from the task (the TASK-23 heuristic) and rendered into the first prompt, so a task that names its files needs no extra rounds
- [x] #5 A partial outcome caused by missing context is only reported after the model has had the opportunity to request the missing files: the final round is always a propose-only round that cannot issue requests, and the prompt states that a missing-context partial is valid only once requests have been tried
- [x] #6 Activity and workflow-level tests use the provider and LLM fakes to cover a model that requests extra files across several rounds, one that hits the round bound, and one that hits the budget bound
- [x] #7 The implementation prompt (bumped to ImplementationPromptVersion v2) and README document the context protocol and its bounds
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Protocol: structured requests via ChatJSON. Add a request variant to ImplementationReply (outcome "request" with list/search/read requests) plus a ContextRequest/ContextReply shape in a new context.go; a propose-only round rejects any request.
2. Activities: add SearchRepository activity (listing-based substring search over ListRepository's listing, bounded, reports truncation) beside ListRepository/ReadFiles; add RequestContext activity (one LLM call per round, returns either a request set or a proposal) and keep GenerateImplementation as the final propose-only round.
3. Bounds: new maxContextRounds and maxContextBudgetBytes constants; the workflow loop tracks the aggregate budget, refuses requests that would exceed it, records refused requests, and the final round is propose-only. Round bound and budget bound produce partial outcomes naming the bound/refused requests; neither fails the run.
4. Workflow: seed context from the TASK-23 heuristic (relevantFiles) into the first prompt, run the bounded request loop through activities, then generate, then create branch/commit (branch creation stays after the loop).
5. Prompt: bump ImplementationPromptVersion to v2 and document the context protocol and bounds; update README.
6. Tests: activity tests for reply parsing, request validation, search; workflow tests for multi-round requests, round bound, budget bound, propose-only final round, plus updates to existing exact-JSON tests.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented the context protocol: RequestContext activity (one ChatJSON round returning requests or a proposal), SearchRepository activity (listing-based path search), Context/ContextRefusal workflow state with the aggregate maxContextBudgetBytes bound, maxContextRounds bound, seed context from the TASK-23 heuristic rendered in the first prompt, propose-only final round, and context-bound reasons folded into partial outcomes. ImplementationPrompt bumped to v2. Existing tests updated and passing.

Verification: go vet ./... clean, gofmt clean, go test ./... passes, go test -race on taskimplementation and llm passes. New tests: TestContextRoundFromReply, TestRequestContextRequests/Proposes/Errors, TestSearchRepository(+Errors), TestContextBytes, TestContextAddFileReplacesAndSorts, TestImplementationPromptDocumentsContextProtocol, TestSeedContextPrefersNamedFiles/FitsBudget, TestWorkflowModelRequestsContextAcrossRounds, TestWorkflowSeedNeedsNoRequestRound, TestWorkflowContextRoundBound, TestWorkflowContextBudgetBound, TestWorkflowContextBudgetRefusalKeepsCompleteOutcome, TestContextBoundReason. Decisions: search is a new listing-based SearchRepository activity (provider client has no search op) and reports truncation for both bounded results and a bounded listing; budget set to 4 MiB so the 40-file/64 KiB seed always fits; a refused request never fails the run and only folds into a partial reason; branch creation stays after the loop.
<!-- SECTION:NOTES:END -->

## Comments

<!-- COMMENTS:BEGIN -->
author: @opencode
created: 2026-11-10 20:42

---

Review notes for the implementer (from a code read against TASK-23 as shipped):

**Why structured requests, not tool calling.** The client is single-shot because its wire types were scoped to the two existing agents, not because the API forbids tools: `wireMessage` has only `role`/`content`, `chatCompletionsRequest` has no `tools`, the response decoder ignores `tool_calls`, and `validateHistory` rejects a `tool` role (pinned by `client_test.go`). Extending it is ~200 lines, but it does not simplify the workflow loop — every tool execution is still an activity and the history still has to replay deterministically — and it is uneven on the self-hosted endpoints the README lists. JSON mode is already hardened for fenced/prose replies and works everywhere. Native tool calling belongs in its own ticket if wanted.

**The aggregate budget is new code.** AC3 names a context budget that does not exist today: `maxContextFileBytes` (64 KiB) and `maxContextFiles` (40) are per-file and per-count limits with no cap on the assembled prompt. This is the largest new piece in the ticket.

**Reply shape ripples into existing tests.** Adding a request variant to `ImplementationReply` changes `TestImplementationFromReply`'s table and the exact-JSON assertions in `workflow_implementation_test.go`. Expect to update them.

**Search has no home yet.** `provider.Client` has `GetFile`/`ListFiles` and `ListRepository` already takes a `Path` prefix, so a listing-based search is feasible, but `ReadFiles` fails the whole call on a bad path. Decide up front whether search is a new `SearchRepository` activity or a workflow-side translation into `ListRepository`/`ReadFiles`, and have the model report when a listing was truncated so search results are not silently incomplete.

**Keep branch creation after the loop.** `CreateImplementationBranch` runs after generation today; keep it there so a context storm cannot leave orphan branches.

**Minor.** `relevantFiles` is unexported and has no direct test; the round-bound test needs a fake scripted with N request replies, which `llm.NewFake` handles fine.

---
<!-- COMMENTS:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Context selection is now a capability of the implementation agent. Each LLM round answers with one JSON object parsed by ChatJSON: either "outcome": "request" with list/search/read requests, or the implementation proposal. The workflow executes requests through the repository tooling activities (ListRepository, ReadFiles, and a new listing-based SearchRepository) and feeds the results back as a replayed conversation, so every provider and LLM call stays in an activity and the workflow body stays deterministic.

Bounds: at most maxContextRounds (3) request rounds then a propose-only final round that cannot issue requests (the prompt says a missing-context partial is valid only after requests were tried); an aggregate maxContextBudgetBytes (4 MiB) budget on top of the per-file (64 KiB) and seed count (40) limits. A request over budget is refused and named to the model and in the partial reason; the round bound is named too. Neither bound fails the run. The TASK-23 heuristic now only seeds the first prompt (renamed seedContext), so a task naming its files needs no extra round. ImplementationPrompt bumped to task-implementation/v2 and documents the protocol and bounds; README updated. Branch creation still happens after the loop.

Tests cover a model requesting files across two rounds (list, search, read), the round bound, the budget bound (refused request named, content never sent), a budget refusal that keeps a complete outcome, and the seed needing no round, plus activity/prompt/parsing unit tests. go vet and gofmt clean; full go test ./... and -race on the touched packages pass.

Follow-up (not in scope): native tool calling for the LLM client.
<!-- SECTION:FINAL_SUMMARY:END -->
