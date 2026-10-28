---
type: C4 Component
title: Webhook source
status: stable
groma:
  id: webhook
  parent: listener
  code:
    - scanner: go
      file: src/internal/source/webhook/webhook.go
  group: Shared event pipeline
  technology: net/http
description: 'Webhook event source: authenticated HTTP endpoint that receives provider deliveries, drops duplicates, and dispatches the resulting events.'
---

Serves POST /v1/{project}/webhooks/{provider}: {project} selects the watched project, {provider} the adapter. The watched repositories and their per-repo webhook secrets come from the config store: Start loads them once and each delivery is verified with the addressed project's own secret, so a broken declaration fails startup loudly. Deliveries for projects not declared in the config store or unknown providers get 404 (rather than 403, to avoid revealing which projects exist); unauthenticated deliveries (provider signature verification) get 401; non-JSON bodies get 415; bodies are capped at 10 MiB. The provider adapter verifies and parses the delivery into zero or more events — irrelevant deliveries (other event types, comment edits, plain issue comments) yield no events and return 200 so the provider does not redeliver. Each event passes the dedupe store before dispatch, so redeliveries never reach consumers twice. Providers register by name (the HTTP server component registers forgejo and gitea); a new provider is one RegisterProvider call.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/source/webhook/webhook.go](../../../../../../src/internal/source/webhook/webhook.go) | [src/internal/source/webhook/forgejo.go](../../../../../../src/internal/source/webhook/forgejo.go) | Delegates verification and parsing | Go interface call |
| [src/internal/source/webhook/webhook.go](../../../../../../src/internal/source/webhook/webhook.go) | [src/internal/event/dispatcher.go](../../../../../../src/internal/event/dispatcher.go) | Dispatches parsed events | Go interface call |
| [src/internal/source/webhook/webhook.go](../../../../../../src/internal/source/webhook/webhook.go) | [src/internal/dedupe/dedupe.go](../../../../../../src/internal/dedupe/dedupe.go) | Drops duplicate deliveries | Go package call |
| [src/internal/source/webhook/webhook.go](../../../../../../src/internal/source/webhook/webhook.go) | [src/internal/source/source.go](../../../../../../src/internal/source/source.go) | Implements the source abstraction | Go interface implementation |
| [src/internal/source/webhook/webhook.go](../../../../../../src/internal/source/webhook/webhook.go) | [src/internal/configstore/store.go](../../../../../../src/internal/configstore/store.go) | Loads watched repositories | Go interface call |
