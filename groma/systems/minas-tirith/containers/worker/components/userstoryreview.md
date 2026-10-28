---
type: C4 Component
title: User story review agent
status: stable
groma:
  id: userstoryreview
  parent: worker
  code:
    - scanner: go
      file: src/internal/worker/userstoryreview/workflow.go
    - scanner: go
      file: src/internal/worker/userstoryreview/activities.go
      symbol: AnalyzeCommit
    - scanner: go
      file: src/internal/worker/userstoryreview/template.go
  technology: Temporal Go SDK
description: 'First agent template: the userStoryReviewWorkflow skeleton for the TASK-2 refinement agent, started by commit events.'
---

The userstoryreview template plugs the first agent into the worker. userStoryReviewWorkflow validates the commit payload (repository and SHA required), runs the userStoryReview.analyzeCommit activity with a retry policy, and completes with a Result; the refinement itself (detect the refined story, generate implementation tickets, open an MR, iterate on comments) is filled in by TASK-2 at the marked seams. Its trigger starts the workflow on commit events, building the input from the event; comment events (the refinement MR feedback loop) are TASK-2 follow-up. The template runs on the minas-tirith-userstoryreview task queue. Today the analyze activity is a skeleton that reports no refinement needed, so the workflow completes and is visible in Temporal while the agent logic is pending.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/worker/userstoryreview/template.go](../../../../../../src/internal/worker/userstoryreview/template.go) | [src/internal/worker/template.go](../../../../../../src/internal/worker/template.go) | Implements the template interface | Go interface implementation |
| [src/internal/worker/userstoryreview/template.go](../../../../../../src/internal/worker/userstoryreview/template.go) | [src/internal/event/event.go](../../../../../../src/internal/event/event.go) | Builds workflow input from events | Go package call |
