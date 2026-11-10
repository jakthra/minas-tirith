---
id: TASK-27
title: Add interactive installer CLI that onboards a Forgejo repository
status: To Do
assignee: []
created_date: '2026-11-10 12:03'
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
- [ ] #1 Running the installer binary starts an interactive flow that asks for the git provider and offers Forgejo
- [ ] #2 It then asks for the repository (owner/name) and a per-repository access token without echoing the token to the terminal
- [ ] #3 On confirmation it creates the webhook on the selected Forgejo repository via the provider API, targeting the listener webhook endpoint
- [ ] #4 It writes the repository and its per-repo secret/token into the watched-repositories config store so the listener watches it
- [ ] #5 Unit tests cover the interactive prompts, the webhook-creation call against a mocked provider API, and the config writing
<!-- AC:END -->
