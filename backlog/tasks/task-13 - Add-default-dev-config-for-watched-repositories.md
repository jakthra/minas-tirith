---
id: TASK-13
title: Add default dev config for watched repositories
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-27 12:29'
updated_date: '2026-10-27 12:30'
labels: []
dependencies: []
references:
  - src/internal/config/config.go
  - src/README.md
modified_files:
  - src/watched.yaml
  - src/internal/config/config.go
  - src/internal/config/config_test.go
  - src/internal/config/worker_test.go
  - src/README.md
  - groma/systems/minas-tirith/components/config.md
ordinal: 12000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
After TASK-12, both binaries refuse to start unless WATCHED_CONFIG or -watched-config points at the declarative watched-repos file. Local development (running from src/ against the local Forgejo instance, forgejo/docker-compose.yaml) has no out-of-the-box config, so every bare go run fails. Ship a committed dev config file and a default-path fallback: when the flag and env var are unset, the listener and worker load watched.yaml from the working directory, so they start bare for local development. The flag and env var keep precedence; a missing default still fails fast with a clear message.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A default dev config file is committed, watching the local dev Forgejo's BeanieGenie with the dev webhook secret used in the README wiring examples
- [x] #2 When -watched-config and WATCHED_CONFIG are unset, both binaries load watched.yaml from the working directory when it exists
- [x] #3 -watched-config and WATCHED_CONFIG still take precedence over the default file
- [x] #4 When nothing is set and no default file exists, startup fails with a clear error naming the flag, the env var, and the default path
- [x] #5 Tests cover the default fallback, precedence, and the missing-default error; the README documents the default
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Commit src/watched.yaml as the dev config: BeanieGenie with the README's dev secret s3cret, commented for local development against forgejo/docker-compose.yaml.
2. internal/config: DefaultWatchedConfig constant (watched.yaml); when flag and env are unset, fall back to it if the file exists; otherwise keep the required-config error, now naming flag, env, and default path.
3. Tests in internal/config: fallback loads watched.yaml from the working directory (t.Chdir), env/flag still win over the default, missing default produces the updated error; update the two exact-message assertions (config_test, worker_test).
4. README: default column for WATCHED_CONFIG, bare go run example, watched-repositories section documents the shipped dev config and its limits.
5. Verify live from src/: bare listener run picks up src/watched.yaml; missing default errors clearly. Refresh the groma config component description.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Committed src/watched.yaml (BeanieGenie + dev secret s3cret, commented for the local Forgejo dev instance) and added the DefaultWatchedConfig fallback in config resolution: -watched-config > WATCHED_CONFIG > watched.yaml in the working directory when it exists; otherwise the required-config error now names the flag, env, and default path. Verified live: bare listener run from src/ loads the committed default and accepts a delivery signed with the dev secret; from an empty directory it exits 2 with 'watched config file is required: set -watched-config or WATCHED_CONFIG, or create watched.yaml in the working directory'.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added a default dev config so both binaries start bare from src/.

What changed:
- src/watched.yaml: committed dev config — BeanieGenie with the dev webhook secret (s3cret) used in the README wiring examples, commented for the local Forgejo dev instance.
- internal/config: DefaultWatchedConfig (watched.yaml) fallback — resolution is now -watched-config > WATCHED_CONFIG > watched.yaml in the working directory (when it exists); otherwise the required-config error names the flag, env var, and default path. Flag help text updated.
- Tests: fallback loading from the working directory (t.Chdir), env-over-default precedence, hermetic missing-default error in both listener and worker tests.
- README: WATCHED_CONFIG table row gains the default, example is now a bare go run, watched-repositories section documents the shipped dev config and its limits. Groma config component record refreshed.

Verification: go build/vet/gofmt clean; go test -race -count=1 passes across config, configstore, server, webhook (full suite green). Live from src/: bare listener run loads the committed default and accepts a delivery signed with the dev secret (200, dispatched); bare worker run passes config and proceeds to the Temporal dial (expected failure without a server); from an empty directory the listener exits 2 with 'watched config file is required: set -watched-config or WATCHED_CONFIG, or create watched.yaml in the working directory'.

Risks/follow-ups: the default is a development convenience with a known dev secret — deployments must point WATCHED_CONFIG at their own file (documented); the fallback resolves relative to the working directory, so the binaries must run from src/ to find the committed file (documented in the table row).
<!-- SECTION:FINAL_SUMMARY:END -->
