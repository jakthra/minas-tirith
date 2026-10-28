---
type: C4 Component
title: Event source
status: stable
groma:
  id: source
  parent: listener
  code:
    - scanner: go
      file: src/internal/source/source.go
  group: Shared event pipeline
description: Pluggable EventSource abstraction through which repository activity is detected and dispatched.
---

An EventSource is anything that can produce events: a passive webhook receiver (returns the HTTP routes it needs served) or an active producer such as a poller or queue consumer (starts its own producers, returns no routes). Start attaches the source to the dispatcher, so new sources can be added without reworking event dispatch. Route describes one HTTP endpoint a source needs served; the Dispatcher interface is the sink into which sources dispatch (satisfied by the event dispatcher). The webhook source is the first implementation.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/source/source.go](../../../../../../src/internal/source/source.go) | [src/internal/event/event.go](../../../../../../src/internal/event/event.go) | Defines the dispatch sink | Go interface definition |
