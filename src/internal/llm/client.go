package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Retry defaults. Transient failures (rate limits, 5xx responses,
// transport errors) are retried with exponential backoff; everything
// else fails the call immediately.
const (
	// defaultMaxAttempts is the total attempts per chat, including the
	// first.
	defaultMaxAttempts = 3
	// defaultInitialBackoff is the pause before the second attempt;
	// each further pause doubles.
	defaultInitialBackoff = time.Second
	// retryAfterCap is the longest provider-asked pause (Retry-After)
	// a call tolerates. A provider asking for more fails the call
	// instead of sleeping: the caller's retry policy (e.g. the
	// Temporal activity retry) handles long rate limits better than a
	// blocked activity.
	retryAfterCap = 30 * time.Second
	// requestTimeout bounds one HTTP request. LLM completions take
	// longer than typical API calls, especially on self-hosted
	// models, so this is generous.
	requestTimeout = 2 * time.Minute
	// maxResponseBytes bounds the JSON decoded from a completion
	// response, against runaway replies.
	maxResponseBytes = 16 << 20
)

// ChatCompletionsClient is a Client speaking the OpenAI-compatible
// Chat Completions API (POST {baseURL}/chat/completions): Mistral (the
// default), OpenAI, Ollama, vLLM, LM Studio, OpenRouter, and every
// other endpoint implementing the format. Authentication is a provider
// API key (Authorization: Bearer header).
type ChatCompletionsClient struct {
	baseURL string // API root, no trailing slash, e.g. https://api.mistral.ai/v1
	apiKey  string
	model   string
	http    *http.Client

	// Retry policy, with defaults from the constants above. Tests
	// shrink the backoff and intercept sleep.
	maxAttempts    int
	initialBackoff time.Duration
	sleep          func(ctx context.Context, d time.Duration) error

	// logger receives token usage and retry notices.
	logger *slog.Logger
}

// NewChatCompletionsClient returns a client for the OpenAI-compatible
// API at baseURL, authenticating with the provider API key and
// requesting model. All three are required: configure them via
// LLM_BASE_URL, LLM_API_KEY, and LLM_MODEL (or the -llm-base-url,
// -llm-api-key, and -llm-model flags).
func NewChatCompletionsClient(baseURL, apiKey, model string) (*ChatCompletionsClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("llm base url is required")
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid llm base url %q", baseURL)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("llm api key is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("llm model is required")
	}
	return &ChatCompletionsClient{
		baseURL:        u.String(),
		apiKey:         strings.TrimSpace(apiKey),
		model:          strings.TrimSpace(model),
		http:           &http.Client{Timeout: requestTimeout},
		maxAttempts:    defaultMaxAttempts,
		initialBackoff: defaultInitialBackoff,
		sleep:          sleepContext,
		logger:         slog.Default(),
	}, nil
}

// Chat implements Client.
func (c *ChatCompletionsClient) Chat(ctx context.Context, system string, messages []Message) (Response, error) {
	return c.chat(ctx, system, messages, false)
}

// ChatJSON implements Client.
func (c *ChatCompletionsClient) ChatJSON(ctx context.Context, system string, messages []Message, out any) (Response, error) {
	if out == nil {
		return Response{}, errors.New("out must be a non-nil pointer")
	}
	resp, err := c.chat(ctx, system, messages, true)
	if err != nil {
		return Response{}, err
	}
	if err := parseJSONContent(resp.Content, out); err != nil {
		return resp, err
	}
	return resp, nil
}

// chat performs one chat completion (JSON mode when jsonMode),
// retrying transient failures with exponential backoff.
func (c *ChatCompletionsClient) chat(ctx context.Context, system string, messages []Message, jsonMode bool) (Response, error) {
	if err := validateHistory(messages); err != nil {
		return Response{}, err
	}
	if jsonMode {
		system = ensureJSONInstruction(system)
	}
	body := chatCompletionsRequest{
		Model:    c.model,
		Messages: wireMessages(system, messages),
	}
	if jsonMode {
		body.ResponseFormat = &wireResponseFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("encode chat completions request: %w", err)
	}

	backoff := c.initialBackoff
	for attempt := 1; ; attempt++ {
		resp, err := c.postChat(ctx, payload)
		if err == nil {
			c.logCompletion(resp, jsonMode)
			return resp, nil
		}
		if ctx.Err() != nil {
			return Response{}, err
		}
		if attempt >= c.maxAttempts || !retryable(err) {
			return Response{}, err
		}
		wait, ok := retryWait(err, backoff)
		if !ok {
			return Response{}, err
		}
		c.logger.Warn("llm chat completion failed, retrying",
			"attempt", attempt, "wait", wait.String(), "error", err.Error())
		if err := c.sleep(ctx, wait); err != nil {
			return Response{}, err
		}
		backoff *= 2
	}
}

