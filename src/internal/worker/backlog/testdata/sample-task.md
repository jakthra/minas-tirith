---
id: TASK-014
title: Add a plant to the garden
status: To Do
assignee: []
created_date: '2026-11-02 13:50'
updated_date: '2026-11-02 13:50'
labels: []
dependencies: []
type: task
ordinal: 14000
---

## Description

As a gardener using BeanieGenie, I want to add a plant to my garden, so that
BeanieGenie can track it and remind me when it needs water.

A plant is added with a name, a species, and how often it should be watered
(in days). The new plant appears in the garden list right away, and adding a
plant without a name is rejected with a clear message.

## Acceptance Criteria

- [ ] #1 The gardener can add a plant with a name, a species, and a watering interval in days
- [ ] #2 A newly added plant appears in the garden list immediately
- [ ] #3 Adding a plant without a name is rejected with a clear message

## Autonomous implementation

Implement the story above in this repository:

1. Find the garden's plant model and the code that lists plants.
2. Add the "add a plant" capability with name, species, and watering
   interval, including validation of the required name.
3. Add or update tests covering adding a plant, it appearing in the list,
   and the missing-name rejection.
4. Run the project's test suite and report the result.
