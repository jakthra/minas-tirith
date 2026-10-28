---
type: C4 Component
title: Agent template
status: stable
groma:
  id: worker-template
  parent: worker
  code:
    - scanner: go
      file: src/internal/worker/template.go
  group: Temporal execution
description: 'Template abstraction defining an agent type: its workflows, activities, and the repository events that start them.'
---

A Template is one agent type: the workflow and activity implementations it registers, and the Triggers that map event kinds to workflow starts (Trigger.Input builds the workflow input from a dispatched event and reports false for events that should not start it). The runner and the event bridge are agent-agnostic; adding a new agent means implementing Template and registering it, without touching worker startup or event wiring.
