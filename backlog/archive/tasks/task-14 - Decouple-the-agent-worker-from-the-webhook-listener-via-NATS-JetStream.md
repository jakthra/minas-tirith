---
id: TASK-14
title: Decouple the agent worker from the webhook listener via NATS JetStream
status: In Progress
assignee:
  - '@jakobt'
created_date: '2026-10-27 13:36'
updated_date: '2026-10-28 14:14'
labels: []
dependencies: []
references:
  - src/internal/transport/transport.go
  - src/internal/transport/publisher.go
  - src/internal/transport/consumer.go
  - worker
  - listener
modified_files:
  - src/go.mod
  - src/go.sum
  - src/internal/transport/transport.go
  - src/internal/transport/publisher.go
  - src/internal/transport/consumer.go
  - src/internal/transport/transport_test.go
  - src/internal/worker/bridge.go
  - src/internal/worker/bridge_test.go
ordinal: 13000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The event dispatcher (internal/event) is an in-process fan-out, so subscribing means running the webhook listener in the same process: cmd/worker embeds the whole webhook ingestion server and opens an inbound HTTP port just to convert events into workflow starts. Events dispatched while no consumer is attached are dropped silently, there is no buffering or replay, and listener and worker cannot deploy or scale independently. Introduce NATS with JetStream as the event transport: the listener publishes verified, deduped, provider-agnostic events; the worker subscribes as a durable consumer, starts the triggered workflows, and acknowledges after the start succeeds, becoming a port-free process. Broker choice discussed 2026-10-27: NATS JetStream recommended over Redis Streams, RabbitMQ, Kafka/Redpanda, and a Postgres job table; the Temporal-only alternative (listener starts workflows directly, no broker) was deferred to keep room for multiple consumer types. Seams to build on: the event.Handler signature and the provider-agnostic event.Event model are where the NATS publisher and consumer adapters slot in; webhook source, dedupe, and provider adapters stay untouched.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The listener publishes each verified, deduped, provider-agnostic event to a JetStream stream
- [ ] #2 The worker consumes events through a durable JetStream consumer and acknowledges a message only after the triggered workflow start has been issued
- [ ] #3 An event published while the worker is down is delivered and processed once the worker reconnects
- [ ] #4 Duplicate deliveries do not start duplicate workflows (DedupeKey-derived workflow IDs and the existing duplicate-start reuse policy cover JetStream redelivery)
- [ ] #5 The worker binary opens no inbound port and embeds no HTTP listener; only cmd/listener receives webhooks
- [ ] #6 Events travel in a versioned JSON envelope so producer and consumer can evolve independently
- [ ] #7 README documents the new event flow and how to run NATS for local development
- [ ] #8 Publish and consume adapters are unit-tested against an in-process or testcontainer NATS
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add NATS deps: nats.go client (jetstream API) + nats-server for in-process tests.
2. New internal/transport package: versioned JSON envelope (v1), subjects minas-tirith.events.v1.<kind>, stream MINAS_TIRITH (file storage, bounded MaxAge, MsgID dedupe via DedupeKey); Publisher (event.Handler adapter, fail-fast stream ensure) and durable Consumer (ack after handler success, NakWithDelay on failure, Term on undecodable/poison envelopes).
3. Event bridge: HandleEvent returns an error (failed workflow starts surface to the consumer; already-started duplicates stay non-errors) so JetStream redelivery only happens when a start was not issued; update bridge tests.
4. Config: shared NATS_URL/-nats-url setting (default nats://localhost:4222) on both binaries; worker drops the embedded-listener config (Listener field, listen flags, DefaultWorkerListenAddr); update config tests.
5. cmd/listener: dial NATS fail-fast, ensure stream, subscribe the publisher; drain NATS on shutdown.
6. cmd/worker: port-free rewrite — durable consumer runs the event bridge; shutdown order: drain consumer, stop Temporal workers, close clients.
7. Unit tests against an embedded in-process NATS server: publish + envelope shape, MsgID dedupe, durable delivery while the worker is down, nak/redelivery, poison-message termination, end-to-end dispatcher->publisher->consumer.
8. Docs: README event flow + NATS local-dev section; justfile nats-dev recipe.
<!-- SECTION:PLAN:END -->
