---
id: TASK-27.3
title: 'Scaffold the interactive cmd/install prompt flow (provider, repo, token)'
status: To Do
assignee: []
created_date: '2026-11-10 12:05'
updated_date: '2026-11-10 12:08'
labels: []
dependencies: []
references:
  - src/cmd/listener/main.go
  - src/internal/config/config.go
parent_task_id: TASK-27
type: feature
ordinal: 29000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:** TASK-27 wants a single interactive binary instead of hand-editing `watched.yaml`/`.env` and clicking through the Forgejo UI. This task scaffolds only the prompt flow of the new `cmd/install` binary; provider API calls and config writes are wired in a follow-up.

**What needs to happen:** Create `cmd/install` with an interactive flow over injectable stdin/stdout (so tests drive it without a TTY): choose the git provider (Forgejo offered today), enter the repository as owner/name, and enter the per-repository access token without echoing it when stdin is a terminal (piped stdin must still work for scripts and tests). Build the command/flag structure with cobra (`spf13/cobra`) or an equivalent standard CLI framework rather than hand-rolled flag parsing, and record which one was chosen — this adds the first third-party CLI dependency to `go.mod` (the module so far is stdlib + Temporal SDK + YAML parser), so run `go mod tidy` and keep the env/`.env` layering compatible with `internal/config`.

**Related information:** No provider API calls in this task — that is the wiring follow-up (TASK-27.4). The token prompt must never write the entered token to stdout/stderr.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Running `cmd/install` starts an interactive flow that asks for the git provider and offers Forgejo
- [ ] #2 It then asks for the repository as owner/name and validates the format
- [ ] #3 It asks for the per-repo access token and does not echo it on a terminal; piped stdin still works
- [ ] #4 Prompts read from injectable stdin/stdout, and unit tests drive the full prompt sequence without a TTY
- [ ] #5 The `cmd/install` command and flag structure is built with cobra or an equivalent standard CLI framework, with the dependency added to `go.mod`
<!-- AC:END -->
