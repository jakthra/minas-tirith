---
id: TASK-27.3
title: 'Scaffold the interactive cmd/install prompt flow (provider, repo, token)'
status: Done
assignee:
  - '@opencode'
created_date: '2026-11-10 12:05'
updated_date: '2026-11-10 20:31'
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
- [x] #1 Running `cmd/install` starts an interactive flow that asks for the git provider and offers Forgejo
- [x] #2 It then asks for the repository as owner/name and validates the format
- [x] #3 It asks for the per-repo access token and does not echo it on a terminal; piped stdin still works
- [x] #4 Prompts read from injectable stdin/stdout, and unit tests drive the full prompt sequence without a TTY
- [x] #5 The `cmd/install` command and flag structure is built with cobra or an equivalent standard CLI framework, with the dependency added to `go.mod`
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add spf13/cobra (cmd/install command tree) and golang.org/x/term (terminal detection + no-echo token read) to go.mod; go mod tidy.
2. New package internal/installer: Options (In/Out/Err, provider list, default provider, IsTerminal func), Run(ctx, Options) (*Answers, error) driving the prompt sequence — provider choice (Forgejo offered), repository owner/name with format validation and re-prompt on invalid input, per-repo access token (no echo when stdin is a terminal via term.ReadPassword, plain line read when piped/not a terminal). Token never written to Out/Err.
3. cmd/install/main.go: cobra root command (persistent flags for input/output paths, -provider/-repo/-token presets for non-interactive use), wires os.Stdin/os.Stdout and runs installer.Run; errors to stderr with a non-zero exit. No provider API calls or config writes (TASK-27.4).
4. Tests: internal/installer covers the full prompt sequence driven by scripted stdin/stdout without a TTY (defaults, invalid repo re-prompt, provider selection, token never echoed, EOF/abort), plus cmd/install command wiring tests (flags parsed, answers printed without the token).
5. README: document cmd/install and its prompt flow; update .env.example note if needed. Run go test ./..., go vet ./..., gofmt.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented:
- internal/installer: Run(ctx, Options) drives the prompt sequence over injectable In/Out streams: provider choice (numbered list, accepts number or name, default preselected), repository owner/name with ValidateRepository and re-prompt on invalid input, and the access token. IsTerminal/ReadPassword are injectable; on a terminal the token is read with echo disabled (golang.org/x/term), piped input reads it as an ordinary line. The token is never written to Out; the confirmation and the cmd/install summary name only provider and repository. Presets (Options.Repository/Token, flags/env) skip their prompts and are validated; end-of-input returns ErrAborted, ctx cancellation is honored between prompts.
- cmd/install: cobra command tree (spf13/cobra v1.10.2) with --provider/--repo/--token flags plus INSTALL_PROVIDER/INSTALL_REPO/INSTALL_TOKEN env fallbacks; SilenceUsage/SilenceErrors, errors printed once to stderr by main with exit 1, 'aborted' on Ctrl-D/Ctrl-C. No provider API calls or config writes (TASK-27.4).
- go.mod: added github.com/spf13/cobra and golang.org/x/term (go mod tidy also promoted the already-required testify to a direct dependency and bumped x/sys to v0.50.0).
- README: new Installer section, binary list, architecture listing.

Verification: go test ./... green; go test -race ./cmd/install ./internal/installer green; go vet and gofmt clean. Manual: piped stdin run of the built binary completes the flow; a PTY run (python pty) confirms the token is not echoed on the terminal and the flow completes.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Scaffolded the interactive installer prompt flow for cmd/install.

Changes:
- internal/installer (new): Run(ctx, Options) collects the three answers over injectable input/output streams — git provider (Forgejo offered, numbered list, default preselected, number or name accepted), repository as owner/name (validated: exactly two segments of letters/digits/./_/-, re-prompted until valid), and the repository's access token. On a terminal the token is read with echo disabled via golang.org/x/term; piped stdin reads it as an ordinary line. The token never reaches the output stream. Preset answers (options, flags, env) skip their prompts; end-of-input returns ErrAborted and cancellation is honored between prompts.
- cmd/install (new): cobra command tree with --provider/--repo/--token flags and INSTALL_PROVIDER/INSTALL_REPO/INSTALL_TOKEN fallbacks, no positional args, errors printed once to stderr with exit 1. It prints the collected provider and repository; webhook creation and watched-config writes remain TASK-27.4.
- go.mod: added spf13/cobra and golang.org/x/term (tidy also promoted testify to a direct dependency and bumped x/sys).
- README: Installer section, binary list, and architecture listing.

Verification: go test ./... green, go test -race on the new packages green, go vet and gofmt clean. Manual runs: piped stdin completes the flow; a PTY run confirms the token is not echoed.

Risks/follow-ups: the first third-party CLI dependency (cobra) is now in go.mod; the installer does not yet create the webhook or write the config (TASK-27.4). A token passed via --token is visible in the process list, documented in the flag help and README.
<!-- SECTION:FINAL_SUMMARY:END -->
