---
type: C4 System
title: Forgejo
status: stable
groma:
  id: forgejo
  technology: Forgejo 16 (Gitea-compatible git forge)
description: Self-hosted git forge hosting the watched repositories; delivers signed webhook events and serves the repository REST API.
---

The self-hosted git forge that hosts the watched repositories (deployment: forgejo/docker-compose.yaml; the watched repository root/BeanieGenie is used for verification). Forgejo interacts with minas-tirith in both directions. Inbound: it delivers repository activity as signed webhooks — push events and issue-comment events (which carry merge request comments) — to POST /v1/{project}/webhooks/{provider}; the delivery format and headers were captured from a live Forgejo 16.0.3 instance and are pinned by fixtures in src/internal/source/webhook/testdata. Outbound: it serves the Gitea-compatible REST API (v1) the provider repository client calls with an access token — fetching files and directories, creating branches, committing file changes, and opening and discussing merge requests. Gitea shares the wire formats; the adapter and the client serve both provider names.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [forgejo](forgejo.md) | [webhook](../systems/minas-tirith/containers/listener/components/webhook.md) | Delivers signed webhook payloads | HTTP POST /v1/{project}/webhooks/{provider}, HMAC-SHA256 |
