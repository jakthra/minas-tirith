---
id: TASK-9
title: >-
  Create Forgejo provider client for repository access (fetch files, create
  branches, commit, open MRs)
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-27 10:48'
updated_date: '2026-10-27 11:19'
labels: []
dependencies:
  - TASK-5
references:
  - 'TASK-2: Autonomous refinement MR/PR creation agent'
  - >-
    TASK-7: Implement user story detection and ticket generation in
    userStoryReviewWorkflow
  - >-
    TASK-8: Submit MR with refined implementation tickets from
    userStoryReviewWorkflow
  - provider
  - forgejo
modified_files:
  - src/internal/provider/provider.go
  - src/internal/provider/forgejo.go
  - src/internal/provider/fake.go
  - src/internal/provider/forgejo_test.go
  - src/internal/provider/fake_test.go
  - src/internal/config/worker.go
  - src/internal/config/worker_test.go
  - src/README.md
  - groma/systems/minas-tirith/components/provider.md
  - groma/systems/minas-tirith/components/provider-forgejo.md
  - groma/systems/minas-tirith/components/fake.md
  - groma/externals/forgejo.md
type: feature
ordinal: 8000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The autonomous agent workflows (TASK-7, TASK-8) need to interact with the Forgejo/Gitea repository to fetch backlog items, read code, create branches, commit files, and open merge requests. No provider client exists yet in the codebase.

This task creates a provider client package that abstracts Forgejo/Gitea API access behind a clean Go interface. The client will be used by Temporal activities in the agent workflows, keeping provider API calls out of the workflow body (preserving Temporal determinism).

