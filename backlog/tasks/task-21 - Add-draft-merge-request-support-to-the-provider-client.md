---
id: TASK-21
title: Add draft merge request support to the provider client
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-02 13:44'
updated_date: '2026-11-02 13:46'
labels: []
milestone: m-0
dependencies:
  - TASK-3
references:
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/internal/provider/fake.go
  - src/internal/provider/forgejo_test.go
  - src/internal/worker/userstoryreview/activities.go
modified_files:
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/internal/provider/fake.go
  - src/internal/worker/userstoryreview/activities.go
  - src/internal/provider/forgejo_test.go
  - src/internal/provider/fake_test.go
  - src/internal/worker/userstoryreview/userstoryreview_test.go
  - src/README.md
  - src/internal/worker/userstoryreview/mrcomment_test.go
type: feature
ordinal: 20000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
TASK-3 AC2 requires the implementation agent to open a *draft* MR when it cannot fulfill a ticket, so a human can review an incomplete attempt instead of the agent silently failing. The provider client (`src/internal/provider`) cannot express this today: `CreateMergeRequest(ctx, repo, title, description, head, base)` has no draft flag, the Forgejo request body (`createPullRequestRequest`) does not send one, and `MergeRequest` carries no draft state to read back. The fake has the same gap, so no agent code can be written or tested against draft MRs.

This ticket closes that gap in the shared provider layer, independent of any agent. The refinement agent (TASK-2) does not use drafts and must keep opening regular MRs.

**Related information:**
- `src/internal/provider/provider.go` — the `Client` interface, `MergeRequest` and `CreateMergeRequest`; this is the contract both agents depend on.
- `src/internal/provider/forgejo.go` — `CreateMergeRequest`, `createPullRequestRequest`, `pullRequestResponse` and `mergeRequestFrom`; Forgejo already returns a draft flag on pull requests, so reading it back is a response-mapping change.
- `src/internal/provider/fake.go` — `CreateMergeRequest` and `GetMergeRequest`; the fake must model draft state for activity tests.
- `src/internal/provider/forgejo_test.go` — `TestForgejoClientMergeRequests` is the existing MR test pattern to extend.
- `src/internal/worker/userstoryreview/activities.go` — the only current `CreateMergeRequest` caller; it must keep compiling and keep opening non-draft MRs.
- TASK-9 — delivered the Forgejo provider client this extends.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The `provider.Client` interface can create a merge request as a draft or as a regular MR, and the choice is explicit at every call site
- [x] #2 The Forgejo client sends the draft flag to the pull request API and maps the returned draft state onto `MergeRequest`
- [x] #3 `MergeRequest` exposes whether the MR is a draft, both for a created MR and for `GetMergeRequest`
- [x] #4 The in-memory `Fake` records draft state and returns it from `CreateMergeRequest` and `GetMergeRequest`, so activity tests can assert on it
- [x] #5 The user story review agent continues to open non-draft MRs unchanged
- [x] #6 Tests cover creating a draft MR, creating a regular MR, and reading draft state back through the Forgejo client and the Fake
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. provider.go: add `Draft bool` to `MergeRequest`; change the interface method to `CreateMergeRequest(ctx, repo, title, description, head, base string, draft bool)` so every call site states its choice explicitly, and document the parameter.
2. forgejo.go: add `Draft bool `json:"draft"`` to `createPullRequestRequest` and `pullRequestResponse`; send the argument and map the returned flag in `mergeRequestFrom`, so both create and get expose draft state.
3. fake.go: store the draft flag on the recorded `MergeRequest` so `CreateMergeRequest` and `GetMergeRequest` both return it.
4. userstoryreview/activities.go: pass `false` in `OpenMergeRequest` (refinement MRs stay regular); update the `failMRClient` test stub signature.
5. Tests: extend `forgejo_test.go` (regular create sends draft=false, draft create sends draft=true and maps it back, get maps draft) and `fake_test.go` (draft and regular MRs round-trip through create/get; error-injection call updated).
6. Update the `CreateMergeRequest` note in src/README.md; run `go build ./...`, `go vet ./...` and `go test -race ./...` in src/.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented draft MR support in the provider layer.

- `provider.MergeRequest` gained `Draft bool`; `Client.CreateMergeRequest` now takes a trailing `draft bool`, so the choice is explicit at every call site (no defaulting).
- Forgejo: `createPullRequestRequest` sends `"draft"` and `pullRequestResponse` reads it; `mergeRequestFrom` maps it, so both create and get report draft state. Verified against the Gitea/Forgejo API structs (`CreatePullRequestOption.Draft`, `PullRequest.Draft`, `Issue.IsWorkInProgress`, all `json:"draft"`) and the existing webhook fixture.
- Fake: records `draft` on the stored `MergeRequest`, returned by both `CreateMergeRequest` and `GetMergeRequest`.
- userstoryreview `OpenMergeRequest` passes `false` (refinement MRs stay regular); test stubs updated.
- Tests: Forgejo `create`, `create draft`, `get`, `get draft`; Fake `draft merge request` subtest plus regular-MR non-draft checks; refinement activity and end-to-end workflow tests assert the MR is not a draft.
- README updated.

Verification: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test -race ./...` all pass.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
The provider layer can now open draft merge requests and report draft state, unblocking TASK-24.

**Changes**
- `src/internal/provider/provider.go`: `MergeRequest.Draft bool`; `Client.CreateMergeRequest` takes a trailing `draft bool` so no call site can omit the choice.
- `src/internal/provider/forgejo.go`: sends `"draft"` in the pull request body and maps the returned `draft` onto `MergeRequest` (create and get).
- `src/internal/provider/fake.go`: records draft state, returned by `CreateMergeRequest` and `GetMergeRequest`.
- `src/internal/worker/userstoryreview/activities.go`: refinement passes `false`, keeping refinement MRs regular.
- Tests: Forgejo `create`/`create draft`/`get`/`get draft`; Fake draft round-trip alongside the regular MR; refinement activity and end-to-end workflow assert non-draft. `src/README.md` updated.

**Verification**: `gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test -race ./...` all pass in `src/`.

**Risk/notes**: the interface signature changed, so any future `Client` implementation or test stub must add the `draft` argument; the compiler enforces it. No live Forgejo call was made for the draft flag — API shapes were confirmed against the Gitea/Forgejo structs and the existing webhook fixture.
<!-- SECTION:FINAL_SUMMARY:END -->
