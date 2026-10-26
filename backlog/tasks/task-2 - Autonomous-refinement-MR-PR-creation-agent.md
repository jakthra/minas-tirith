---
id: TASK-2
title: Autonomous refinement MR/PR creation agent
status: Backlog
assignee: []
created_date: '2026-10-20 07:57'
labels: []
milestone: m-0
dependencies: []
type: feature
ordinal: 2000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The agent should generate MR/PRs automatically for refining technical stories into implementation tasks. The user must use the MR/PR as the entrypoint for adjusting the refinement of the provided user story. The flow must works as follows:

1. User commits new feature request into the respository
2. The agent picks up the feature requests and refines technical implementation tickets
3. The implementation tickets + any questions nececessary are provided in an MR format
4. The agent must change accordingly to user comments in the MR
5. Once user is satistfied, the MR can be merged, bringing the refined tickets into the main branch

For now `backlog.md` is utilized as the backlog store, such that all can be stored as .md files in the repository.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 New user stories trigger MR creation
- [ ] #2 MR contains implementation tickets for the provided user story
- [ ] #3 Agents change files using Inline comments
- [ ] #4 Agents change files using generally summary comments
<!-- AC:END -->
