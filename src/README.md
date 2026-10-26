# Event Listener

A standalone, event-driven webhook listener for the local Forgejo instance.
It receives repository webhooks, verifies their authenticity, and dispatches
new commits and new merge request comments as structured, provider-agnostic
events that downstream consumers (the planned TASK-2 refinement and TASK-3
implementation agents) can subscribe to.

Built with the Go standard library only (`net/http`, `log/slog`).

## Running

```sh
cd src
go run ./cmd/listener
```

Configuration is environment variables and flags — no code changes needed:

| Environment variable | Flag          | Default | Description                                          |
| -------------------- | ------------- | ------- | ---------------------------------------------------- |
| `LISTEN_ADDR`        | `-listen`     | `:8080` | Address the webhook endpoint listens on              |
| `WEBHOOK_SECRET`     | `-secret`     | —       | Shared secret used to authenticate webhook deliveries (required) |
| `WATCHED_REPOS`      | `-watched`    | —       | Comma-separated watched project names (required)     |
| `DEDUPE_TTL`         | `-dedupe-ttl` | `24h`   | How long event identities are remembered for duplicate suppression |

Example:

```sh
WEBHOOK_SECRET=s3cret WATCHED_REPOS=BeanieGenie,OtherRepo \
  go run ./cmd/listener
```

## Webhook route

The endpoint is `POST /v1/{project}/webhooks/{provider}`.

- `{project}` is the watched project name (the repository name), e.g. the
  repo `root/BeanieGenie` is watched as `BeanieGenie`.
- `{provider}` selects the payload adapter: `forgejo` (Forgejo instances)
  and `gitea` (same wire format, different header names) are built in. New
  providers are one `RegisterProvider` call away in `internal/server`.

Deliveries for unwatched projects or unknown providers return `404`.
Unauthenticated deliveries return `401`. Deliveries the listener does not
care about (other event types, comment edits, plain issue comments) return
`200` so the provider does not redeliver them.

A `GET /healthz` endpoint reports liveness.

## Wiring up Forgejo

1. **Allow webhooks to reach local services.** Forgejo only delivers webhooks
   to explicitly allowed hosts (`webhook.ALLOWED_HOST_LIST`, default
   `external`). The instance in `forgejo/docker-compose.yaml` sets:

   ```yaml
   - FORGEJO__webhook__ALLOWED_HOST_LIST=private,169.254.0.0/16
   ```

   `169.254.0.0/16` covers the host-mapping address used by rootless podman
   networking (see below); restart the container after changing this.

2. **Create a repository webhook** (Settings → Webhooks on the repo, or via
   the API) with:
   - Target URL: `http://169.254.1.2:8080/v1/BeanieGenie/webhooks/forgejo`
   - Method: `POST`, Content type: `application/json`
   - Secret: the same value as `WEBHOOK_SECRET`
   - Trigger on: **Push events** and **Pull request comment** (issue comment
     events carry merge request comments)

   `169.254.1.2` is the address the Forgejo container uses to reach services
   on the host when running under rootless podman (pasta networking). If
   Forgejo runs elsewhere, use the address that reaches the listener.

   Via the API (the `events` field must be included on every PATCH, or the
   event selection is reset):

   ```sh
   curl -X POST -H "Authorization: token $TOKEN" \
     -H "Content-Type: application/json" \
     http://localhost:3000/api/v1/repos/root/BeanieGenie/hooks -d '{
       "type": "gitea", "active": true,
       "events": ["push", "pull_request_comment", "issue_comment"],
       "config": {"url": "http://169.254.1.2:8080/v1/BeanieGenie/webhooks/forgejo",
                   "content_type": "json", "secret": "s3cret"}
     }'
   ```

## Dispatched events

All events are provider-agnostic — no Forgejo payload format leaks through.
Subscribers receive them through the dispatcher (see `internal/event`):

```go
srv.Subscribe(func(ctx context.Context, ev event.Event) {
    switch ev.Kind {
    case event.KindCommit:  // ev.Commit: SHA, Message, Author, Branch, ...
    case event.KindComment: // ev.Comment: ID, Body, Author, MR (number, title, ...)
    }
})
```

Handlers run asynchronously, one goroutine per subscriber per event, with a
context detached from the webhook request — slow consumers never delay the
webhook response. The binary ships with a logging consumer that prints every
dispatched event; downstream agents subscribe the same way.

### Duplicate deliveries

Forgejo redelivers failed deliveries, and redeliveries get fresh delivery
IDs, so delivery IDs cannot be used for deduplication. Instead every event
carries a stable identity (`Event.DedupeKey`: provider + repository + commit
SHA or comment ID). The listener keeps a TTL window (`DEDUPE_TTL`, default
24h) and drops duplicates before dispatching, so a redelivered push or
comment is dispatched to consumers exactly once. The store is in-memory: a
restart forgets the window.

## Architecture

```
cmd/listener            entry point: config, logging consumer, lifecycle
internal/config         flag + env configuration
internal/event          provider-agnostic event model + dispatcher (subscribers)
internal/source         EventSource abstraction (Start → routes + dispatcher)
internal/source/webhook the webhook event source: route, auth, dedupe, dispatch
  └─ forgejo.go         Forgejo/Gitea provider adapter: HMAC-SHA256 verification,
                        push + issue-comment parsing
internal/server         wires sources, mux, HTTP server, graceful shutdown
```

Detection goes through the pluggable `source.EventSource` abstraction: the
webhook receiver is the first source, and future sources (polling, message
queues) can be added by implementing `Start` and attaching to the same
dispatcher without reworking event dispatch. Provider adapters are a second,
independent axis: any Forgejo-compatible provider is one adapter away.

The Forgejo adapter's payload handling is tested against fixtures captured
from a live Forgejo 16.0.3 instance (`internal/source/webhook/testdata`).

## Testing

```sh
cd src
go test ./...
```
