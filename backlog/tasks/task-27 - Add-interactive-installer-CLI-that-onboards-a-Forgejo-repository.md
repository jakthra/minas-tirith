---
id: TASK-27
title: Add interactive installer CLI that onboards a Forgejo repository
status: Done
assignee: []
created_date: '2026-11-10 12:03'
updated_date: '2026-11-10 20:37'
labels: []
dependencies: []
references:
  - src/README.md
  - src/watched.yaml
type: feature
ordinal: 26000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:** Today connecting a repository to Minas Tirith is manual: hand-editing `watched.yaml` (repository + webhook secret), filling in `.env` (`FORGEJO_TOKEN` etc.), and creating the webhook in the Forgejo UI (see src/README.md). A single installer binary should perform that onboarding interactively instead.

**What needs to happen:** Add a new binary (e.g. `cmd/install`) that runs interactively and asks for the git provider (Forgejo today) and the repository. Token model decision: per-project — each watched repository gets its own access token/webhook secret, entered during install and stored per repository entry. On confirmation it creates the webhook on the selected Forgejo repository via the provider API (pointing at the listener webhook endpoint) and writes/updates the watched-repositories config entry so the listener picks the repository up.

**Related information:** Out of scope: git providers other than Forgejo, and deployment/hosting of listener, worker, and Temporal. Reuses the declarative watched-repos config store (`src/internal/configstore`, per-repo secrets) and the existing Forgejo webhook/provider client patterns. Done means the binary plus unit tests for the prompts and config writing — no live demo against the dev Forgejo required.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Running the installer binary starts an interactive flow that asks for the git provider and offers Forgejo
- [x] #2 It then asks for the repository (owner/name) and a per-repository access token without echoing the token to the terminal
- [x] #3 On confirmation it creates the webhook on the selected Forgejo repository via the provider API, targeting the listener webhook endpoint
- [x] #4 It writes the repository and its per-repo secret/token into the watched-repositories config store so the listener watches it
- [x] #5 Unit tests cover the interactive prompts, the webhook-creation call against a mocked provider API, and the config writing
<!-- AC:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Verified across the four subtasks (all Done):
- AC1: cmd/install runs the interactive flow and offers Forgejo (installer.Providers = [forgejo], preselected; TestRunProviderSelection, TestInstallOnboardsEndToEnd).
- AC2: repository asked as owner/name with validation/re-prompt (TestRunRepositoryValidation); token read with echo disabled on a terminal (PTY smoke run confirmed no echo) and as a plain line when piped (TestRunTokenOnTerminal).
- AC3: webhook creation goes through provider.Client.CreateWebhook to {listener}/v1/{project}/webhooks/{provider}; ForgejoClient tested against httptest (TestForgejoClientCreateWebhook) and the installer against provider.Fake (TestOnboardFullFlow). Live dev run via the new 'just install-dev' created hook id 2 on root/BeanieGenie at http://169.254.1.2:9797/v1/BeanieGenie/webhooks/forgejo (test artifact deleted afterwards, watched.yaml restored).
- AC4: configstore.File.AddOrUpdate writes project/secret/token; TestOnboardFullFlow reloads the temp file and asserts the entry, TestOnboardPreservesOtherEntries checks other entries survive.
- AC5: 55 tests across the installer, provider, configstore and cmd/install packages cover prompts, webhook creation (mocked provider API), and config writing; go test ./... green.
Also added the 'just install-dev' recipe (onboards root/BeanieGenie into the dev listener) as the easy dev test.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Delivered the interactive installer that onboards a Forgejo repository, in four subtasks:

- TASK-27.1: provider.Client.CreateWebhook (Forgejo hooks API + Fake with call recording).
- TASK-27.2: per-repo token in the watched-repos config store plus File.AddOrUpdate (atomic write, other entries preserved).
- TASK-27.3: cmd/install (cobra) and internal/installer prompt flow over injectable streams — provider, repository owner/name with validation, access token hidden on a terminal.
- TASK-27.4: end-to-end wiring — generated webhook secret, webhook created at {listener}/v1/{project}/webhooks/{provider}, then the config entry written; webhook first so a failed creation leaves no config entry.

Developer convenience: 'just install-dev' onboards root/BeanieGenie into the dev listener (uses FORGEJO_URL/FORGEJO_TOKEN from src/.env, targets http://169.254.1.2:9797).

Verification: go test ./... green (55 tests across installer/provider/configstore/cmd/install), vet and gofmt clean. A live run of 'just install-dev' against the dev Forgejo and listener created the webhook and wrote the config entry; the test webhook was deleted and src/watched.yaml restored afterwards.

Risks/follow-ups: the listener must be restarted after onboarding; re-running the installer creates an additional webhook rather than updating the existing one; worker-side use of the per-repo token instead of FORGEJO_TOKEN is still open (noted in TASK-27.2).
<!-- SECTION:FINAL_SUMMARY:END -->
