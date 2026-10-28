---
id: TASK-10
title: 'Create LLM client for agent reasoning (ticket generation, comment response)'
status: Done
assignee:
  - '@jakobt'
created_date: '2026-10-27 10:54'
updated_date: '2026-10-27 11:43'
labels: []
dependencies:
  - TASK-5
references:
  - 'TASK-2: Autonomous refinement MR/PR creation agent'
  - >-
    TASK-7: Implement user story detection and ticket generation in
    userStoryReviewWorkflow
modified_files:
  - src/internal/llm/llm.go
  - src/internal/llm/client.go
  - src/internal/llm/fake.go
  - src/internal/llm/client_test.go
  - src/internal/llm/fake_test.go
  - src/internal/config/worker.go
  - src/internal/config/worker_test.go
  - src/README.md
type: feature
ordinal: 9000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The autonomous refinement agent needs an LLM to reason about user stories and generate implementation tickets (TASK-7), as well as to interpret user comments and update tickets accordingly (future comment-handling task). No LLM client exists in the codebase yet.

This task creates an LLM client package that abstracts the LLM provider API behind a clean Go interface. The client will be used by Temporal activities in the agent workflows, keeping LLM API calls out of the workflow body (preserving Temporal determinism).

The client must support: chat completions with system prompts and conversation history, structured JSON output for reliable ticket generation, configurable model selection, retry logic for transient failures, and token usage logging for cost tracking. Authentication uses an API key configured via environment/flags.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 An `llm` package exists at `src/internal/llm/` with a `Client` interface abstracting LLM provider operations
- [x] #2 Client supports chat completions with system prompt and message history (user/assistant turns)
- [x] #3 Client supports structured JSON output mode for reliable parsing of generated tickets
- [x] #4 Client is configurable via environment variables (e.g., `LLM_API_KEY`, `LLM_MODEL`, `LLM_BASE_URL`) consistent with existing config layering
- [x] #5 Client implements retry logic with exponential backoff for transient failures (rate limits, 5xx errors)
- [x] #6 Client logs token usage (input/output tokens) for cost tracking and debugging
- [x] #7 Client interface is mockable so Temporal activity tests can substitute fakes without real API calls
- [x] #8 Unit tests cover chat completion, structured output parsing, retry behavior, and error handling using a mock HTTP server
<!-- AC:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Create internal/llm package: Client interface (Chat with system prompt + user/assistant history; ChatJSON structured-JSON mode), Message/Response/Usage types, APIError + ErrInvalidJSON, shared input validation and JSON extraction helpers.
2. Implement ChatCompletionsClient (internal/llm/client.go) speaking the OpenAI-compatible POST {base_url}/chat/completions wire format (Bearer API key auth, response_format json_object in JSON mode, auto JSON instruction guard): retries 429/5xx/transport errors with exponential backoff (3 attempts, 1s initial), honors short Retry-After, fails fast on long rate limits so caller-level (Temporal activity) retry policies take over; logs input/output token usage via slog.
3. Add llm.Fake (internal/llm/fake.go): in-memory Client with scripted responses and recorded conversations for Temporal activity tests.
4. Add LLMConfig to internal/config/worker.go: LLM_BASE_URL/LLM_MODEL/LLM_API_KEY env vars + -llm-base-url/-llm-model/-llm-api-key flags, flag>env>default layering, defaults to Mistral (https://api.mistral.ai/v1, mistral-small-latest); extend worker config tests.
5. Unit tests (internal/llm/client_test.go, fake_test.go) via httptest mock server: chat completion request/response shape, structured output parsing (incl. fenced/prose-wrapped JSON), retry/backoff behavior (429, 5xx, transport errors, Retry-After), error mapping (APIError, no retry on 4xx), token usage logging; fake behavior tests.
6. Update src/README.md (LLM env/flag table rows, LLM client section, architecture tree entry); go vet + go test ./... green.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented internal/llm: Client interface (Chat with system prompt + user/assistant history, ChatJSON structured-JSON mode), Message/Response/Usage types, APIError (+RetryAfter) and ErrInvalidJSON, shared history validation and robust JSON extraction (plain/fenced/prose-wrapped replies). ChatCompletionsClient speaks the OpenAI-compatible POST {base}/chat/completions (Bearer auth, response_format json_object, auto JSON instruction guard): retries 429/5xx/transport errors with exponential backoff (3 attempts, 1s initial, doubling), honors Retry-After up to 30s, fails fast on longer rate limits; logs model/input_tokens/output_tokens per completion via slog. llm.Fake scripts responses and records conversations for activity tests.

Added LLMConfig to the worker config: LLM_BASE_URL/LLM_API_KEY/LLM_MODEL env + -llm-base-url/-llm-api-key/-llm-model flags (flag>env>default), defaulting to the Mistral API (https://api.mistral.ai/v1, mistral-small-latest). Tests: httptest mock server covering request/response shape, JSON mode and robust parsing, retry/backoff/Retry-After behavior, APIError mapping, usage logging, cancellation; fake tests; llm coverage 98.9% (remaining lines are defensive unreachable branches). go vet clean, go test -race ./... green. README updated (config table, LLM client section, architecture tree).
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Created the LLM client package the autonomous agents reason through.

What changed:
- src/internal/llm (new): Client interface — Chat (system prompt + user/assistant history) and ChatJSON (structured-JSON mode, reply parsed into a Go value, token usage returned even on parse failure). ChatCompletionsClient implements it over the OpenAI-compatible POST {base_url}/chat/completions wire format (Bearer auth, response_format json_object in JSON mode, JSON instruction auto-appended when the prompt doesn't ask for JSON — OpenAI rejects such requests). Robust JSON extraction handles fenced and prose-wrapped replies. Transient failures (429, 5xx, transport errors) are retried with exponential backoff (3 attempts, 1s doubling), honoring Retry-After up to 30s and failing fast on longer rate limits so the Temporal activity retry policy owns them; other 4xx fail immediately (*APIError, flat and nested error bodies read). Every completion logs model/input_tokens/output_tokens via slog. llm.Fake scripts responses and records conversations for activity tests; both implementations validate inputs identically.
- src/internal/config: LLMConfig — LLM_BASE_URL/LLM_API_KEY/LLM_MODEL env vars with -llm-base-url/-llm-api-key/-llm-model flags (flag > env > default), defaulting to the Mistral API (https://api.mistral.ai/v1, mistral-small-latest). API key not required at startup (required once TASK-7 wires the client in), matching the FORGEJO_TOKEN pattern.
- src/README.md: config table rows, LLM client section, architecture tree entry.

Verification: go vet ./... clean, gofmt clean, go test -race ./... green; internal/llm 98.9% statement coverage (remaining lines are defensive unreachable branches); httptest mock server covers request/response shape, JSON mode and robust parsing, retry/backoff/Retry-After behavior, APIError mapping, token logging, and cancellation; TestClientIsMockable demonstrates activity-style use with Fake, no API calls.

Decisions (user-approved): OpenAI-compatible wire format; Mistral API defaults; ship llm.Fake.

Risks/follow-ups: the client is not yet constructed in cmd/worker — TASK-7 wires it into the userStoryReview activities via config.LLM; the model alias mistral-small-latest auto-tracks Mistral Small releases.
<!-- SECTION:FINAL_SUMMARY:END -->
