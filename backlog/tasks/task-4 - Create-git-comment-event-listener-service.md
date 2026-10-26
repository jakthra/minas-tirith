---
id: TASK-4
title: Create webhook-based git event listener service
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-20 08:10'
updated_date: '2026-10-26 12:15'
labels: []
dependencies: []
type: task
ordinal: 1000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:**
The autonomous agent pipeline planned for Minas Tirith (TASK-2 refinement agent, TASK-3 implementation agent) is triggered by repository activity: the implementation agent reacts when a commit adds a task to the backlog, the refinement flow starts when a user commits a new feature request, and the refinement agent iterates on user comments in its MR. Today nothing detects these events, so the agents have no trigger to act on.

**What needs to happen:**
A standalone event-driven listener service that receives webhooks from the local Forgejo instance and dispatches new commits and new MR comments as structured events that downstream consumers can subscribe to. Detection goes through a pluggable event-source abstraction: this ticket implements a webhook adapter as the first source. Future sources can be added behind the same abstraction without reworking event dispatch.

**Related information:**
Webhooks are chosen over polling for immediate, event-driven triggers with no polling-interval latency and no per-event-type detection logic. The service exposes an authenticated HTTP endpoint, and Forgejo pushes events to it as repository webhooks. Forgejo redelivers failed webhook deliveries, so the service must tolerate duplicate deliveries without dispatching duplicate events. Other event types (for example MR state changes) are follow-up work behind the same abstraction. MR/PR creation is not standardized across providers, requires provider-specific information, and is out of scope. What consumers do with dispatched events is owned by TASK-2 and TASK-3.

**Technical Notes:**
The Forgejo instance is defined in `forgejo/docker-compose.yaml`, so the listener must be reachable from it (shared Docker network or exposed port); `repos/BeanieGenie` can act as the watched repository for verification. No listener code exists yet, so this adds a new service.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A new commit on a watched repository is dispatched as a structured, provider-agnostic commit event, and a subscribed consumer receives it
- [x] #2 A new comment on a merge request is dispatched as a structured, provider-agnostic comment event, and a subscribed consumer receives it
- [x] #3 Inbound webhook requests are authenticated with a shared secret, and unauthenticated requests are rejected
- [x] #4 Watched repositories, webhook endpoint, and shared secret are configurable without code changes
- [x] #5 Duplicate webhook deliveries (for example provider redelivery) do not result in duplicate dispatched events
- [x] #6 The webhook route should be /v1/{project}/webhooks/{provider}
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. New Go module in src/ (module minas-tirith), stdlib net/http only: cmd/listener binary + internal packages (config, event, dedupe, source/webhook + forgejo adapter).
2. Event model: provider-agnostic Event{Kind=commit|comment, Provider, Project, Repository, Commit|Comment}; Dispatcher with Subscribe/Dispatch (async fan-out, unsubscribe support) is the consumer API.
3. Pluggable source abstraction (source.EventSource with Start(ctx, dispatcher) returning HTTP routes); first source = webhook adapter: POST /v1/{project}/webhooks/{provider}, provider adapters (forgejo, gitea alias) handle auth (HMAC-SHA256 via X-Forgejo/X-Gitea-Signature) + payload parsing.
4. Dedupe: in-memory TTL store keyed on event identity (provider/repo/commit-SHA, provider/repo/comment-ID), applied before dispatch so redeliveries never double-dispatch.
5. Config via flags + env (LISTEN_ADDR, WEBHOOK_SECRET, WATCHED_REPOS, DEDUPE_TTL) — no code changes needed to reconfigure.
6. Tests: unit tests for dispatcher/dedupe/config/forgejo parser with captured real Forgejo 16 payloads as fixtures; httptest tests for route/auth/dedupe behavior.
7. Live verification against the local Forgejo (root/BeanieGenie): webhook -> http://169.254.1.2:8080/v1/BeanieGenie/webhooks/forgejo, push commit, comment on MR, duplicate delivery, unauthenticated request rejection.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented the listener in src/ as a stdlib-only Go webservice (module minas-tirith): cmd/listener + internal/{config,event,dedupe,source,source/webhook,server}. Forgejo adapter parses push + MR-comment deliveries; header/payload formats were captured from the live Forgejo 16.0.3 instance and are used as test fixtures. All unit + integration tests pass (incl. -race).

