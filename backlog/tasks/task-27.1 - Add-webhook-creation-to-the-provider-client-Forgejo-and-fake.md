---
id: TASK-27.1
title: Add webhook creation to the provider client (Forgejo and fake)
status: Done
assignee:
  - '@jakobt'
created_date: '2026-11-10 12:05'
updated_date: '2026-11-10 15:27'
labels: []
dependencies: []
references:
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/internal/provider/fake.go
modified_files:
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/internal/provider/fake.go
  - src/internal/provider/forgejo_test.go
  - src/internal/provider/fake_test.go
  - src/README.md
parent_task_id: TASK-27
type: feature
ordinal: 27000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
**Context:** TASK-27 (interactive installer) must register the webhook on the Forgejo repository through the API, but `provider.Client` today covers only repository content and merge-request operations — there is no webhook operation.

**What needs to happen:** Add a webhook-creation operation to the `provider.Client` interface (repository, target URL, secret, events), implement it in `ForgejoClient` via the Forgejo hooks API following the existing `do`/`APIError` patterns, and implement it in `Fake` so installer and activity tests never make real API calls.

**Related information:** This is the provider seam the installer (TASK-27) uses; webhook signature verification on the listener side already exists in `src/internal/source/webhook/forgejo.go` and is unchanged.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 `provider.Client` gains a documented webhook-creation operation (repo, target URL, secret, events)
- [x] #2 `ForgejoClient` implements it against the Forgejo repo-hooks API endpoint, surfacing non-2xx answers as `APIError`
- [x] #3 `Fake` implements the same operation, recording calls for tests
- [x] #4 Unit tests with httptest cover the success path and a non-2xx provider error
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add a Webhook type (ID, URL, Events, Active) and a documented CreateWebhook(ctx, repo, targetURL, secret, events) operation to the provider.Client interface (provider.go), next to the repository-write operations.
2. Implement ForgejoClient.CreateWebhook (forgejo.go): POST repos/{owner}/{repo}/hooks with type "gitea" (the type both Forgejo and Gitea accept, as in the README wiring example), active, content_type json, config {url, content_type, secret}, and the given events; parse the hook response into Webhook; validate targetURL and events before the call; reuse the existing do/responseError plumbing so non-2xx answers surface as APIError (404 as ErrNotFound).
3. Implement Fake.CreateWebhook (fake.go): validate like the real client, store the webhook per repo with a generated ID, and record every call with full arguments (WebhookCall + WebhookCalls(), mirroring llm.Fake's Calls pattern) so installer tests can assert the repo, URL, secret, and events.
4. Unit tests: forgejo_test.go covers the success path (method, path, auth, JSON body, response mapping) and a non-2xx APIError; fake_test.go covers call recording, returned webhook, missing repo, and the injected-Err path (TestFakeErr).
5. go test ./..., go vet ./..., gofmt.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented CreateWebhook across the provider seam: Webhook type + interface method (provider.go), ForgejoClient POST repos/{owner}/{repo}/hooks with type gitea / active / json content type / config{url,content_type,secret} / events, response parsed to Webhook (forgejo.go), Fake with per-repo storage, generated ids, and full-argument call recording via WebhookCall/WebhookCalls + Webhooks getter (fake.go). Tests: TestForgejoClientCreateWebhook (success path incl. method/path/auth/body, 422 APIError, 404 ErrNotFound, input validation) and TestFakeCreateWebhook (call recording, storage, fresh ids, copy safety, missing repo, validation), plus TestFakeErr coverage. go test -race ./... green, go vet clean, gofmt clean.

Evidence: AC1 — CreateWebhook(ctx, repo, targetURL, secret, events) documented on provider.Client (provider.go); the compile-time var _ Client = (*Fake)(nil) check forces both implementations. AC2 — ForgejoClient.CreateWebhook POSTs repos/{owner}/{repo}/hooks through the existing do/responseError plumbing; TestForgejoClientCreateWebhook asserts the request (method, path, auth, JSON body with type gitea/active/json content type/config url+secret/events) and the 422 -> *APIError mapping, plus 404 -> ErrNotFound. AC3 — Fake.CreateWebhook records full-argument calls (WebhookCall via WebhookCalls()) and stores per-repo webhooks (Webhooks); TestFakeCreateWebhook verifies. AC4 — both paths run against the newTestForgejo httptest server and pass with go test -race. Also updated the Client doc comment and the README provider-client operation list to mention webhook creation.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added webhook creation to the provider seam. provider.Client gained a documented CreateWebhook(ctx, repo, targetURL, secret, events) operation returning a new Webhook value (ID, URL, Events, Active). ForgejoClient implements it by POSTing to the Forgejo repo-hooks endpoint (repos/{owner}/{repo}/hooks) with type "gitea" (accepted by Forgejo and Gitea alike, matching the README wiring example), active, json content type, and the config url/secret plus the requested events, reusing the existing do/responseError plumbing so non-2xx answers surface as *APIError (404 as ErrNotFound); it validates the target URL and events before any API call. Fake implements the same operation with per-repo storage and generated ids, and records every call with full arguments (WebhookCall via WebhookCalls(), mirroring llm.Fake's Calls pattern) so installer tests can assert the repo, URL, secret, and events; Webhooks(repo) returns the created hooks. Tests: TestForgejoClientCreateWebhook (httptest success path incl. request method/path/auth/body, 422 APIError, 404 ErrNotFound, input validation) and TestFakeCreateWebhook (call recording, storage, fresh ids, copy safety, missing repo, validation), plus TestFakeErr coverage. go test -race ./... green, go vet clean, gofmt clean. Doc comments and the README provider-client list updated. Risks/follow-ups: the hook type and JSON content type are client-side choices (documented); TASK-27.4 wires this seam into the installer.
<!-- SECTION:FINAL_SUMMARY:END -->
