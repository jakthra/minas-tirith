---
type: C4 Actor
title: Repository contributor
status: stable
groma:
  id: repository-contributor
description: Pushes commits and comments on merge requests in the watched Forgejo repositories.
---

A developer working in a watched Forgejo repository (for example root/BeanieGenie): they push commits and comment on merge requests. Their activity is what the system detects — pushes and new merge request comments are dispatched as provider-agnostic events that trigger the autonomous agents. In the TASK-2 refinement flow the contributor also reviews the MR the agent opens and steers the refinement with comments.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [repository-contributor](repository-contributor.md) | [forgejo](../externals/forgejo.md) | Pushes commits and MR comments | git push / Forgejo web UI |
