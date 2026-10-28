---
id: TASK-5
title: Create Temporal worker for spawning autonomous agents on git events
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-26 12:25'
updated_date: '2026-10-26 12:43'
labels: []
dependencies:
  - TASK-4
references:
  - 'TASK-2: Autonomous refinement MR/PR creation agent'
  - 'TASK-4: Create webhook-based git event listener service'
type: feature
ordinal: 4000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:**
The Minas Tirith autonomous agent pipeline needs a way to reliably spawn agent work when repository events occur. TASK-4 delivered a webhook-based event listener that dispatches commit and comment events, and TASK-2 defines an autonomous refinement agent that should react to commits refining user stories. Today there is no execution layer that connects these events to agent workflows — events are dispatched but nothing durable picks them up and runs work. Temporal provides durable execution, retries, and visibility for long-running agent workflows, making it the right foundation for orchestrating autonomous agents.

**What needs to happen:**
Build a Temporal worker service that connects to a Temporal server, registers workflow and activity types through an extensible template system, and starts workflows when repository events arrive from the event listener (TASK-4). The worker must be designed so that new agent types can be added by registering additional worker templates without modifying core worker logic. Include a `userStoryReviewWorkflow` as the first concrete workflow — a skeleton that demonstrates the pattern for the TASK-2 refinement agent (triggered by commit events refining a story). The Temporal Go SDK should drive all workflow and activity execution. The worker subscribes to the listener's event dispatcher and maps commit events to workflow starts.

**Related information:**
- TASK-4 (done) provides the event listener with a `Dispatcher` API (`src/internal/event`) that dispatches provider-agnostic commit and comment events. The worker should subscribe to this dispatcher.
- TASK-2 (backlog) defines the full refinement agent behavior; this ticket delivers the execution infrastructure and a skeleton workflow, not the full agent logic.
- The listener runs as `src/cmd/listener`; the worker will be a new binary (e.g. `src/cmd/worker`) in the same Go module `minas-tirith`.
- Temporal server deployment (local dev via docker-compose or Temporal Cloud) is infrastructure setup that should be documented but may be a separate concern.
- The worker must handle Temporal server connectivity, graceful shutdown, and worker registration lifecycle.

