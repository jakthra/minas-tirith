---
id: TASK-27.2
title: Add per-repo access token and write support to the watched-repos config store
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-10 12:05'
updated_date: '2026-11-10 20:27'
labels: []
dependencies: []
references:
  - src/internal/configstore/store.go
  - src/internal/configstore/file.go
  - src/watched.yaml
modified_files:
  - src/internal/configstore/store.go
  - src/internal/configstore/file.go
  - src/internal/configstore/file_test.go
  - src/README.md
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
- [x] #1 `configstore.Repository` carries an optional per-repo access token, parsed from and serialized to the YAML entry
- [x] #2 The file store exposes an add/update operation that persists the entry to the config file, preserving every other existing entry
- [x] #3 Loading the persisted file returns the added/updated repository with its token and secret intact
- [x] #4 Unit tests cover token parsing, add vs. update of an existing project, and reload-after-write against a temp file
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. store.go: add optional Token field to Repository (per-repo provider access token; rides alongside the webhook secret, unused by the listener).
2. file.go: add token to the YAML entry (omitempty), map it through load validation, and add (*File).AddOrUpdate(Repository) — read the existing file (empty document when missing), validate the input, upsert by project, validate the merged document with the same rules as load, then atomically write (temp file + rename, preserving the existing file mode, 0o600 for new files).
3. file_test.go: token parsing (present and absent), add vs update of an existing project preserving other entries, reload-after-write round trip, file creation when missing, serialized YAML shape, and validation failures leaving the file untouched.
4. README.md: document the optional token field in the watched-config section.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented: Repository.Token (optional, yaml token with omitempty), File.AddOrUpdate with input validation, in-place update by project, merged-document validation before write, and an atomic temp-file+rename write that creates missing files at mode 600 and preserves an existing file's mode. Repositories() now shares a parse helper; its error behavior is unchanged. README documents the token field. All tests pass incl. -race.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Extended the watched-repos config store with per-repo access tokens and write support.

Changes:
- store.go: Repository gains an optional Token field (per-repo provider access token; the listener ignores it, the installer/worker consume it).
- file.go: token parsed from and serialized to the YAML entry (omitempty); new File.AddOrUpdate validates the input (project and secret required, token optional), upserts by project in place, validates the merged document with the same rules as load (a broken existing file is refused, never silently rewritten), and writes atomically (temp file + rename in the same directory; missing files are created at mode 600, existing files keep their mode). Repositories() now shares a parse helper; its error behavior is unchanged.
- file_test.go: 7 new tests — token parsing (present/absent), add vs in-place update preserving other entries and their tokens, omitempty serialization, file creation when missing (mode 600 asserted), validation failures leaving the file byte-identical, unparsable file refused.
- README.md: documents the optional token field in the watched-repositories section.

Verification: go test ./... green, go test -race ./internal/configstore/ green, go vet and gofmt clean. All 4 acceptance criteria checked with test evidence.

Notes/risks: AddOrUpdate rewrites the file in full, so comments in a hand-maintained config are not preserved (documented on the method). TASK-27.4 can now wire the installer to AddOrUpdate; worker-side token consumption remains a follow-up as scoped.
<!-- SECTION:FINAL_SUMMARY:END -->
