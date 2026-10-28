---
id: TASK-16
title: Add .env file support for development configuration
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-28 15:09'
updated_date: '2026-10-28 15:19'
labels: []
dependencies: []
references:
  - config
modified_files:
  - src/internal/config/dotenv.go
  - src/internal/config/config.go
  - src/internal/config/worker.go
  - src/.env.example
  - .gitignore
  - src/internal/config/dotenv_test.go
  - src/README.md
  - justfile
ordinal: 15000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Developers currently have to export environment variables (FORGEJO_TOKEN, LLM_API_KEY, and friends) in every shell session or pass them as flags every time they run the listener or worker. There is no standard place to keep local development configuration, so each developer reconstructs the same setup by hand and risks committing secrets. A .env file gives every developer one place to put their local values: a committed .env.example template documents every supported variable, and a gitignored .env holds the actual local secrets and overrides. Both binaries load it at startup so running them directly or through just works the same way.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A .env.example template is committed (in src/ next to go.mod) listing every supported environment variable with example values and comments explaining each
- [x] #2 .env is added to .gitignore so local secrets are never committed
- [x] #3 Both cmd/listener and cmd/worker load .env from the working directory at startup when the file exists
- [x] #4 Environment variables already set in the shell take precedence over .env file values (the file fills gaps, it does not override the shell)
- [x] #5 Command-line flags still take precedence over both environment variables and .env values
- [x] #6 The justfile's run-listener and run-worker recipes work with the .env file without extra shell setup (e.g. via just's dotenv-load or by sourcing the file)
- [x] #7 A missing .env file is not an error: the binaries still run with defaults, environment variables, and flags as before
- [x] #8 The README documents the .env workflow: copy .env.example to .env, fill in values, run with just or go run
- [x] #9 Tests cover the .env loading: values are read, shell environment wins over file, flags win over both, missing file is fine
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add src/internal/config/dotenv.go: load the working directory's .env (KEY=VALUE lines, comments, optional quoting and export prefix; missing file is fine, malformed lines error with file:line), plus a lookup that layers .env values under the shell environment (shell wins, file fills gaps; no process-env mutation).
2. Wire the lookup into config.Load (listener) and config.LoadWorker (worker): flags > shell env > .env > defaults.
3. Commit src/.env.example documenting every supported variable with defaults and comments; add /src/.env to the root .gitignore.
4. Document the workflow in src/README.md (copy, fill, run); note in the justfile's run recipes that the binaries load src/.env themselves (they run in src/), so no dotenv-load or sourcing is needed.
5. Add src/internal/config/dotenv_test.go: parse rules, listener + worker load values from .env, shell env wins over file, flags win over both, missing file is fine, malformed file errors.
6. Run go test ./... and functionally verify both binaries (go run and just run-*) with a .env: values applied, missing file fine.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented .env support in internal/config: new dotenv.go loads .env from the working directory (missing file fine, malformed line errors as .env:<line>), envLookup layers file values under the shell environment (file fills gaps, never overrides), and Load/LoadWorker resolve flags > shell env > .env > defaults with no process-env mutation. Committed src/.env.example (every variable with defaults and comments), gitignored /src/.env, documented the workflow in src/README.md, and noted on the justfile's run recipes that the binaries load src/.env themselves (verified just's dotenv-load searches the invocation directory, not src/, so it is the wrong mechanism here). dotenv_test.go covers parsing, both entry points, shell-over-file, flags-over-both, missing file, and malformed file. Verified functionally against the running Temporal dev server: listener and worker pick up .env values (listen=:9798, task_queue=env-file-queue, host_port=127.0.0.1:7233 from the file), shell and flags win over the file, missing .env runs on defaults, malformed .env fails startup naming file and line, and just run-listener/run-worker load the same file.

Round-trip verified the committed template: cp .env.example .env (unmodified) + watched.yaml in a scratch directory runs the listener on the documented defaults, so a fresh copy is a working local setup. Corrected the .env.example comments for FORGEJO_TOKEN and LLM_API_KEY: the worker now requires both at startup (provider and llm clients validate them since TASK-7/8).
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added .env support for local development configuration, stdlib-only, loading the file inside the config package so both binaries pick it up at startup with no process-env mutation.

What changed:
- src/internal/config/dotenv.go (new): loads .env from the working directory. Missing file is fine (nil values); a malformed line fails startup with .env:<line>: <problem> from either binary. Parses KEY=VALUE lines, blank lines and # comments, optional export prefixes, single/double-quoted values (closing quote must end the line), inline comments on unquoted values. envLookup layers file values under the shell environment: the file fills gaps, it never overrides a variable the shell has already set.
- src/internal/config/config.go + worker.go: Load and LoadWorker read .env and resolve through the layered lookup. Precedence: flags > shell env > .env > defaults, unchanged elsewhere.
- src/.env.example (new, committed): documents all 11 supported variables with the built-in defaults and per-variable comments, including the parse rules; a fresh copy is a working local setup.
- .gitignore: /src/.env ignored (verified with git check-ignore; .env.example stays committable).
- src/README.md: new Local development: .env section (copy, fill, run with just or go run) and the precedence chain updated in the Running intro.
- justfile: comments on run-listener/run-worker noting the binaries load src/.env themselves. just's dotenv-load was verified to search the invocation directory (repo root), not src/, so it cannot find the file; the binary-side loading is the correct mechanism and needs no justfile semantics.
- src/internal/config/dotenv_test.go (new): parser table (values, quotes, export, comments, CRLF, overwrites), error table (missing =, empty key, whitespace key, unterminated quotes, line numbers), and end-to-end tests through Load and LoadWorker: values load from the file, shell wins over file, flags win over shell and file, missing file runs on shell + defaults, malformed file fails both binaries.

Verification: go test ./... green (also via just test), gofmt clean, go vet clean. Functionally verified against the running Temporal dev server: listener and worker pick up .env values (listen=:9798, task_queue=env-file-queue, host_port=127.0.0.1:7233 from the file), shell and flags win over the file, missing .env runs on defaults, malformed .env exits 2 naming file and line, and just run-listener / just run-worker load the same file. The committed .env.example round-trips: an unmodified copy runs the listener on the documented defaults.

Risks/follow-ups: .env is loaded from the working directory only (src/ when run from there, matching the README workflow); running a built binary from elsewhere needs a .env in that directory. The README env table still describes the forgejo token and llm api key as required once the agent workflows consume those clients, though both are now required at worker startup (the .env.example comments state this correctly) — a pre-existing wording gap, left for a docs pass.
<!-- SECTION:FINAL_SUMMARY:END -->
