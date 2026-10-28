---
type: C4 Container
title: Agent worker
status: stable
groma:
  id: worker
  parent: minas-tirith
  technology: Go command-line application, Temporal Go SDK
description: 'Agent worker binary (cmd/worker): a pure Temporal worker that executes agent workflows.'
---

The worker binary: loads its configuration, dials the Temporal server, registers agent templates and starts one Temporal worker per template, and waits for workflows to execute. It opens no inbound port and receives no webhooks; the event listener (cmd/listener) starts the workflows it executes. On SIGTERM/SIGINT it drains the Temporal workers, letting in-flight workflow and activity tasks finish before the process exits.
