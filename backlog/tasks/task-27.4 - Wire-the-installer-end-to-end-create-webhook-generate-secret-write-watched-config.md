---
id: TASK-27.4
title: >-
  Wire the installer end-to-end: create webhook, generate secret, write watched
  config
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-10 12:05'
updated_date: '2026-11-10 20:35'
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
- [x] #1 Running the installer end-to-end (Fake provider + temp config in tests) creates the webhook on the chosen repo pointing at the configured listener URL with a freshly generated secret
- [x] #2 It writes the repo entry (project, secret, per-repo token) into the watched config file so the listener watches the repository
- [x] #3 On success it prints a summary naming the repository, webhook URL, and config file path
- [x] #4 A failed webhook creation leaves no config entry (and a failed config write is reported), covered by unit tests
- [x] #5 Unit tests cover the full flow with `Fake` and a temp config; `go test ./...` passes
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. internal/config: add LoadInstall(args) (*InstallConfig, error) with the same layering as the listener/worker (flag > shell env > .env > default) and a shared dotenv helper for the installer: Forgejo URL (FORGEJO_URL, default http://localhost:3000), listener base URL (INSTALL_LISTENER_URL, default http://localhost:9797), watched config path (WATCHED_CONFIG, default watched.yaml), webhook events (INSTALL_WEBHOOK_EVENTS, default push/issue_comment/pull_request_comment/pull_request_review_*). Validate URL formats.
2. internal/installer: add Onboard(ctx, Options) running the existing prompt flow then: generate a fresh random webhook secret (crypto/rand hex), create the webhook on the provider via provider.Client.CreateWebhook targeting <listener>/v1/{project}/webhooks/{provider}, then write configstore.File.AddOrUpdate(project, secret, token). Order webhook first so a failed creation leaves no config entry; a failed config write is reported loudly and names the orphaned webhook. Injectable Provider factory + Secret generator for tests. Print summary naming repository, webhook URL, config path (never the token/secret).
3. cmd/install: wire the real ForgejoClient factory, config.LoadInstall, and Onboard; new flags -forgejo-url/-listener-url/-watched-config/-webhook-events.
4. Tests: installer Onboard with provider.Fake + temp config (webhook created with generated secret + correct URL/events, entry written, summary), failed webhook leaves no entry, failed config write reported, secret randomness; cmd/install end-to-end over Fake via injected factory; config LoadInstall layering tests.
5. README: document the end-to-end installer, its flags/env, and what it writes. go test ./..., vet, gofmt.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented:
- internal/config/install.go: InstallFlags (forgejo-url, listener-url, watched-config, webhook-events) + LoadInstall(flags) with the shared layering (flag > shell env > .env > default), URL validation (absolute http/https), comma-separated event parsing, and DefaultWebhookEvents = the README's agent event set (push, issue_comment, pull_request_comment, pull_request_review_comment/_approved/_rejected). DefaultListenerURL http://localhost:9797.
- internal/installer/onboard.go: Onboard(ctx, OnboardOptions) runs the TASK-27.3 prompt flow, then generates a 32-byte hex webhook secret (crypto/rand), creates the webhook via provider.Client.CreateWebhook at {listener}/v1/{project}/webhooks/{provider} (project = repository name), then writes configstore.File.AddOrUpdate(project, secret, token). Webhook first: a failed creation writes nothing; a failed config write reports the orphaned webhook id/url. Injectable NewProvider factory and GenerateSecret. Project/WebhookURL helpers. WriteSummary names repository, webhook URL, config path only.
- cmd/install: wired config.LoadInstall + Onboard with the real Forgejo client factory; new flags -forgejo-url/-listener-url/-watched-config/-webhook-events; newRootCmdWithProvider seam for tests.

Verification: go test ./... green, -race on cmd/install, internal/installer, internal/config green, vet/gofmt clean. Manual: built binary against a stub Forgejo API — POST /api/v1/repos/acme/widgets/hooks with token auth, gitea/json/active, generated secret in config matching the request, summary printed, exit 0; 422 stub -> loud error naming 'no config entry written', exit 1, no config file created.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Wired the installer end-to-end: cmd/install now onboards a repository instead of only collecting answers.

Changes:
- internal/installer/onboard.go: Onboard runs the prompt flow, generates a fresh webhook secret (32 random bytes, hex), creates the webhook on the provider at {listener}/v1/{project}/webhooks/{provider} via provider.Client.CreateWebhook, then writes the entry (project, secret, per-repo token) with configstore.File.AddOrUpdate. The webhook is created first, so a failed creation leaves no config entry; a failed config write is reported together with the orphaned webhook. Provider factory and secret generator are injectable; WriteSummary prints repository, webhook URL, and config path, never the token or secret.
- internal/config/install.go: LoadInstall with the shared flag > env > .env > default layering for forgejo-url (FORGEJO_URL), listener-url (INSTALL_LISTENER_URL, default http://localhost:9797), watched-config (WATCHED_CONFIG) and webhook-events (INSTALL_WEBHOOK_EVENTS, defaulting to the agent event set), with URL validation.
- cmd/install: wires the real config loader and Forgejo client; new flags --forgejo-url, --listener-url, --watched-config, --webhook-events.
- README: the Installer section now documents the two onboarding steps, the failure ordering, and the flag/env table.

Verification: go test ./... green; -race on cmd/install, internal/installer, internal/config green; vet and gofmt clean. Tests cover the full flow over provider.Fake with a temp config (webhook URL/events/generated secret, config entry, summary without secrets), preservation of existing entries, failed webhook leaving no config file, failed config write reporting the created webhook, provider-factory and secret-generation failures, abort before side effects, and the config layering. Manual run of the built binary against a stub Forgejo API confirmed the authenticated hooks POST and the written config; a 422 stub produced a loud error and no config file.

Risks/follow-ups: the listener loads watched repositories at startup, so it must be restarted after onboarding (stated in the summary output and README). The installer does not update an existing webhook, so re-running it creates a second one on the repository. Worker-side consumption of the per-repo token (replacing FORGEJO_TOKEN) remains the follow-up noted in TASK-27.2.
<!-- SECTION:FINAL_SUMMARY:END -->