**Technical Notes:**
- New code lives in `src/` (module `minas-tirith`): `cmd/worker` binary + `internal/worker` packages (temporal client setup, worker template registry, workflow definitions, event subscription adapter).
- Add `go.temporal.io/sdk` dependency to `src/go.mod`.
- Worker template pattern: an interface or registry where each agent type registers its workflows and activities; the worker binary iterates registered templates and starts a Temporal worker per template (or one worker with all types registered).
- `userStoryReviewWorkflow` skeleton: accepts a commit event payload (repo, branch, SHA, message), logs/validates the input, and is structured so TASK-2 can fill in the refinement logic later.
- Event subscription: the worker binary wires a subscriber to the listener's `Dispatcher` that calls `workflow.ExecuteWorkflow` on the Temporal client for matching commit events.
- Configuration via env/flags consistent with the listener: `TEMPORAL_HOST_PORT`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASK_QUEUE`, listener event source config.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A Temporal worker binary starts, connects to a Temporal server, and registers workflows/activities successfully
- [x] #2 New worker templates can be added by registering a template (workflows + activities) without modifying core worker startup code
- [x] #3 The userStoryReviewWorkflow is registered and can be started via the Temporal Go SDK with a commit event payload
- [x] #4 A commit event dispatched by the TASK-4 listener triggers the worker to start userStoryReviewWorkflow end-to-end (event to workflow execution visible in Temporal UI/CLI)
- [x] #5 The worker shuts down gracefully on SIGTERM, draining in-flight workflow tasks
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add go.temporal.io/sdk v1.50.0 to src/go.mod (done; go directive bumped to 1.26).
2. internal/worker core package: Template abstraction (Name/Workflows/Activities/Triggers), Runner (one Temporal SDK worker per template on task queue <base>-<template>, Start/Stop lifecycle, StartWorkflow-by-name with task-queue resolution), DialClient (eager connect, slog adapter).
3. internal/worker EventBridge: event.Handler that maps dispatched events to workflow starts via registered triggers — deterministic workflow ID (<workflow>-<event DedupeKey>), WorkflowIDReusePolicy AllowDuplicateFailedOnly so duplicate events don't double-run, WorkflowExecutionAlreadyStarted logged as dedupe.
4. internal/worker/userstoryreview: first template — userStoryReviewWorkflow skeleton (validate commit payload, AnalyzeCommit activity, structured for TASK-2 refinement logic) + commit-event trigger building the workflow input.
5. internal/config LoadWorker: TEMPORAL_HOST_PORT/TEMPORAL_NAMESPACE/TEMPORAL_TASK_QUEUE env+flags layered over the existing listener config (worker embeds the TASK-4 event server, since the Dispatcher is an in-process API).
6. cmd/worker binary: config -> temporal client -> Runner (register userstoryreview template) -> embedded TASK-4 webhook server -> subscribe EventBridge; SIGTERM stops ingestion, unsubscribes, drains Temporal workers, closes client.
7. Tests: runner registry (registration, duplicates, queue map), EventBridge with fake starter (commit->start, comment->skip, dedupe), config tests, userstoryreview workflow via SDK TestWorkflowEnvironment; go test ./... -race.
8. README: worker run/config docs + local Temporal server setup (temporal CLI dev server; docker-compose/Cloud pointers).
9. Live verification vs temporal server start-dev: worker connects+registers, replay captured Forgejo push fixture -> workflow visible in CLI/UI, duplicate delivery -> dedupe, manual workflow start, SIGTERM graceful drain.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented as the Temporal execution layer in src/ (module minas-tirith, SDK go.temporal.io/sdk v1.50.0):

- internal/worker (core): Template abstraction (Name/Workflows/Activities/Triggers) — one agent type = one template; Runner registers templates (validates unique template/workflow names, trigger wiring) and starts ONE Temporal SDK worker per template on its own task queue (<TEMPORAL_TASK_QUEUE>-<template>, default minas-tirith-userstoryreview); StartWorkflow-by-name resolves the task queue, uses AllowDuplicateFailedOnly reuse policy + WorkflowExecutionErrorWhenAlreadyStarted so event-triggered starts are idempotent; DialClient = eager client.DialContext routed through slog (SDK log.NewStructuredLogger), fails fast when the server is unreachable.
- internal/worker/bridge.go: EventBridge (event.Handler) subscribes to the TASK-4 Dispatcher; commit events -> matching triggers -> deterministic workflow ID (<workflow>-<event DedupeKey>) -> StartWorkflow; WorkflowExecutionAlreadyStarted logged as dedupe (skip), start failures logged and non-fatal, 30s RPC timeout.
- internal/worker/userstoryreview: first template. userStoryReviewWorkflow (type name userStoryReviewWorkflow) validates the commit payload (sha+repository required), runs userStoryReview.analyzeCommit activity with retry policy, returns Result{Reviewed, NeedsRefinement, Status}; TASK-2 seams marked in code. Commit trigger builds Input from the event (provider/project/repo/branch/sha/message/url/author).
- internal/config: LoadWorker layers TEMPORAL_HOST_PORT/TEMPORAL_NAMESPACE/TEMPORAL_TASK_QUEUE (env+flags, defaults localhost:7233/default/minas-tirith) over the existing listener config; listener flag/env resolution extracted into shared registerListenerFlags/resolve used by both binaries.
- cmd/worker: embeds the TASK-4 event server (dispatcher is an in-process API, so webhook ingestion runs in the worker process; deployments point repo webhooks at the worker), subscribes the EventBridge, runs until SIGTERM/SIGINT, then shuts down in order: HTTP server drain -> unsubscribe bridge -> Stop() Temporal workers (drains in-flight workflow/activity tasks) -> close client -> exit 0.
- README.md: worker run/config docs, Temporal server setup (CLI dev server + docker-compose/Cloud pointers), manual workflow start example, updated architecture tree.

Design decision: the worker embeds the TASK-4 listener rather than talking to it over IPC — subscription is the in-process Dispatcher API (TASK-4 final summary explicitly noted out-of-process transport as a possible follow-up). The listener binary stays for agent-less pipelines; a queue/SSE relay remains a follow-up if both binaries must consume simultaneously.

Verification:
- Unit: go test -race ./... all green (runner registry/lifecycle with stubbed SDK worker factory, bridge mapping/dedupe/error paths with fake starter incl. dispatcher end-to-end, LoadWorker env/flag/default/error layering, userstoryreview via Temporal testsuite: workflow happy path + invalid-input failure, activity via TestActivityEnvironment, InputFromEvent mapping, template wiring).
- Live against temporal server start-dev (CLI 1.8.2 / server 1.31.2), worker with WEBHOOK_SECRET + WATCHED_REPOS=BeanieGenie: connected and started the worker on minas-tirith-userstoryreview (AC1); replayed the captured Forgejo push fixture (testdata/forgejo-push.json, HMAC-signed) -> webhook accepted, commit event dispatched, userStoryReviewWorkflow started with workflow ID userStoryReviewWorkflow-forgejo|root/BeanieGenie|commit|<sha>, executed the analyzeCommit activity and completed; visible via temporal workflow list/describe: COMPLETED, result {reviewed:true, needs_refinement:false, status:'skeleton...'} (AC3, AC4). Duplicate redelivery suppressed at the listener (duplicates=1); re-delivery after worker restart (dedupe window lost) suppressed at the bridge via AllowDuplicateFailedOnly ('workflow already started for event; skipping duplicate') — still exactly one workflow per event (AC4). Manual start via temporal workflow start --type userStoryReviewWorkflow --task-queue minas-tirith-userstoryreview completed (AC3). SIGTERM -> 'event listener stopped' -> 'Stopped Worker' (SDK drain) -> 'temporal workers stopped' -> 'agent worker stopped', exit 0 (AC5). Two templates registered in runner unit test without core changes (AC2 pattern).
- Cleanup: temporal dev server stopped, verification worker processes stopped, no leftover state in the dev server that matters (it was ephemeral).
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Created the Temporal agent worker as a new binary (src/cmd/worker) in the minas-tirith module, connecting the TASK-4 event listener to durable agent execution on Temporal.

What changed:
- src/internal/worker: agent-agnostic Temporal execution layer — Template abstraction (workflows + activities + event triggers per agent type), Runner that starts one Temporal SDK worker per registered template on per-template task queues, workflow starts by type name with event-idempotent reuse policy, eager slog-routed client dial, and an EventBridge that subscribes to the listener's Dispatcher and maps commit events to workflow starts with deterministic workflow IDs.
- src/internal/worker/userstoryreview: first template — userStoryReviewWorkflow skeleton for the TASK-2 refinement agent (validates the commit payload, runs userStoryReview.analyzeCommit, completes with a skeleton status) plus its commit trigger; TASK-2 fills the refinement logic at marked seams.
- src/internal/config: LoadWorker (TEMPORAL_HOST_PORT/TEMPORAL_NAMESPACE/TEMPORAL_TASK_QUEUE env+flags, defaults localhost:7233/default/minas-tirith) layered over the shared listener config.
- src/cmd/worker: embeds the TASK-4 event server (in-process dispatcher subscription), subscribes the bridge, graceful SIGTERM shutdown (stop ingestion -> unsubscribe -> drain workers -> close client).
- src/README.md: worker run/config docs, Temporal server setup (CLI dev server, docker-compose, Cloud), manual workflow start example, updated architecture.
- go.mod: +go.temporal.io/sdk v1.50.0, go directive bumped to 1.26.0.

Why: events were dispatched with no durable execution layer to pick them up; Temporal gives the agents durable execution, retries, and visibility. Templates make new agents a registration, not a core change.

Verification: all unit tests pass incl. -race (runner registry/lifecycle, bridge mapping/dedupe/error paths, config layering, workflow+activity via the Temporal test suite). Live end-to-end against temporal server start-dev: worker connected and registered; a signed Forgejo push webhook (captured fixture) dispatched a commit event that started userStoryReviewWorkflow, which completed and was visible in temporal CLI with its result; duplicate redelivery suppressed at the listener and, across a worker restart, at the bridge (AllowDuplicateFailedOnly — one workflow per event); manual CLI start worked; SIGTERM produced the full graceful drain sequence and exit 0.

Risks/follow-ups: the worker embeds the listener because the dispatcher is in-process — running both binaries as consumers simultaneously needs the out-of-process transport follow-up flagged in TASK-4; the analyzeCommit skeleton has no real story detection (TASK-2); dev-server persistence is ephemeral (production deployments need the documented docker-compose/Cloud setup).
<!-- SECTION:FINAL_SUMMARY:END -->
