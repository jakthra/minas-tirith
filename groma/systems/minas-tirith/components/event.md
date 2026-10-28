---
type: C4 Component
title: Events
status: stable
groma:
  id: event
  parent: minas-tirith
  code:
    - scanner: go
      file: src/internal/event/event.go
    - scanner: go
      file: src/internal/event/dispatcher.go
  group: Shared event pipeline
description: Provider-agnostic event model and the dispatcher that fans events out to subscribers.
---

The event model normalizes repository activity into Event values — a Kind (commit or comment), the provider, project and repository, and exactly one kind-specific payload (Commit or Comment) — so no provider payload format leaks to consumers. Event.DedupeKey gives every activity a stable identity (provider + repository + commit SHA or comment ID), used both for duplicate suppression and for workflow IDs. The Dispatcher fans each event out to all subscribed handlers, one goroutine per handler with a context detached from the webhook request, so slow consumers never delay the delivery path; a panicking handler is logged, not fatal. event.go holds the model, dispatcher.go the fan-out.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/event/dispatcher.go](../../../../src/internal/event/dispatcher.go) | [src/internal/worker/bridge.go](../../../../src/internal/worker/bridge.go) | Delivers events to the bridge | Go callback invocation |
| [src/internal/event/dispatcher.go](../../../../src/internal/event/dispatcher.go) | [src/cmd/listener/main.go](../../../../src/cmd/listener/main.go) | Delivers events for logging | Go callback invocation |
