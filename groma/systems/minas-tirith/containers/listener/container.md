---
type: C4 Container
title: Event listener
status: stable
groma:
  id: listener
  parent: minas-tirith
  technology: Go command-line application
description: Webhook event listener binary (cmd/listener) that starts agent workflows on Temporal.
---

The listener binary: loads its configuration, wires the HTTP server with the webhook event source, connects to Temporal, registers the agent templates on a starter (to learn the workflow-to-task-queue mapping and event triggers), subscribes the event bridge that maps dispatched events to workflow starts, and serves until SIGTERM/SIGINT, then drains gracefully. The agent worker (cmd/worker) executes the workflows the listener starts.
