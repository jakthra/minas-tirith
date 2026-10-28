---
type: C4 System
title: Temporal server
status: stable
groma:
  id: temporal-server
  technology: Temporal (gRPC frontend, go.temporal.io/sdk)
description: Durable execution backend that runs the agent workflows and activities.
---

The durable execution backend for the autonomous agents: a self-hosted Temporal cluster, Temporal Cloud, or the local dev server (temporal server start-dev, web UI on :8233; the worker's defaults match it). The worker's runners poll per-template task queues and execute the agent workflows and activities there; Temporal provides retries, durable state, and visibility through its UI and CLI.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [temporal-server](temporal-server.md) | [runner](../systems/minas-tirith/containers/worker/components/runner.md) | Dispatches workflow tasks | Temporal task queue polling |
