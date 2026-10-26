---
id: TASK-3
title: Autonomous implementation agent
status: Backlog
assignee: []
created_date: '2026-10-20 08:02'
updated_date: '2026-10-20 08:02'
labels: []
milestone: m-0
dependencies: []
type: feature
ordinal: 3000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
An agent is needed for creating and implementing code respecting the refined technical tickets provided by TASK-2. The agent must convey questions for implementation if any doubt is raised on an MR. It must tag the refiner agent, if any things are unrefined. The flow must works as follows:

Commit event with a task added to backlog must trigger an implementation agent. The agent must implement and execute the details of the ticket. It should output the implementation of the ticket in the form of an MR tagged with the ticket number.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 MR is created after an implementation task is added
- [ ] #2 Draft MRs must be created if the implementation agent cannot fulfill the implementation.
- [ ] #3 The implementation agent must include the refiner agent for further refinement if condition of AC2 is met
<!-- AC:END -->
