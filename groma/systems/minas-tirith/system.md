---
type: C4 System
title: Minas Tirith
status: stable
groma:
  id: minas-tirith
  technology: Go (standard library + Temporal Go SDK)
description: 'Event-driven services that connect repository activity to autonomous agents: a webhook event listener that starts workflows on Temporal, and a Temporal agent worker that executes them.'
---

Two command-line applications built from one Go module (src/):

- **Event listener** (cmd/listener) — receives provider webhooks, verifies their authenticity, drops duplicate deliveries, and starts the agent workflows the events trigger on Temporal. The agent worker executes those workflows.
- **Agent worker** (cmd/worker) — a pure Temporal worker: it polls task queues and executes the registered agent workflows and activities. It opens no inbound port and receives no webhooks.

Both binaries are built from shared packages. The event pipeline packages (internal/config, internal/event, internal/dedupe, internal/source, internal/source/webhook, internal/server) serve the listener; the Temporal execution layer (internal/worker) is shared — the listener uses its starter and event bridge to start workflows, the worker uses its runner to execute them.

Externally, Forgejo delivers the webhooks and a Temporal server provides durable execution. The activity the system detects is the repository contributor's: pushes and merge request comments.
