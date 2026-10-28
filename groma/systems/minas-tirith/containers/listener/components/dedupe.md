---
type: C4 Component
title: Dedupe store
status: stable
groma:
  id: dedupe
  parent: listener
  code:
    - scanner: go
      file: src/internal/dedupe/dedupe.go
  group: Shared event pipeline
description: In-memory TTL store that suppresses duplicate webhook deliveries.
---

Providers redeliver failed deliveries with fresh delivery IDs, so dedupe keys on stable event identity (Event.DedupeKey), not delivery IDs. Seen records a key and reports whether it was already seen within the TTL window (DEDUPE_TTL, default 24h); a sweep past 4096 keys keeps memory bounded by recent activity. The store is in-memory: a restart forgets the window, which is why the workflow start additionally deduplicates at the Temporal level (the starter's workflow-ID reuse policy).
