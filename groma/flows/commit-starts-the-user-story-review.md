---
type: Groma Flow
title: Commit starts the user story review
groma:
  id: commit-starts-the-user-story-review
---

End-to-end: a repository contributor pushes a commit to a watched repository; Forgejo delivers the signed webhook; the webhook source verifies the delivery, the Forgejo adapter parses it into a commit event, dedupe drops redeliveries, and the dispatcher fans the event out. In the listener, the event bridge matches the user story review trigger and starts userStoryReviewWorkflow on Temporal through the starter. The worker's runner picks the workflow up from the agent task queue and executes it. The same activity dispatched twice — a provider redelivery, or an event re-dispatched across a listener restart — starts the workflow once: the listener's dedupe store suppresses redeliveries within the TTL window, and the starter's workflow-ID reuse policy suppresses them across restarts.

## Steps

| From | To | Action |
| --- | --- | --- |
| [Repository contributor](../actors/repository-contributor.md) | [Forgejo](../externals/forgejo.md) | Push a commit to a watched repository |
| [Forgejo](../externals/forgejo.md) | [Webhook source](../systems/minas-tirith/containers/listener/components/webhook.md) | Deliver the signed push webhook |
| [Webhook source](../systems/minas-tirith/containers/listener/components/webhook.md) | [Events](../systems/minas-tirith/components/event.md) | Verify, parse, dedupe, and dispatch the commit event |
| [Events](../systems/minas-tirith/components/event.md) | [Event bridge](../systems/minas-tirith/containers/listener/components/bridge.md) | Deliver the commit event to the subscribed bridge |
| [Event bridge](../systems/minas-tirith/containers/listener/components/bridge.md) | [Starter](../systems/minas-tirith/containers/worker/components/starter.md) | Start userStoryReviewWorkflow for the commit |
| [Starter](../systems/minas-tirith/containers/worker/components/starter.md) | [Temporal server](../externals/temporal-server.md) | Submit the workflow start |
| [Temporal server](../externals/temporal-server.md) | [Worker runner](../systems/minas-tirith/containers/worker/components/runner.md) | Dispatch the workflow to the agent task queue |
| [Worker runner](../systems/minas-tirith/containers/worker/components/runner.md) | [User story review agent](../systems/minas-tirith/containers/worker/components/userstoryreview.md) | Execute userStoryReviewWorkflow |
