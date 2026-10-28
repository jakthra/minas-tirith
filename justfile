# Minas Tirith — general command library.
# `just` prints every command with its description.

default:
    just --list

# Run the Go test suite (listener and worker).
[working-directory('src')]
test:
    go test ./...

# Run the webhook event listener (:9797): receives webhooks, starts workflows.
# Runs in src/, so the binary loads src/.env at startup: local values
# without any shell setup (see src/.env.example).
[working-directory('src')]
run-listener:
    go run ./cmd/listener

# Run the agent worker: pure Temporal worker, executes workflows.
# Runs in src/, so the binary loads src/.env at startup: local values
# without any shell setup (see src/.env.example).
[working-directory('src')]
run-worker:
    go run ./cmd/worker

# Start the Temporal dev server (web UI at http://localhost:8233).
temporal-dev:
    temporal server start-dev

# Start the dev Forgejo instance (origin of the template repository).
[working-directory('forgejo')]
forgejo-up:
    podman compose up -d

# Commit a sample user story to the template repository and push it to the dev Forgejo.
commit-sample-story:
    ./scripts/commit-sample-story.sh
