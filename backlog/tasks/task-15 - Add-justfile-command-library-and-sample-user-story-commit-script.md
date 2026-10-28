---
id: TASK-15
title: Add justfile command library and sample user story commit script
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-28 13:44'
updated_date: '2026-10-28 13:47'
labels: []
dependencies: []
modified_files:
  - justfile
  - scripts/commit-sample-story.sh
ordinal: 14000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The repository has no shared entry point for its recurring commands: running the binaries, the Go test suite, and the dev infrastructure (Temporal dev server, dev Forgejo) are all typed out by hand, and the README is the only place they are documented. The template repository checked out at repos/BeanieGenie is the watched repository the refinement agent acts on, but triggering the agent end-to-end (the TASK-2 flow: a user commits a user story, the agent refines it) currently means hand-writing a story file in backlog.md format, committing, and pushing to the dev Forgejo. Add just as the repository's general command library, including a command that commits a sample user story to the template repository and pushes it, so one command exercises the whole flow.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A justfile at the repository root lists the available commands by default and covers the general development commands (Go test suite, running the listener and worker binaries, Temporal dev server, dev Forgejo instance)
- [x] #2 A just command commits a sample user story to the template repository at repos/BeanieGenie and pushes it to its origin (the dev Forgejo)
- [x] #3 The sample user story is a markdown file in backlog.md format under backlog/tasks/ of the template repository, matching what the userStoryReviewWorkflow detection (TASK-7) treats as a user story
- [x] #4 Repeated runs do not collide: each run creates a distinct story (incrementing task id) so every run produces a fresh commit and a fresh workflow trigger
- [x] #5 The script fails with a clear error message when the template repository checkout is missing or the push to the dev Forgejo fails, without leaving an uncommitted story file behind
- [x] #6 The justfile follows the recipe style of the sibling BeanieGenie justfile (default recipe lists commands, working-directory attributes)
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add a root justfile: default recipe lists commands; general recipes for the Go test suite, running the listener and worker, the Temporal dev server, and the dev Forgejo (podman-compose up), following the BeanieGenie justfile style (working-directory attributes, kebab-case recipe names).

2. Add scripts/commit-sample-story.sh: resolves the template repository checkout at repos/BeanieGenie, picks the next free zero-padded task id under backlog/tasks/, writes a sample user story in backlog.md markdown format, commits it with git, and pushes the current branch to origin (dev Forgejo) so the push webhook triggers the userStoryReviewWorkflow.

3. Add the commit-sample-story recipe to the justfile that runs the script.

4. Test: just --list, just test, run the script against the template checkout; verify the commit lands on main and the push-failure path prints a clear hint when the dev Forgejo is down; run the script twice to confirm distinct stories.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Verified end to end against the running dev Forgejo: two runs of just commit-sample-story created backlog/tasks/task-001 (cba3f42) and task-002 (404033a) in repos/BeanieGenie, committed with message 'Add user story: Track watering history per plant' and pushed to origin main. Verified the missing-checkout error path (checkout moved aside: clear error, exit 1, nothing written) and the push-failure path (Forgejo stopped: clear error with the 'just forgejo-up' hint, exit 1, working tree clean — the commit stays local, nothing uncommitted left behind). Unpushed test commit was reset (git reset --hard 404033a) and Forgejo restarted afterwards, so the template repository and its origin hold exactly the two sample stories.

Decision: forgejo-up uses 'podman compose up -d' rather than BeanieGenie's 'podman-compose' because podman-compose is not installed on this machine; podman 6 delegates to the configured external compose provider (/usr/local/bin/docker-compose v5.0.0).

Decision: the script writes the story markdown directly and commits with plain git rather than driving the backlog CLI inside the template repository — the story is test-fixture data for the refinement agent, and plain git keeps the script dependency-free (git only).
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added just as the repository's general command library: a root justfile whose default recipe lists all commands, with recipes for the Go test suite (just test), running both binaries (just run-listener, just run-worker), the Temporal dev server (just temporal-dev), and the dev Forgejo instance (just forgejo-up), following the sibling BeanieGenie justfile style (working-directory attributes, kebab-case names). Added scripts/commit-sample-story.sh, exposed as just commit-sample-story: it writes a sample user story ('Track watering history per plant') in backlog.md format under backlog/tasks/ of the template repository at repos/BeanieGenie — the location the userStoryReviewWorkflow (TASK-7) treats as a user story — commits it on the current branch with a story-style message, and pushes it to origin (the dev Forgejo) so the push webhook triggers the refinement agent. Every run picks the next free zero-padded task id, so repeated runs produce distinct stories, fresh commits, and fresh workflow triggers. All acceptance criteria verified against the running dev Forgejo (two pushed stories, error paths exercised, failed push leaves the tree clean). The two sample stories remain pushed on the template repository's main (cba3f42, 404033a) as the tool's intended output; remove them with git reset + force-push if a pristine template repo is wanted.
<!-- SECTION:FINAL_SUMMARY:END -->
