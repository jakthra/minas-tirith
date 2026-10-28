---
type: C4 Component
title: Forgejo adapter
status: stable
groma:
  id: forgejo-adapter
  parent: listener
  code:
    - scanner: go
      file: src/internal/source/webhook/forgejo.go
  group: Shared event pipeline
  technology: net/http
description: 'Forgejo/Gitea webhook adapter: HMAC-SHA256 delivery verification and push / merge-request-comment payload parsing.'
---

Adapts Forgejo (and Gitea, whose wire format it shares) deliveries to the provider-agnostic event model. Verify checks the HMAC-SHA256 signature over the raw body sent in X-Forgejo-Signature (with the X-Gitea-Signature / X-Gogs-Signature aliases). Parse turns push deliveries into one commit event per pushed commit (branch refs only; tag pushes and ref deletions are ignored) and issue-comment deliveries into a comment event when a new comment is created on a merge request; everything else yields no events. Header and payload shapes were captured from a live Forgejo 16 instance and are pinned by fixtures in src/internal/source/webhook/testdata.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/source/webhook/forgejo.go](../../../../../../src/internal/source/webhook/forgejo.go) | [src/internal/event/event.go](../../../../../../src/internal/event/event.go) | Parses deliveries into events | Go package call |
