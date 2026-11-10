---
id: TASK-27.2
title: Add per-repo access token and write support to the watched-repos config store
status: To Do
assignee: []
created_date: '2026-11-10 12:05'
labels: []
dependencies: []
references:
  - src/internal/configstore/store.go
  - src/internal/configstore/file.go
  - src/watched.yaml
parent_task_id: TASK-27
type: feature
ordinal: 28000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:** TASK-27 decided a per-project token model: each watched repository carries its own access token. `configstore.Repository` today has only `Project` and the webhook `Secret`, and the `File` store is read-only — nothing can add a repository to the declarative config.

**What needs to happen:** Extend the watched-repos YAML schema with an optional per-repo access token field on each entry, and give the file store an add/update operation that persists a repository entry back to the YAML file while preserving all other entries. Worker-side consumption of the per-repo token for provider API calls (replacing the global `FORGEJO_TOKEN` there) is a follow-up, not this task.

**Related information:** The listener already reads per-repo webhook secrets from this store; the token rides alongside the secret in the same entry so the installer writes one place.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `configstore.Repository` carries an optional per-repo access token, parsed from and serialized to the YAML entry
- [ ] #2 The file store exposes an add/update operation that persists the entry to the config file, preserving every other existing entry
- [ ] #3 Loading the persisted file returns the added/updated repository with its token and secret intact
- [ ] #4 Unit tests cover token parsing, add vs. update of an existing project, and reload-after-write against a temp file
<!-- AC:END -->
