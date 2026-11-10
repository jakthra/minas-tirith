---
id: TASK-27.4
title: >-
  Wire the installer end-to-end: create webhook, generate secret, write watched
  config
status: To Do
assignee: []
created_date: '2026-11-10 12:05'
labels: []
dependencies:
  - TASK-27.1
  - TASK-27.2
  - TASK-27.3
references:
  - src/cmd/listener/main.go
  - src/internal/provider/fake.go
  - src/internal/configstore/file.go
parent_task_id: TASK-27
type: feature
ordinal: 30000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:** TASK-27 (interactive installer) only delivers when the prompt flow, the provider webhook operation, and the config-store write are wired together in `cmd/install`. Today each step exists (or will exist) separately; nothing runs them as one onboarding.

**What needs to happen:** Wire `cmd/install` end-to-end: collect provider/repo/token via the prompt flow, generate a fresh webhook secret, create the webhook on the chosen Forgejo repository through the provider client targeting the listener webhook endpoint (URL configurable, e.g. flag/env following the existing layering), then write the repository entry (project, generated secret, per-repo token) into the watched-repos config file, and print a summary of what was configured (repo, webhook URL, config path). Handle partial failure loudly: a failed webhook creation must not leave a config entry behind, and vice versa.

**Related information:** Depends on TASK-27.1 (provider webhook op), TASK-27.2 (configstore token + write), TASK-27.3 (prompt flow). Tests use the `Fake` provider and a temp config file — no live Forgejo required (per TASK-27, done means binary + unit tests).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Running the installer end-to-end (Fake provider + temp config in tests) creates the webhook on the chosen repo pointing at the configured listener URL with a freshly generated secret
- [ ] #2 It writes the repo entry (project, secret, per-repo token) into the watched config file so the listener watches the repository
- [ ] #3 On success it prints a summary naming the repository, webhook URL, and config file path
- [ ] #4 A failed webhook creation leaves no config entry (and a failed config write is reported), covered by unit tests
- [ ] #5 Unit tests cover the full flow with `Fake` and a temp config; `go test ./...` passes
<!-- AC:END -->
