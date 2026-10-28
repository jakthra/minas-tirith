---
type: C4 Component
title: Starter
status: stable
groma:
  id: starter
  parent: worker
  code:
    - scanner: go
      file: src/internal/worker/starter.go
  group: Temporal execution
  technology: Temporal Go SDK
description: Starts workflows by type name on the task queues that registered templates declare, without running Temporal workers.
---

NewStarter + Register + StartWorkflow: the starter is the listener's Temporal client. It wraps the Temporal client connection, learns the workflow-to-task-queue mapping from registered templates (each template's queue is the base queue plus "-" plus the template name), and starts workflows with WorkflowIDReusePolicy AllowDuplicateFailedOnly plus WorkflowExecutionErrorWhenAlreadyStarted — re-using the ID of a completed workflow is rejected unless it failed — which makes event-triggered starts idempotent. The worker's runner embeds the starter and adds the Temporal workers that execute the started workflows.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/worker/starter.go](../../../../../../src/internal/worker/starter.go) | [src/internal/worker/template.go](../../../../../../src/internal/worker/template.go) | Reads triggers from templates | Go package call |
| [starter](starter.md) | [temporal-server](../../../../../externals/temporal-server.md) | Starts workflows | Temporal gRPC |
