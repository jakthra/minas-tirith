---
type: C4 Component
title: Worker main
status: stable
groma:
  id: worker-main
  parent: worker
  code:
    - scanner: go
      file: src/cmd/worker/main.go
  technology: Go command-line application, Temporal Go SDK
description: 'Entry point of the worker: loads configuration, dials Temporal, registers agent templates, starts Temporal workers, and runs until signaled.'
---

Composition root of cmd/worker: loads the worker configuration, dials the Temporal server (fail fast when unreachable), creates the runner and registers the agent templates (a new agent type is one Template implementation plus one Register call), starts the Temporal workers, and waits until SIGTERM/SIGINT. The worker is a pure Temporal worker: it opens no inbound port and receives no webhooks; the listener starts the workflows it executes. Shutdown drains the Temporal workers, letting in-flight workflow and activity tasks finish before the process exits.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/cmd/worker/main.go](../../../../../../src/cmd/worker/main.go) | [src/internal/config/worker.go](../../../../../../src/internal/config/worker.go) | Loads worker configuration | Go package call |
| [src/cmd/worker/main.go](../../../../../../src/cmd/worker/main.go) | [src/internal/worker/client.go](../../../../../../src/internal/worker/client.go) | Dials the Temporal server | Go package call |
| [src/cmd/worker/main.go](../../../../../../src/cmd/worker/main.go) | [src/internal/worker/runner.go](../../../../../../src/internal/worker/runner.go) | Creates the runner and starts workers | Go package call |
| [src/cmd/worker/main.go](../../../../../../src/cmd/worker/main.go) | [src/internal/worker/userstoryreview/template.go](../../../../../../src/internal/worker/userstoryreview/template.go) | Registers the agent template | Go package call |
