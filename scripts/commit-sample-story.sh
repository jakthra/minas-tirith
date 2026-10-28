#!/usr/bin/env bash
# commit-sample-story.sh — commit a sample user story to the template
# repository and push it to the dev Forgejo.
#
# The template repository checked out at repos/BeanieGenie is the watched
# repository the refinement agent acts on (the TASK-2 flow): a commit that
# adds a user story under backlog/tasks/ arrives as a push webhook and
# starts the userStoryReviewWorkflow. This script writes a sample story
# in backlog.md format, commits it on the current branch, and pushes it
# to origin, so one command exercises the whole flow.
#
# Every run creates a distinct story — the next free task id — so repeated
# runs never collide and each produces a fresh commit and workflow trigger.
#
# Usage:
#   just commit-sample-story
#   ./scripts/commit-sample-story.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="$repo_root/repos/BeanieGenie"
story_dir="backlog/tasks"
title="Track watering history per plant"

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

# --- pick the next free story id ----------------------------------------

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
    die "refusing to overwrite existing story $file"
fi

# --- write the sample story in backlog.md format ------------------------

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
type: feature
ordinal: $ordinal
---

## Description

As a gardener using BeanieGenie, I want to see the watering history for each
of my plants, so that I can avoid over- and under-watering them.

The history should list every watering event for the plant with its date, and
let me register a manual watering event for the times I water outside the app.

## Acceptance Criteria

- [ ] #1 Each plant has a watering history listing its past watering events with dates
- [ ] #2 Watering through the app is registered in the plant's history automatically
- [ ] #3 The gardener can add a manual watering event with a custom date
EOF

# --- commit --------------------------------------------------------------

git -C "$repo" add "$file"
if ! git -C "$repo" commit -m "Add user story: $title" > /dev/null; then
    git -C "$repo" reset -- "$file" > /dev/null 2>&1 || true
    rm -f "$path"
    die "git commit failed in the template repository; nothing was committed and the story file was removed"
fi
sha="$(git -C "$repo" rev-parse --short HEAD)"

# --- push ----------------------------------------------------------------

if ! git -C "$repo" push origin "$branch"; then
    die "push to origin failed — is the dev Forgejo running? Start it with: just forgejo-up
The commit stays local in $repo; push it once Forgejo is up:
  git -C repos/BeanieGenie push origin $branch"
fi

echo "Committed sample user story to $repo (branch $branch):"
echo "  $sha  $file"
echo "Pushed to origin — the push webhook should start the user story review workflow."
