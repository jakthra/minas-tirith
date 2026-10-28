---
type: C4 Component
title: Provider repository client
status: stable
groma:
  id: provider
  parent: minas-tirith
  code:
    - scanner: go
      file: src/internal/provider/provider.go
    - scanner: go
      file: src/internal/provider/forgejo.go
    - scanner: go
      file: src/internal/provider/fake.go
  technology: Go, Forgejo REST API v1 (Gitea-compatible)
description: Forgejo/Gitea repository API client behind the provider-agnostic Client interface the agent activities use for repository access.
---

Outbound counterpart to the inbound webhook adapter: where the adapter receives repository activity, this component acts on the repository through the provider REST API. It covers the operations the autonomous agents need — fetch file contents by path and ref (backlog items, source code), list a directory recursively, create a branch, write one or more file changes as a single commit (creating or updating each file as needed), open and fetch merge requests, and read and add MR comments. Repositories are addressed by full name (root/BeanieGenie) and refs are branch names or commit SHAs.

Everything sits behind the provider.Client interface so Temporal activity code depends on the abstraction, never the wire format: ForgejoClient implements it against the Forgejo API (which Gitea shares, authenticated by access token — FORGEJO_URL/FORGEJO_TOKEN in the worker config), and Fake is an in-memory substitute so activity tests run without real API calls. Errors are provider-agnostic too: 404s map to ErrNotFound, other non-2xx responses to *APIError, letting activity logic distinguish 'missing' from 'rejected' without HTTP details. The wire shapes were verified against the live Forgejo 16 dev instance (read operations and swagger), including its multi-file contents endpoint that carries the single-commit writes.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [src/internal/provider/forgejo.go](../../../../src/internal/provider/forgejo.go) | [forgejo](../../../externals/forgejo.md) | Fetches and updates repository content | HTTP, Forgejo REST API v1 (Gitea-compatible), access token |
