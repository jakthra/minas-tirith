---
type: C4 Component
title: Temporal client
status: stable
groma:
  id: client
  parent: worker
  code:
    - scanner: go
      file: src/internal/worker/client.go
  group: Temporal execution
  technology: Temporal Go SDK (gRPC)
description: Eager, fail-fast connection to the Temporal server.
---

DialClient connects to the Temporal frontend (host:port + namespace) and fails at startup when the server is unreachable, so a misconfigured worker exits instead of silently running nothing. SDK-internal logs are routed through the default slog logger.

## Relationships

| Source | Target | Description | Technology |
| --- | --- | --- | --- |
| [client](client.md) | [temporal-server](../../../../../externals/temporal-server.md) | Connects over gRPC | Temporal frontend gRPC |
