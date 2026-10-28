---
id: TASK-6
title: Curate the groma.md architecture for minas-tirith
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-26 13:07'
updated_date: '2026-10-26 13:18'
labels: []
dependencies: []
references:
  - minas-tirith
  - listener
  - config
  - event
  - dedupe
  - source
  - webhook
  - server
  - listener-main
  - worker-main
  - client
  - runner
  - bridge
  - worker-template
  - userstoryreview
  - forgejo-adapter
  - worker
  - repository-contributor
  - forgejo
  - temporal-server
  - commit-starts-the-user-story-review
modified_files:
  - groma/systems/minas-tirith/components/config.md
  - groma/systems/minas-tirith/components/worker.md
  - groma/systems/minas-tirith/components/event.md
  - groma/systems/minas-tirith/components/dispatcher.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/userstoryreview.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/workflow.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/activities.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/userstoryreview-template.md
  - groma/systems/minas-tirith/components/forgejo-adapter.md
  - groma/systems/minas-tirith/components/forgejo.md
  - groma/systems/minas-tirith/containers/worker/container.md
  - groma/systems/minas-tirith/containers/worker/components/bridge.md
  - groma/systems/minas-tirith/containers/worker/components/client.md
  - groma/systems/minas-tirith/containers/worker/components/runner.md
  - groma/systems/minas-tirith/containers/worker/components/userstoryreview.md
  - groma/systems/minas-tirith/containers/worker/components/worker-main.md
  - groma/systems/minas-tirith/containers/worker/components/worker-template.md
  - groma/systems/minas-tirith/containers/minas-tirith-worker/container.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/bridge.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/client.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/runner.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/worker-main.md
  - >-
    groma/systems/minas-tirith/containers/minas-tirith-worker/components/worker-template.md
  - groma/actors/repository-contributor.md
  - groma/externals/forgejo.md
  - groma/externals/temporal-server.md
  - groma/systems/minas-tirith/components/dedupe.md
  - groma/systems/minas-tirith/components/server.md
  - groma/systems/minas-tirith/components/source.md
  - groma/systems/minas-tirith/components/webhook.md
  - groma/project.md
  - groma/systems/minas-tirith/system.md
  - groma/systems/minas-tirith/containers/listener/container.md
  - groma/systems/minas-tirith/containers/listener/components/listener-main.md
  - groma/flows/commit-starts-the-user-story-review.md
ordinal: 5000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The groma.md architecture (groma/) is still at its first scan: the scanner placed 18 file-shaped components under the system, but no element has a description or overview, and there are no actors, external systems, relationships, or flows. Several records also do not reflect the code's responsibilities — e.g. src/internal/config/worker.go is scanned as a component titled "Worker" although it is the worker/Temporal configuration inside the config package, and the shared event-pipeline packages sit unexplained at system level. Curate the architecture through the groma CLI so it reflects the source: combine records that share one responsibility, settle what the two containers (listener and worker binaries) and the shared pipeline are, declare the actor and the external systems the scanner cannot see, author the relationships and the end-to-end flow, and keep this task's architecture links current as elements are renamed, combined, or created.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Records that share one responsibility are combined (config + worker config, event model + dispatcher, userstoryreview workflow + activities + template) and each surviving component owns the right source files
- [x] #2 Every element (system, containers, components, actor, external systems) has a title, a short description, and an overview a new reader can understand
- [x] #3 The actor (repository contributor) and the external systems (Forgejo, Temporal server) are declared with overviews
- [x] #4 Relationships cover the real interactions: Forgejo webhook delivery, ingestion to dedupe to parse to dispatch, subscription of the logging consumer and the event bridge, worker wiring, and Temporal execution
- [x] #5 A flow documents the end-to-end scenario: a commit on a watched repository starts the user story review workflow
- [x] #6 Two consecutive groma scans create no new components and keep curated groups and multi-file Code lists unchanged
- [x] #7 This task's references and modified files match the final architecture (renames and combines recorded)
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Combine records that share one responsibility: config+worker (internal/config), event+dispatcher (internal/event), userstoryreview workflow+activities+template (internal/worker/userstoryreview).
2. Rename records whose scanned ID no longer describes them: component forgejo -> forgejo-adapter (frees the name for the external Forgejo system), component workflow -> userstoryreview, container minas-tirith-worker -> worker (freed by the config combine).
3. Declare what the scanner cannot see: actor Repository contributor; external systems Forgejo (webhook source) and Temporal server (durable execution).
4. Describe every element (title, description, overview, technology) from the source and src/README.md; keep the shared event-pipeline packages at system level because both binaries embed them.
5. Author the relationships (Forgejo delivery, ingestion/dedupe/parse/dispatch, consumer subscription, worker wiring, Temporal execution) and the end-to-end flow 'Commit starts the user story review'.
6. Group the worker container's agent-agnostic Temporal components; verify with two consecutive scans and groma view.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
First scan state confirmed via groma view --plain and groma scan: 18 file-shaped components, no descriptions, no actors/externals/relationships/flows. Source read in full (src/cmd, src/internal).