The client must support the operations the agent needs: reading file contents (backlog items, source code), listing files in a directory, creating branches, committing file changes, opening merge requests, and reading/writing MR comments. Authentication uses a provider access token configured via environment/flags.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A `provider` package exists at `src/internal/provider/` with a `Client` interface abstracting Forgejo/Gitea operations
- [x] #2 Client supports fetching file contents by path and ref (branch/SHA) from a repository
- [x] #3 Client supports listing files in a directory path (e.g., all files under `backlog/tasks/`)
- [x] #4 Client supports creating a new branch from an existing ref
- [x] #5 Client supports committing one or more file changes to a branch (create/update files)
- [x] #6 Client supports opening a Merge Request with title, description, source branch, and target branch
- [x] #7 Client supports fetching MR details and comments by MR number
- [x] #8 Client supports adding a comment to an MR
- [x] #9 Client is configured via environment variables (e.g., `FORGEJO_URL`, `FORGEJO_TOKEN`) and/or flags, consistent with existing config layering
- [x] #10 Unit tests cover all client operations using a mock HTTP server (httptest) or interface-based fakes
- [x] #11 The client interface is mockable so Temporal activity tests can substitute fakes without real API calls
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. src/internal/provider/provider.go: Client interface (GetFile/ListFiles/CreateBranch/Commit/CreateMergeRequest/GetMergeRequest/ListMergeRequestComments/AddMergeRequestComment) + provider-agnostic domain types (File, FileInfo, Branch, FileChange, CommitResult, MergeRequest, Comment) + sentinel errors (ErrNotFound, *APIError).
2. src/internal/provider/forgejo.go: ForgejoClient implementing Client against the Gitea-compatible Forgejo API (token auth, JSON plumbing, status->error mapping; file reads via /contents base64; recursive directory listing; branch create; multi-file single commit via git trees->commits->update ref chain; MRs + comments via /pulls and /issues endpoints).
3. src/internal/provider/fake.go: in-memory Fake implementing Client so Temporal activity tests substitute fakes without real API calls (AC11).
4. src/internal/provider tests: httptest mock server covering every operation, auth header, base64 encoding, commit chain, 404->ErrNotFound mapping; fake behavior tests (AC10).
5. src/internal/config/worker.go: ProviderConfig{URL,Token} in WorkerConfig with FORGEJO_URL/-forgejo-url (default http://localhost:3000, local dev instance) + FORGEJO_TOKEN/-forgejo-token, layered flags>env>defaults like the rest; config tests (AC9).
6. README: config table + architecture tree entries for the provider package.
7. Live verification of every write op (branch, commit, MR, comment) against the local Forgejo 16 dev instance (read shapes already verified live); requires an API token.
8. groma: add provider component + worker->forgejo API relationship via groma CLI; record modified files/refs on the task as files change.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented src/internal/provider: Client interface (GetFile/ListFiles/CreateBranch/Commit/CreateMergeRequest/GetMergeRequest/ListMergeRequestComments/AddMergeRequestComment) with provider-agnostic types and ErrNotFound/APIError; ForgejoClient against the Gitea-compatible API whose shapes were verified live (read ops + swagger pinning the exact wire format); Fake in-memory client for activity tests. Config: ProviderConfig (FORGEJO_URL/-forgejo-url default http://localhost:3000, FORGEJO_TOKEN/-forgejo-token) layered in LoadWorker. All unit tests pass incl -race. Key API finding: this Forgejo 16 (gitea-1.22.0 API) exposes multi-file commits via POST /repos/{owner}/{repo}/contents (ChangeFilesOptions) — the git trees/blobs/refs write endpoints do not exist on it, so Commit uses the contents endpoint (one commit with create/update operations, auto-resolving each file's current blob sha).

Live verification against the Forgejo 16 dev instance (root/test) passed all checks with a full-scope token: GetFile on default branch; recursive ListFiles (root and subdirectory); CreateBranch (real head SHA returned); Commit with two files in one commit (new file + update of existing, blob SHA auto-resolved), then an update-only Commit; GetFile via the returned commit SHA as ref; CreateMergeRequest (#1, head/base/author mapped); GetMergeRequest; AddMergeRequestComment; ListMergeRequestComments; ErrNotFound mapping on a missing path. Live run found and fixed one real bug: ListFiles with an empty path (repository root) was rejected by the path validation — root listing is now supported and unit-tested. Cleanup: verification MR closed, its comment deleted, test branch deleted; main back to its initial state (README.md only). Verification tool (src/tmp-verify) removed. Token a163... is still active in Forgejo — revoke via Settings->Applications when done with it.
<!-- SECTION:NOTES:END -->

## Comments

<!-- COMMENTS:BEGIN -->
created: 2026-10-27 11:02

---

Live write verification will use a new Forgejo token (user is generating one via UI); read API shapes already verified live against the dev instance (contents file/dir responses, branches, git refs).

---
<!-- COMMENTS:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Created the provider repository client (src/internal/provider) for the Forgejo/Gitea API, the repository-access layer the autonomous agent workflows (TASK-7/8) will call from Temporal activities.

What changed:
- src/internal/provider: a provider-agnostic Client interface (fetch file by path/ref, recursive directory listing, create branch, commit multiple file changes as one commit, open/fetch merge requests, list/add MR comments) with domain types (File, FileInfo, Branch, FileChange, CommitResult, MergeRequest, Comment) and errors (ErrNotFound, *APIError); ForgejoClient implementing it against the Forgejo REST API v1 (Gitea-compatible, access-token auth); Fake, an in-memory Client substitute for activity tests.
- src/internal/config: ProviderConfig in WorkerConfig — FORGEJO_URL/-forgejo-url (default http://localhost:3000) and FORGEJO_TOKEN/-forgejo-token, layered flags>env>defaults like the rest.
- src/README.md: provider client section, config table rows, architecture tree.
- groma architecture: provider component (described, all three source files owned) + provider->forgejo API relationship; Forgejo external updated to cover the outbound API direction.

Why: the agent workflows need to fetch backlog items and source, create branches, commit generated tickets, and open/discuss MRs; no provider client existed. The interface keeps provider calls in activities (Temporal determinism) and mockable in tests.

Key design finding: this Forgejo 16 (gitea-1.22.0 API) exposes multi-file commits via POST /repos/{owner}/{repo}/contents (ChangeFilesOptions) — the git trees/blobs/refs write endpoints do not exist, so Commit auto-resolves each change (existing blob SHA -> update op, missing -> create op) and submits them as one commit.

Verification: API shapes were pinned against the live instance's swagger and captured responses, then every operation was live-verified against the Forgejo 16 dev instance (root/test): reads on branch and commit-SHA refs, recursive and root listings, branch creation (real head SHA), a two-file commit plus an update-only commit, MR open/fetch, comments add/list, and 404->ErrNotFound mapping. The live run caught one real bug (empty-path root listing was rejected) which was fixed and unit-tested. Cleanup: verification MR closed, comment and test branch deleted; repo back to initial state. Unit tests: all client operations covered via httptest (auth header, base64, request/response mapping, error paths, path safety) plus Fake behavior tests; go test ./... -race green; go vet/gofmt clean.

Risks/follow-ups: the provider config is not yet consumed by cmd/worker — TASK-7/8 construct the client (FORGEJO_TOKEN will be required at worker startup once wired); the temporary verification token (full scopes) is still active in Forgejo — revoke via Settings->Applications; a gitea provider client would be the same class with the same wire format, so no extra work is anticipated there.
<!-- SECTION:FINAL_SUMMARY:END -->
