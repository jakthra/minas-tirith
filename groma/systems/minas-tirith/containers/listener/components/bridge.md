---
type: C4 Component
title: Event bridge
status: stable
groma:
  id: bridge
  parent: listener
  code:
    - scanner: go
      file: src/internal/worker/bridge.go
  technology: Go
description: Maps dispatched repository events to workflow starts through the registered triggers.
---

The bridge is an event handler subscribed to the listener's dispatcher. For each dispatched event it finds the triggers whose kind matches, builds the workflow input, and starts the trigger's workflow through the starter. The workflow ID derives from the event identity (workflow name + "-" + event DedupeKey), so a redelivered event maps to the same execution instead of a duplicate. Starts are bounded by a 30-second RPC timeout; an already-started workflow is logged as a duplicate rather than an error, and other start failures are logged and non-fatal (the webhook that dispatched the event has already been answered).

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/worker/bridge.go](../../../../../../src/internal/worker/bridge.go) | [src/internal/worker/starter.go](../../../../../../src/internal/worker/starter.go) | Starts triggered workflows | Go interface call |
