---
type: Groma Project
title: Minas Tirith
groma:
  profile: architecture
description: Event-driven services that connect repository activity to autonomous agents.
---

Minas Tirith is a Go module (src/) with two binaries. The **event listener** (cmd/listener) receives Forgejo repository webhooks, verifies their authenticity, drops duplicate deliveries, and starts the agent workflows the events trigger on Temporal. The **agent worker** (cmd/worker) is a pure Temporal worker: it polls task queues and executes the registered agent workflows and activities, opening no inbound port. Repository contributors trigger the pipeline by pushing commits and commenting on merge requests; the autonomous agents (TASK-2 refinement, TASK-3 implementation) execute the workflows the listener starts. See src/README.md for run and configuration instructions.
