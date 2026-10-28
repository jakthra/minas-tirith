---
type: C4 Component
title: Config store
status: stable
groma:
  id: config-store
  parent: minas-tirith
  code:
    - scanner: go
      file: src/internal/configstore/store.go
    - scanner: go
      file: src/internal/configstore/file.go
description: Declarative store for watched repositories and their webhook secrets.
---

Loads the repositories the listener watches, each with its own webhook secret, from a declarative source. Store is the seam a Kubernetes operator or CRD-backed implementation plugs into later without touching the listener. File reads the YAML config file (JSON parses too, YAML being its superset) that WATCHED_CONFIG / -watched-config points at — the shape a ConfigMap mounts — validating every declaration (non-empty project and secret, no duplicate projects) and reporting all problems in one error. Static serves a fixed in-memory list for wiring and tests. The webhook source loads the store at Start and verifies each delivery with the addressed project's secret.