Curation complete. Structure: combined config+worker config (internal/config), event model+dispatcher (internal/event), and userstoryreview workflow+activities+template into single-responsibility components; renamed forgejo -> forgejo-adapter (freeing the name for the external Forgejo system), workflow -> userstoryreview, and container minas-tirith-worker -> worker. The shared event-pipeline packages (config, events, dedupe, event source, webhook source, Forgejo adapter, HTTP server) stay at system level in a 'Shared event pipeline' group because both binaries embed them; the worker container's agent-agnostic Temporal parts (client, runner, agent template, event bridge) are grouped as 'Temporal execution'. Declared actor Repository contributor and external systems Forgejo and Temporal server. 27 authored relationships; flow 'Commit starts the user story review' (6 steps, each matching a directed relationship). Verification: every element record has title+description+overview; groma view --plain and element/container/system/file views render the full map; two consecutive groma scans created 0 components and left all architecture files byte-identical (md5 snapshots equal), so groups and multi-file Code lists survive rescans. groma lint reports only the two known helper duplicates (event.shortSHA/firstLine vs userstoryreview ShortSHA/FirstLine) — deliberate: Temporal workflow code keeps its own tiny deterministic helpers; no action. Issue found and fixed during curation: a raw '<workflow>' angle bracket in the bridge overview broke groma's Markdown parse, silently dropping that record's overview and its Relationships rows from the parsed model (visible as missing body/relationships in groma view); reworded without angle brackets. The terminal map strips {project}/{provider} tokens from displayed text only — the stored documents keep them.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Curated the groma.md architecture from its first scan into a described, connected map of the minas-tirith source, entirely through the groma CLI.

Structure (18 file-shaped records -> 14 truthful components): combined records sharing one responsibility — config + worker config (src/internal/config), event model + dispatcher (src/internal/event), and the userstoryreview workflow + activities + template into one 'User story review agent' component; renamed forgejo -> forgejo-adapter, workflow -> userstoryreview, and container minas-tirith-worker -> worker. The two containers are the binaries: Event listener (cmd/listener) and Agent worker (cmd/worker, which embeds the listener). The seven shared event-pipeline packages stay at system level (group 'Shared event pipeline') because neither binary owns them alone; the worker container's agent-agnostic Temporal parts are grouped as 'Temporal execution'.

Meaning: every element (project, system, 2 containers, 14 components, 1 actor, 2 external systems) now has a title, short description, and overview written from the source and src/README.md, plus technology where relevant. Declared what the scanner cannot see: actor Repository contributor; external systems Forgejo (signed webhook delivery) and Temporal server (durable execution).

Interactions: 27 authored relationships covering Forgejo webhook delivery, ingestion (auth, provider parsing, dedupe) to dispatch, subscription of the logging consumer and the event bridge, the worker's wiring (config, client, runner, bridge, template registration), and Temporal execution. One flow, 'Commit starts the user story review', walks the end-to-end scenario contributor push -> Forgejo webhook -> verify/parse/dedupe/dispatch -> event bridge -> workflow start -> Temporal execution; groma validated every step against a directed relationship.

Verification: element, container, system, and file views render the full map with source links; two consecutive groma scans created no components and left the architecture byte-identical (curated groups and multi-file Code lists survive rescans); all 7 acceptance criteria checked. groma lint's only findings are the two known shortSHA/FirstLine helper duplicates between internal/event and the userstoryreview workflow — deliberate, since Temporal workflow code keeps its own deterministic helpers. Follow-up worth noting: TASK-2's refinement logic will land in the userstoryreview component, and TASK-3 will add a second agent template next to it; both are one-registration changes the current architecture already anticipates.
<!-- SECTION:FINAL_SUMMARY:END -->
