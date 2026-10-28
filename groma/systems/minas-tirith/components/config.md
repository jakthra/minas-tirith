---
type: C4 Component
title: Configuration
status: stable
groma:
  id: config
  parent: minas-tirith
  code:
    - scanner: go
      file: src/internal/config/config.go
    - scanner: go
      file: src/internal/config/worker.go
description: Flag and environment configuration for both binaries.
---

Loads runtime settings from command-line flags layered over environment variables, then defaults — nothing requires a code change to reconfigure. Listener settings: LISTEN_ADDR (-listen, default :9797), WATCHED_CONFIG (-watched-config, path to the declarative watched-repos config; falls back to the working directory's watched.yaml when unset — the committed dev config when running from src/), DEDUPE_TTL (-dedupe-ttl, default 24h). Temporal settings shared by both binaries: TEMPORAL_HOST_PORT (-temporal-host-port, default localhost:7233), TEMPORAL_NAMESPACE (-temporal-namespace, default default), TEMPORAL_TASK_QUEUE (-temporal-task-queue, default minas-tirith). Worker-only settings: FORGEJO_URL/FORGEJO_TOKEN (provider client) and LLM_BASE_URL/LLM_API_KEY/LLM_MODEL (LLM client). Watched repositories and their per-repo webhook secrets are not flags or env: resolve builds a configstore File store from the resolved path and carries it on the listener configuration for the webhook source to load. Validation reports every problem in one error. Both files live in internal/config: config.go holds the listener flags, Temporal settings, and the flag/env/default resolution; worker.go adds the provider and LLM settings and LoadWorker.
