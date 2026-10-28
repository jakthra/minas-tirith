---
id: TASK-12
title: Add declarative config store for watched repos and secrets
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-27 12:04'
updated_date: '2026-10-27 12:18'
labels: []
dependencies: []
references:
  - src/internal/config/config.go
  - src/internal/source/webhook/webhook.go
  - src/cmd/worker/main.go
  - src/internal/configstore/store.go
  - config-store
modified_files:
  - src/internal/configstore/store.go
  - src/internal/configstore/file.go
  - src/internal/configstore/file_test.go
  - src/internal/source/webhook/webhook.go
  - src/internal/source/webhook/webhook_test.go
  - src/internal/config/config.go
  - src/internal/config/config_test.go
  - src/internal/config/worker_test.go
  - src/internal/server/server.go
  - src/internal/server/server_test.go
  - src/cmd/worker/main.go
  - src/README.md
  - src/go.mod
  - src/go.sum
  - groma/systems/minas-tirith/components/config-store.md
  - groma/systems/minas-tirith/components/config.md
  - groma/systems/minas-tirith/components/webhook.md
  - groma/systems/minas-tirith/components/server.md
ordinal: 11000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The listener and worker currently require webhook secrets and watched repository lists via `-secret`/`-watched` CLI flags or `WEBHOOK_SECRET`/`WATCHED_REPOS` environment variables. This makes configuration imperative and difficult to manage in containerized/Kubernetes deployments where declarative configuration (ConfigMaps, CRDs) is the norm.\n\nThis ticket introduces a config store abstraction that loads watched repositories and their associated webhook secrets from a declarative source, removing the need for CLI flags and environment variables for these values. The declarative store must support multiple repositories being watched in the same store, each with its own webhook secret. The abstraction should be designed so a Kubernetes operator/CRD can interface with it later, but the Kubernetes interface itself is out of scope.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A config store abstraction exists that loads multiple watched repositories and their webhook secrets from a single declarative source
- [x] #2 Each watched repository in the store can have its own webhook secret (per-repo secrets, not a single shared secret)
- [x] #3 The `-secret` and `-watched` CLI flags are removed from both listener and worker binaries
- [x] #4 The `WEBHOOK_SECRET` and `WATCHED_REPOS` environment variables are removed
- [x] #5 The webhook source loads secrets and watched repos from the config store instead of direct config
- [x] #6 All existing tests pass and new tests cover the config store abstraction with multiple repos
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. New package internal/configstore: Repository{Project, Secret}, Store interface (Repositories() ([]Repository, error)), File store (declarative YAML config file, validated: non-empty project and secret, no duplicate projects), and Static store (in-memory slice — the seam a future Kubernetes CRD/operator implements, and the test seam).
2. Webhook source: webhook.Config drops Secret/Watched, gains Store; Start loads the watched repos and their per-repo secrets from the store (validating them); the delivery handler verifies each delivery with the routed project's own secret.
3. internal/config: remove -secret/-watched flags and WEBHOOK_SECRET/WATCHED_REPOS env vars; add -watched-config flag / WATCHED_CONFIG env locating the declarative file (required); Config.Store carries the store; gopkg.in/yaml.v3 becomes a direct dependency.
4. Server wiring: pass cfg.Store into the webhook source; watched projects logged by the webhook source at Start; worker main drops its watched-repos log line.
5. Tests: rewrite affected config/worker/webhook/server tests; new configstore tests (multi-repo loading, validation errors, JSON compatibility); webhook tests for per-repo secrets (delivery signed with another repo's secret is rejected 401).
6. Docs + architecture: README (env table, config file format, Forgejo wiring section), groma architecture (new configstore component, refreshed config/webhook/server descriptions).
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Added internal/configstore: Store interface (Repositories), Repository declaration (Project+Secret, per-repo secret), File store (validated YAML/JSON config file) and Static store (in-memory seam for tests and future K8s operator).

Webhook source now takes a configstore.Store in its Config (Secret/Watched removed); Start loads watched repos + per-repo secrets from the store (validating declarations, reporting every problem) and the delivery handler verifies with the addressed project's own secret.

Server passes cfg.Store into the webhook source; worker/listener startup logs adjusted (watched projects logged by the webhook source at Start). Verified live: -secret/-watched rejected as unknown flags; two-repo YAML config loaded; delivery authenticated only by the addressed repo's own secret (401 with another repo's secret, 200 with its own).

Architecture curated via groma: configstore files combined into a Config store component (config-store), described it, refreshed the stale config/webhook/server records, and authored the webhook->config-store and config->config-store relationships. Rescan keeps the curation.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added the declarative config store for watched repositories and per-repo webhook secrets, replacing the -secret/-watched flags and WEBHOOK_SECRET/WATCHED_REPOS env vars.

What changed:
- New internal/configstore: Store interface (Repositories() ([]Repository, error)) with Repository{Project, Secret} — the seam a future Kubernetes operator/CRD store implements; File (declarative YAML config file, JSON parses too, every declaration validated and all problems reported in one error); Static (in-memory, for wiring/tests).
- Webhook source (AC5): webhook.Config now carries the store instead of Secret/Watched; Start loads the watched repos and their per-repo secrets (AC1/AC2) and each delivery is verified with the addressed project's own secret.
- Config (AC3/AC4): -secret/-watched flags and WEBHOOK_SECRET/WATCHED_REPOS removed from both binaries; new required -watched-config/WATCHED_CONFIG locates the declarative file; Load builds the File store and carries it on Config.Store. gopkg.in/yaml.v3 became a direct dependency.
- README documents the file format, and the groma architecture gained a curated config-store component with refreshed config/webhook/server records.

Verification: go build/vet/gofmt clean; go test -race -count=1 ./... all 10 packages pass (new tests: multi-repo file loading, per-repo secrets incl. 401 for another repo's secret, store validation, config flag/env layering). Live: both binaries reject -secret/-watched as unknown flags; a two-repo YAML config loaded (log: projects=[BeanieGenie Garden]); delivery to Garden signed with BeanieGenie's secret -> 401, with Garden's own -> 200 dispatched.

Risks/follow-ups: the store loads once at startup (no hot reload — a ConfigMap change needs a restart until a watch/reload mechanism is added); the Kubernetes operator/CRD store remains future work, as scoped out.
<!-- SECTION:FINAL_SUMMARY:END -->
