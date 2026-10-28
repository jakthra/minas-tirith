---
type: C4 Component
title: Worker runner
status: stable
groma:
  id: runner
  parent: worker
  code:
    - scanner: go
      file: src/internal/worker/runner.go
  group: Temporal execution
  technology: Temporal Go SDK
description: 'Template registry and Temporal worker lifecycle: one worker per template, embedding the starter for workflow starts.'
---

NewRunner + Register + Start: the runner validates templates (unique names, trigger wiring), then starts one Temporal SDK worker per template on that template's own task queue (base queue + "-" plus template name, e.g. minas-tirith-userstoryreview), so agents poll independently of each other. The runner embeds the starter: template registration, task-queue resolution, trigger aggregation, and workflow starts come from the starter; the runner adds the workers that execute the started workflows. Stop drains in-flight workflow and activity tasks of every worker. Registering after Start is rejected.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [runner](runner.md) | [temporal-server](../../../../../externals/temporal-server.md) | Executes workflows on task queues | Temporal SDK workers |
| [src/internal/worker/runner.go](../../../../../../src/internal/worker/runner.go) | [src/internal/worker/userstoryreview/workflow.go](../../../../../../src/internal/worker/userstoryreview/workflow.go) | Executes the template workflows | Temporal SDK workers |
