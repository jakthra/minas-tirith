#!/usr/bin/env bash
# commit-sample-task.sh — commit a sample task to the template repository
# and push it to the dev Forgejo.
#
# The template repository checked out at repos/BeanieGenie is the watched
# repository the agents act on. This script writes a sample task in
# backlog.md format under backlog/tasks/, commits it on the current branch,
# and pushes it to origin.
#
# Unlike commit-sample-story.sh, whose story carries "type: feature" and
# starts the userStoryReviewWorkflow, this sample is an ordinary
# implementation task: it carries "type: task", which the shared backlog
# classification routes to taskImplementationWorkflow, plus an "Autonomous
# implementation" section describing the work an implementation agent
# should perform, so it can drive that flow end to end. The id is
# TASK-NNN either way — the backlog CLI names every item TASK-NNN — so the
# type is what makes this a task.
#
# Every run creates a distinct task — the next free task id — so repeated
# runs never collide and each produces a fresh commit.
#
# Usage:
#   just commit-sample-task
#   ./scripts/commit-sample-task.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="$repo_root/repos/BeanieGenie"
story_dir="backlog/tasks"
title="Add a plant to the garden"

die() {
    echo "error: $*" >&2
    exit 1
}

# --- locate the template repository -------------------------------------

if [[ ! -d "$repo/.git" ]]; then
    die "template repository not found at $repo
clone the watched repository there first, e.g.:
  git clone http://localhost:3000/root/BeanieGenie.git repos/BeanieGenie"
fi

branch="$(git -C "$repo" branch --show-current)"
if [[ -z "$branch" ]]; then
    die "the template repository at $repo has no current branch (detached HEAD)"
fi

# --- pick the next free task id -----------------------------------------

mkdir -p "$repo/$story_dir"
shopt -s nullglob
id=1
for existing in "$repo/$story_dir"/task-*.md; do
    n="$(basename "$existing")"
    n="${n#task-}"
    n="${n%% -*}"
    if [[ "$n" =~ ^[0-9]+$ ]] && (( 10#$n >= id )); then
        id=$(( 10#$n + 1 ))
    fi
done
shopt -u nullglob

# Zero-padded to three digits, matching the template repository's
# backlog config (zero_padded_ids: 3).
slug="${title// /-}"
file="$story_dir/task-$(printf '%03d' "$id") - $slug.md"
ordinal=$(( id * 1000 ))
now="$(date '+%Y-%m-%d %H:%M')"
path="$repo/$file"

if [[ -e "$path" ]]; then
    die "refusing to overwrite existing task $file"
fi

# --- write the sample task in backlog.md format -------------------------

cat > "$path" <<EOF
---
id: TASK-$(printf '%03d' "$id")
title: $title
status: To Do
assignee: []
created_date: '$now'
updated_date: '$now'
labels: []
dependencies: []
type: task
ordinal: $ordinal
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
EOF

# --- commit --------------------------------------------------------------

git -C "$repo" add "$file"
if ! git -C "$repo" commit -m "Add task: $title" > /dev/null; then
    git -C "$repo" reset -- "$file" > /dev/null 2>&1 || true
    rm -f "$path"
    die "git commit failed in the template repository; nothing was committed and the task file was removed"
fi
sha="$(git -C "$repo" rev-parse --short HEAD)"

# --- push ----------------------------------------------------------------

if ! git -C "$repo" push origin "$branch"; then
    die "push to origin failed — is the dev Forgejo running? Start it with: just forgejo-up
The commit stays local in $repo; push it once Forgejo is up:
  git -C repos/BeanieGenie push origin $branch"
fi

echo "Committed sample task to $repo (branch $branch):"
echo "  $sha  $file"
echo "Pushed to origin."
