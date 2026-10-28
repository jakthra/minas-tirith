# Minas Tirith

Event-driven services that connect repository activity to autonomous
agents: an event listener that receives repository webhooks and starts
the agent workflows they trigger on Temporal, and an agent worker that
executes those workflows durably.

Two binaries, one Go module (`minas-tirith`, built with the Go standard
library, the Temporal Go SDK, and a YAML parser for the declarative
config):

- **`cmd/listener`** — the webhook event listener. Receives webhooks
  (Forgejo today, other providers one adapter away), verifies their
  authenticity, drops duplicate deliveries, and starts the agent
  workflows the events trigger on Temporal.
- **`cmd/worker`** — the agent execution service. A pure Temporal worker:
  it polls task queues and executes the registered agent workflows and
  activities. It opens no inbound port and receives no webhooks; the
  listener starts the workflows.

Deployments that run agents point the repository webhooks at the listener
and run the worker alongside it; the worker connects to the same Temporal
server and executes the workflows the listener starts.

## Running

```sh
cd src
go run ./cmd/listener
go run ./cmd/worker
```

Configuration is environment variables and flags — no code changes
needed. Flags win over environment variables, environment variables over
`.env` values, `.env` values over defaults — for local development,
both binaries load a [`.env` file](#local-development-env) from the
working directory. The watched repositories and their webhook secrets
are declarative: they come from a config file (see
[Watched repositories](#watched-repositories)), not from flags or
environment variables.

| Environment variable | Flag | Default | Description |
| --------------------- | ---- | ------- | ----------- |
| `LISTEN_ADDR` | `-listen` | `:9797` | Address the webhook endpoint listens on (listener only) |
| `WATCHED_CONFIG` | `-watched-config` | `watched.yaml` | Path to the declarative watched-repositories config file; falls back to `watched.yaml` in the working directory when unset (the committed dev config when running from `src/`) |
| `DEDUPE_TTL` | `-dedupe-ttl` | `24h` | How long event identities are remembered for duplicate suppression (listener only) |
| `TEMPORAL_HOST_PORT` | `-temporal-host-port` | `localhost:7233` | Temporal frontend gRPC address |
| `TEMPORAL_NAMESPACE` | `-temporal-namespace` | `default` | Temporal namespace |
| `TEMPORAL_TASK_QUEUE` | `-temporal-task-queue` | `minas-tirith` | Base task queue; each agent polls `<queue>-<template>` |
| `FORGEJO_URL` | `-forgejo-url` | `http://localhost:3000` | Forgejo/Gitea instance the agent activities access repositories on (worker only) |
| `FORGEJO_TOKEN` | `-forgejo-token` | — | Forgejo access token for agent repository access (required once agent workflows consume the provider client; TASK-7/8 wire it in) |
| `LLM_BASE_URL` | `-llm-base-url` | `https://api.mistral.ai/v1` | OpenAI-compatible LLM API the agent activities reason through (worker only; any OpenAI-compatible endpoint — Mistral, OpenAI, Ollama, vLLM, LM Studio, OpenRouter — works) |
| `LLM_API_KEY` | `-llm-api-key` | — | LLM provider API key for agent reasoning (required once agent workflows consume the LLM client; TASK-7 wires it in) |
| `LLM_MODEL` | `-llm-model` | `mistral-small-latest` | Model requested for agent reasoning (worker only) |

Example:

```sh
go run ./cmd/listener   # loads the default dev config, watched.yaml
go run ./cmd/worker     # pure Temporal worker, no webhooks
```

### Local development: `.env`

Both binaries load a `.env` file from the working directory (`src/`)
at startup, before flags and defaults resolve — one place for local
values instead of exporting variables in every shell session:

```sh
cp .env.example .env   # then fill in your local values
```

- The committed [`.env.example`](.env.example) lists every supported
  variable with its default and a comment; the values shown are the
  built-in defaults, so a fresh copy is a working local setup.
- `.env` is gitignored: local secrets stay out of the repository.
- The precedence above is unchanged: flags win, then shell environment
  variables, then `.env` values, then defaults. The file fills gaps —
  it never overrides a variable the shell has already set.
- A missing `.env` is not an error: defaults, environment variables,
  and flags work exactly as before.
- `just run-listener` and `just run-worker` run the binaries in
  `src/`, so both load the same `.env` — no extra shell setup.

The file is parsed, not sourced: `KEY=VALUE` lines, blank lines and
`#` comments, optional `export ` prefixes, and single- or double-quoted
values. The header of `.env.example` spells out the exact rules.

Both binaries shut down gracefully on SIGTERM/SIGINT: the listener drains
its webhook server, and the worker stops its Temporal workers, letting
in-flight workflow and activity tasks finish before the process exits.

### Watched repositories

The watched repositories and their webhook secrets live in the
declarative config file `WATCHED_CONFIG` points at — a YAML document
listing every repository the listener accepts, each with its own webhook
secret:

```yaml
repos:
  - project: BeanieGenie
    secret: s3cret
  - project: Garden
    secret: another-secret
```

- `project` is the project name the repository is watched as — the
  `{project}` segment of the webhook route; the repo `root/BeanieGenie`
  is watched as `BeanieGenie`.
- `secret` is that repository's webhook secret: deliveries are verified
  with the secret of the project they are addressed to, so every
  repository can have its own. There is no shared fallback.

A config file that declares no repositories, or entries without a
project or secret, fails startup with every problem listed at once.
JSON parses too (YAML is its superset). The file is the deployment's
single declarative source: in Kubernetes, mount it from a ConfigMap and
point `WATCHED_CONFIG` at the mounted path. The listener loads it
through the `internal/configstore` `Store` interface — a future operator
or CRD-backed store implements the same interface and replaces the file
without touching the listener.

For local development the repository ships a default:
[`watched.yaml`](watched.yaml) next to `go.mod` watches the local dev
Forgejo instance's `BeanieGenie` with the dev webhook secret used in
the wiring examples below. Running from `src/`, both binaries load it
automatically when `WATCHED_CONFIG` is unset — no flags, no env. It is
a development convenience only: point `WATCHED_CONFIG` at your own file
for anything else, and change the secret before any real use.

### Temporal server

Both binaries need a Temporal server: the listener starts workflows on it,
the worker executes them.

- **Local development**: the Temporal CLI runs a self-contained dev server
  (web UI at http://localhost:8233):

  ```sh
  temporal server start-dev
  ```

  The defaults (`localhost:7233`, namespace `default`) match it.
- **Self-hosted production**: run a Temporal cluster via
  [Temporal's docker-compose setup](https://docs.temporal.io/self-hosted-guide/docker-compose)
  and point `TEMPORAL_HOST_PORT`/`TEMPORAL_NAMESPACE` at it.
- **Temporal Cloud**: set `TEMPORAL_HOST_PORT` to your cloud namespace's
  host and `TEMPORAL_NAMESPACE` accordingly.

## Webhook route

The endpoint is `POST /v1/{project}/webhooks/{provider}` (listener only;
the worker opens no inbound port).

- `{project}` is the watched project name (the repository name), e.g. the
  repo `root/BeanieGenie` is watched as `BeanieGenie`.
- `{provider}` selects the payload adapter: `forgejo` (Forgejo instances)
  and `gitea` (same wire format, different header names) are built in. New
  providers are one `RegisterProvider` call away in `internal/server`.

Deliveries for projects that are not declared in the watched config, or
unknown providers, return `404`. Unauthenticated deliveries — signed with
any secret other than the addressed project's — return `401`. Deliveries
the listener does not care about (other event types, comment edits,
plain issue comments) return `200` so the provider does not redeliver
them.

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
   - Target URL: `http://169.254.1.2:9797/v1/BeanieGenie/webhooks/forgejo`
   - Method: `POST`, Content type: `application/json`
   - Secret: the repository's `secret` as declared in the watched config
     file
   - Trigger on: **Push events** and **Pull request comment** (issue comment
     events carry merge request comments)

   `169.254.1.2` is the address the Forgejo container uses to reach services
   on the host when running under rootless podman (pasta networking). If
   Forgejo runs elsewhere, use the address that reaches the listener, port
   `:9797` (the default `LISTEN_ADDR`).

   Via the API (the `events` field must be included on every PATCH, or the
   event selection is reset):

   ```sh
   curl -X POST -H "Authorization: token $TOKEN" \
     -H "Content-Type: application/json" \
     http://localhost:3000/api/v1/repos/root/BeanieGenie/hooks -d '{
       "type": "gitea", "active": true,
       "events": ["push", "pull_request_comment", "issue_comment"],
       "config": {"url": "http://169.254.1.2:9797/v1/BeanieGenie/webhooks/forgejo",
                   "content_type": "json", "secret": "s3cret"}
     }'
   ```

## Dispatched events

All events are provider-agnostic — no Forgejo payload format leaks through.
The listener's event bridge (`internal/worker/bridge.go`) subscribes to the
dispatcher and starts the triggered workflows on Temporal:

```go
bridge := worker.NewEventBridge(starter, starter.Triggers())
srv.Subscribe(func(ctx context.Context, ev event.Event) {
    if err := bridge.HandleEvent(ctx, ev); err != nil {
        slog.Error("failed to start workflow", "error", err, "event", ev.Summary())
    }
})
```

Handlers run asynchronously, one goroutine per subscriber per event, with a
context detached from the webhook request — slow workflow starts never delay
the webhook response. The listener also ships with a logging consumer that
prints every dispatched event.

### Duplicate deliveries

Forgejo redelivers failed deliveries, and redeliveries get fresh delivery
IDs, so delivery IDs cannot be used for deduplication. Instead every event
carries a stable identity (`Event.DedupeKey`: provider + repository + commit
SHA or comment ID). The listener keeps a TTL window (`DEDUPE_TTL`, default
24h) and drops duplicates before dispatching, so a redelivered push or
comment is dispatched exactly once. The store is in-memory: a restart
forgets the window. The workflow start additionally deduplicates at the
Temporal level (see below), so an event re-dispatched across a restart maps
back to the same workflow execution.

## Agent worker

`cmd/worker` is the Temporal execution layer for the autonomous agents
(TASK-2 refinement, TASK-3 implementation). It is a pure Temporal worker:
it opens no inbound port and receives no webhooks. The listener starts the
workflows; the worker executes them.

- **Templates**: an agent type is a `worker.Template` — its workflow and
  activity implementations plus the repository events that should start
  them (`internal/worker`). The worker's runner registers each template on
  its own task queue (`TEMPORAL_TASK_QUEUE` + `-` + template name) and
  starts one Temporal worker per template. The listener registers the same
  templates on a `worker.Starter` to learn the workflow-to-task-queue
  mapping and the event triggers. A new agent type is one `Template`
  implementation plus one `Register` call in each binary; the worker core
  and lifecycle management stay untouched.
- **Event triggers**: the listener's event bridge
  (`internal/worker/bridge.go`) subscribes to its dispatcher and starts the
  triggered workflows on Temporal. Workflow IDs derive from the event
  identity (`<workflow>-<event DedupeKey>`), and starts use Temporal's
  "allow duplicate failed only" reuse policy: the same event dispatched
  twice starts the workflow once, while a workflow that failed can re-run.
  Duplicate starts are recognized and logged, not treated as errors.
- **`userStoryReviewWorkflow`** (`internal/worker/userstoryreview`): the
  first template: the TASK-2 autonomous refinement agent. Commit events
  on a watched repository start it with the commit's payload
  (repository, branch, SHA, message, changed files). It detects user
  stories (backlog.md files under `backlog/tasks/`), and for every
  story the commit added or modified it fetches the story content,
  generates implementation tickets with the LLM, and opens a
  refinement merge request: a `refine/<story-slug>-<short-sha>` branch
  carrying the tickets as backlog.md files, proposed into the branch
  the commit landed on, with a description summarizing the refinement
  and any questions the agent has for the user. Commits on the agent's
  own `refine/*` branches are declined (the agent never refines its own
  tickets). Comment handling is TASK-11.

Workflows can also be started manually, e.g. for testing:

```sh
temporal workflow start --task-queue minas-tirith-userstoryreview \
  --type userStoryReviewWorkflow \
  --input '{"repository":"root/BeanieGenie","branch":"main","sha":"8ef0de217a18","message":"Refine story"}'
```

Execution is visible in the Temporal UI (http://localhost:8233 for the dev
server) or CLI (`temporal workflow list`).

## Provider client

Agent workflows reach the repository through `internal/provider`: a
provider-agnostic `Client` interface (implemented by `ForgejoClient`,
speaking the Forgejo REST API that Gitea shares) covering the operations
the autonomous agents need:

- **Read**: fetch a file's content by path and ref (`GetFile`), list every
  file under a directory recursively (`ListFiles`).
- **Write**: create a branch from an existing ref (`CreateBranch`), write
  one or more file changes as a single commit — creating or updating
  each file as needed (`Commit`).
- **Merge requests**: open (`CreateMergeRequest`), fetch details
  (`GetMergeRequest`), list and add comments
  (`ListMergeRequestComments`, `AddMergeRequestComment`).

Repositories are addressed by full name (`root/BeanieGenie`); refs are
branch names or commit SHAs, an empty ref meaning the default branch.
Authentication is a provider access token; the worker is configured
through `FORGEJO_URL` and `FORGEJO_TOKEN` (or `-forgejo-url` /
`-forgejo-token`). Provider calls belong in activities, never in
workflow bodies; `provider.Fake` is an in-memory `Client` so activity
tests run without any real API calls.

The client maps 404 responses to `provider.ErrNotFound` (for
`errors.Is`) and every other non-2xx response to `*provider.APIError`;
activity code distinguishes "missing" from "rejected" without parsing
HTTP.

## LLM client

Agent workflows reason through `internal/llm`: a provider-agnostic
`Client` interface (implemented by `ChatCompletionsClient`, speaking
the OpenAI-compatible Chat Completions API that Mistral — the default —
OpenAI, Ollama, vLLM, LM Studio, and OpenRouter all implement) covering
the operations the autonomous agents need:

- **Chat**: send a system prompt and the conversation history (user and
  assistant turns), get the model's reply (`Chat`).
- **Structured JSON**: ask for a single JSON object and have the reply
  parsed into a Go value (`ChatJSON`) — the mode ticket generation uses
  for reliable parsing, with fenced or prose-wrapped replies still
  parsed.

Authentication is the provider API key (`Authorization: Bearer`); the
worker is configured through `LLM_BASE_URL`, `LLM_API_KEY`, and
`LLM_MODEL` (or `-llm-base-url` / `-llm-api-key` / `-llm-model`),
defaulting to the Mistral API. LLM calls belong in activities, never in
workflow bodies; `llm.Fake` is an in-memory `Client` (scripted
responses, recorded conversations) so activity tests run without any
real API calls.

The client retries transient failures itself — rate limits (429) and
5xx responses and transport errors, with exponential backoff and the
provider's `Retry-After` honored up to 30s; longer provider-asked
pauses and non-transient errors (4xx) fail the call, leaving retries to
the activity's Temporal retry policy. Every completion logs its token
usage (`model`, `input_tokens`, `output_tokens`) for cost tracking.

## Architecture

```
Webhook ──► cmd/listener ──► Temporal ──► cmd/worker
                │                              │
                │  receives webhooks,          │  pure Temporal worker:
                │  verifies + dedupes,         │  polls task queues,
                │  starts workflows            │  executes workflows/activities
                ▼                              ▼
         internal/server                internal/worker
         internal/source/webhook        (runner: one Temporal worker
         internal/event                  per template)
         internal/config                internal/provider
         internal/worker/bridge         internal/llm
         (event bridge: events
          → workflow starts)
         internal/worker/starter
         (workflow starts by
          type name)
```

```
cmd/listener            listener entry point: config, Temporal starter,
                        event bridge, logging consumer, HTTP server, lifecycle
cmd/worker              worker entry point: config, Temporal workers, lifecycle
internal/config         flag + env configuration (listener + worker)
internal/configstore    declarative config store for watched repos: Store interface,
                        File (YAML/JSON config file, per-repo webhook secrets),
                        Static (in-memory); the seam a K8s operator/CRD plugs into
internal/event          provider-agnostic event model + dispatcher (subscribers)
internal/source         EventSource abstraction (Start → routes + dispatcher)
internal/source/webhook the webhook event source: route, per-repo auth via the
                        config store, dedupe, dispatch
  └─ forgejo.go         Forgejo/Gitea provider adapter: HMAC-SHA256 verification,
                        push + issue-comment parsing
internal/server         wires sources, mux, HTTP server, graceful shutdown
internal/provider      Forgejo/Gitea repository API client behind the Client
                        interface (fetch files, list directories, create
                        branches, commit file changes, open merge requests,
                        read/write MR comments); in-memory Fake for tests
internal/llm           OpenAI-compatible LLM chat completions client behind
                        the Client interface (system prompt + history chat,
                        structured JSON mode, retry with backoff, token
                        usage logging); in-memory Fake for activity tests
internal/worker         Temporal execution layer: template registry, starter
                        (workflow starts by type name, used by the listener),
                        runner (one Temporal worker per template, used by the
                        worker), event bridge (events → workflow starts)
  └─ userstoryreview    first agent template: userStoryReviewWorkflow skeleton
                        (TASK-2 refinement agent) + commit trigger
```

Detection goes through the pluggable `source.EventSource` abstraction: the
webhook receiver is the first source, and future sources (polling, message
queues) can be added by implementing `Start` and attaching to the same
dispatcher without reworking event dispatch. Provider adapters are a second,
independent axis: any Forgejo-compatible provider is one adapter away.
Agent execution is a third: any agent is one template away, running its
own workflows on its own task queue, started by the event kinds it declares.

The Forgejo adapter's payload handling is tested against fixtures captured
from a live Forgejo 16.0.3 instance (`internal/source/webhook/testdata`).

## Testing

```sh
cd src
go test ./...
```

Workflow and activity code is tested with the Temporal SDK's test
environment (in-process execution, no server needed). The worker's
server-facing behavior (connectivity, registration, event-to-workflow
end-to-end, graceful drain) is verified against a running dev server.