// postChat performs one POST /chat/completions request and maps the
// outcome: non-2xx responses become *APIError, transport failures and
// undecodable 2xx bodies become wrapped errors.
func (c *ChatCompletionsClient) postChat(ctx context.Context, payload []byte) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("build chat completions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("post chat completions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, apiErrorFrom(resp)
	}
	var wire chatCompletionsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&wire); err != nil {
		return Response{}, fmt.Errorf("decode chat completions response: %w", err)
	}
	return responseFrom(wire, c.model)
}

// logCompletion records the exchange's token counts, for cost
// tracking and debugging.
func (c *ChatCompletionsClient) logCompletion(resp Response, jsonMode bool) {
	c.logger.Info("llm chat completion",
		"model", resp.Model,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"json", jsonMode)
}

// apiErrorFrom turns a non-2xx response into an *APIError. Error
// bodies come in two shapes — flat ("message", Mistral) and nested
// ("error.message", OpenAI) — and both are read.
func apiErrorFrom(resp *http.Response) error {
	var payload struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	// A truncated or non-JSON error body must not mask the status.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    payload.Message,
	}
	if apiErr.Message == "" {
		apiErr.Message = payload.Error.Message
	}
	if after, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && after > 0 {
		apiErr.RetryAfter = time.Duration(after) * time.Second
	}
	return apiErr
}

// retryable reports whether err is a transient failure worth another
// attempt: rate limits, 5xx responses, transport errors, and
// undecodable 2xx bodies (a flaky proxy can garble one response of
// several). Other errors — invalid requests, auth failures — cannot be
// fixed by trying again.
func retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	return true
}

// retryWait picks the pause before the next attempt: the provider's
// Retry-After when it asks for a short pause, otherwise the
// exponential backoff. It reports false when the provider asked for
// more than retryAfterCap: failing the call beats sleeping through the
// caller's own retry budget.
func retryWait(err error, backoff time.Duration) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		if apiErr.RetryAfter > retryAfterCap {
			return 0, false
		}
		return apiErr.RetryAfter, true
	}
	return backoff, true
}

// ensureJSONInstruction returns a system prompt that asks for JSON:
// system unchanged when it already does, otherwise the standard
// instruction appended (or standing alone when no system prompt was
// given). OpenAI rejects json_object requests whose prompt never
// mentions JSON, and every provider follows the instruction better
// when it is explicit.
func ensureJSONInstruction(system string) string {
	const instruction = "Respond with a single JSON object."
	if strings.Contains(strings.ToLower(system), "json") {
		return system
	}
	if strings.TrimSpace(system) == "" {
		return instruction
	}
	return strings.TrimSpace(system) + "\n\n" + instruction
}

// sleepContext waits for d, or until ctx is done. It is the client's
// default sleep; the field form lets tests intercept the backoff.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// wireMessages builds the API message list: the system prompt first
// (when given), then the conversation history.
func wireMessages(system string, messages []Message) []wireMessage {
	out := make([]wireMessage, 0, len(messages)+1)
	if s := strings.TrimSpace(system); s != "" {
		out = append(out, wireMessage{Role: RoleSystem, Content: s})
	}
	for _, m := range messages {
		out = append(out, wireMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// responseFrom maps a wire response to Response, falling back to the
// requested model when the provider does not echo one.
func responseFrom(wire chatCompletionsResponse, requestedModel string) (Response, error) {
	if len(wire.Choices) == 0 {
		return Response{}, errors.New("chat completions response has no choices")
	}
	model := wire.Model
	if model == "" {
		model = requestedModel
	}
	return Response{
		Content:      wire.Choices[0].Message.Content,
		Model:        model,
		Usage:        Usage{InputTokens: wire.Usage.PromptTokens, OutputTokens: wire.Usage.CompletionTokens},
		FinishReason: wire.Choices[0].FinishReason,
	}, nil
}

// --- request/response wire types (only the fields the client uses) ---

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireResponseFormat struct {
	Type string `json:"type"` // "json_object" asks for a JSON object reply
}

type chatCompletionsRequest struct {
	Model          string              `json:"model"`
	Messages       []wireMessage       `json:"messages"`
	ResponseFormat *wireResponseFormat `json:"response_format,omitempty"`
}

type chatCompletionsResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
