---
type: C4 Component
title: HTTP server
status: stable
groma:
  id: server
  parent: listener
  code:
    - scanner: go
      file: src/internal/server/server.go
  group: Shared event pipeline
  technology: net/http
description: Wires event sources, the dispatcher, and the HTTP server together; owns the listener lifecycle.
---

Builds the HTTP server around the shared dispatcher: registers GET /healthz (liveness), attaches the webhook event source with its forgejo and gitea provider adapters — feeding it the watched-repo config store from the listener configuration — and serves the routes the sources return (AddSource makes a new source one call away). Subscribe exposes the dispatcher to consumers (the event bridge that starts workflows, the logging consumer). Run serves until the context is canceled, then shuts down gracefully with a 10-second drain. Only the listener binary runs this server; the worker is a pure Temporal worker with no inbound port.
