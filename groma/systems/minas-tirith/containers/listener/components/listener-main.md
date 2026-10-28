---
type: C4 Component
title: Listener main
status: stable
groma:
  id: listener-main
  parent: listener
  code:
    - scanner: go
      file: src/cmd/listener/main.go
  technology: Go command-line application
description: 'Entry point of the listener: loads configuration, wires the HTTP server, connects to Temporal, subscribes the event bridge to start workflows, and runs until signaled.'
---

Composition root of cmd/listener: loads the listener configuration (exits with status 2 on misconfiguration), builds the HTTP server with the webhook event source, dials the Temporal server (fail fast when unreachable), creates a starter and registers the agent templates (a new agent type is one Template implementation plus one Register call), subscribes the event bridge that maps dispatched events to workflow starts, and subscribes the built-in logging consumer that prints every dispatched event. Serves until SIGTERM/SIGINT, then drains gracefully.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/cmd/listener/main.go](../../../../../../src/cmd/listener/main.go) | [src/internal/server/server.go](../../../../../../src/internal/server/server.go) | Runs the HTTP server | Go package call |
| [src/cmd/listener/main.go](../../../../../../src/cmd/listener/main.go) | [src/internal/config/config.go](../../../../../../src/internal/config/config.go) | Loads listener configuration | Go package call |
| [src/cmd/listener/main.go](../../../../../../src/cmd/listener/main.go) | [src/internal/worker/client.go](../../../../../../src/internal/worker/client.go) | Dials the Temporal server | Go package call |
| [src/cmd/listener/main.go](../../../../../../src/cmd/listener/main.go) | [src/internal/worker/starter.go](../../../../../../src/internal/worker/starter.go) | Creates the workflow starter | Go package call |
| [src/cmd/listener/main.go](../../../../../../src/cmd/listener/main.go) | [src/internal/worker/bridge.go](../../../../../../src/internal/worker/bridge.go) | Subscribes the event bridge | Go package call |