Live verification against the local Forgejo 16.0.3 instance (root/BeanieGenie), with the listener running on :8080 and the repo webhook pointed at http://169.254.1.2:8080/v1/BeanieGenie/webhooks/forgejo:
- Pushed commit -> dispatched as provider-agnostic commit event, received by the subscribed logging consumer.
- Commented on MR !1 -> dispatched as comment event with merge request reference, received by the consumer.
- Forged and missing signatures -> 401 rejected; unwatched project -> 404; ignored event types -> 200 (no provider redelivery).
- Re-push of a branch containing an already-dispatched commit: delivery contained 5 commits, dispatched=4 duplicates=1 — the already-seen SHA was suppressed (dedupe by event identity, not delivery ID).
- SIGTERM -> graceful shutdown.
Environment changes required for the integration (documented in src/README.md): forgejo/docker-compose.yaml now sets FORGEJO__webhook__ALLOWED_HOST_LIST=private,169.254.0.0/16 (Forgejo 16 defaults to 'external' and denies ALL local webhook targets). Rootless podman reaches host services via the pasta host-mapping address 169.254.1.2.
Cleanup done: test branches deleted, test PR !1 closed, verification processes stopped. The repo webhook on BeanieGenie is left configured for the listener (secret 'task4-test-secret' — change it before real use). A Forgejo access token 'task4-webhook-verification' (full admin scopes) was created for verification and could not be revoked via API (token deletion needs the account password) — revoke it in Forgejo UI: Settings -> Applications.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Created the webhook-based git event listener as a stdlib-only Go webservice in src/ (module minas-tirith, cmd/listener + internal packages).

What changed:
- src/: new Go module with the listener service. POST /v1/{project}/webhooks/{provider} receives webhooks, authenticates them (HMAC-SHA256 via X-Forgejo/X-Gitea-Signature), parses them through provider adapters (forgejo + gitea alias), drops duplicate deliveries via a TTL event-identity store, and dispatches provider-agnostic commit and comment events to subscribers (async fan-out; built-in logging consumer). Detection goes through the pluggable source.EventSource abstraction so future sources (polling, queues) and providers need no dispatch rework.
- forgejo/docker-compose.yaml: added FORGEJO__webhook__ALLOWED_HOST_LIST=private,169.254.0.0/16 — required; Forgejo 16 by default denies webhooks to ALL local targets (verified via delivery logs).
- src/README.md: run/config instructions, Forgejo webhook wiring (incl. the rootless-podman host address 169.254.1.2), event model, dedupe semantics.

Why: nothing detected repository activity to trigger the TASK-2/TASK-3 agents; webhooks give immediate, event-driven triggers.

Verification: all unit + integration tests pass incl. -race (fixtures captured from the live Forgejo 16.0.3). Live end-to-end against the real instance: push -> commit event dispatched to the subscribed consumer; MR comment -> comment event; forged/missing signature -> 401; unwatched project -> 404; ignored events -> 200; duplicate delivery of an already-dispatched commit suppressed (dispatched=4 duplicates=1); graceful shutdown on SIGTERM.

Risks/follow-ups: dedupe store is in-memory (a restart forgets the window — duplicate delivered across a restart would re-dispatch); MR state changes and other event types are follow-up work behind the same abstraction; consumer transport for out-of-process agents (e.g. SSE/queue endpoint) is a possible follow-up — current subscription is the in-process Dispatcher API. The BeanieGenie webhook secret is a test value ('task4-test-secret') — change it before real use, and revoke the 'task4-webhook-verification' Forgejo token (Settings -> Applications).
<!-- SECTION:FINAL_SUMMARY:END -->
