// Package llm abstracts the LLM provider API behind a small Go
// interface.
//
// The autonomous agent workflows reason through Client: generating
// implementation tickets from user stories (TASK-7) and interpreting
// review comments to update them (TASK-11). LLM API calls live in
// Temporal activities, never in workflow bodies, so workflow code
// stays deterministic; and Client is an interface, so activity tests
// substitute fakes (Fake) without any real API calls.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Message roles in a conversation.
const (
	// RoleSystem is the system message role. Chat and ChatJSON carry
	// the system prompt separately, so history messages use only
	// RoleUser and RoleAssistant.
	RoleSystem = "system"
	// RoleUser is a user turn of the conversation.
	RoleUser = "user"
	// RoleAssistant is a model turn of the conversation.
	RoleAssistant = "assistant"
)

// Message is one turn of the conversation history sent to the model.
type Message struct {
	// Role is the message's role: RoleUser or RoleAssistant.
	Role string
	// Content is the message's text.
	Content string
}

// Usage are the token counts one chat completion consumed, as the
// provider reports them. They are logged by the client for cost
// tracking.
type Usage struct {
	// InputTokens is the tokens the prompt consumed (API:
	// prompt_tokens).
	InputTokens int
	// OutputTokens is the tokens the reply consumed (API:
	// completion_tokens).
	OutputTokens int
}

// Response is the model's reply to one chat completion.
type Response struct {
	// Content is the reply text. In JSON mode it is the raw JSON
	// string before parsing.
	Content string
	// Model is the model that produced the reply, as the provider
	// reports it.
	Model string
	// Usage are the exchange's token counts.
	Usage Usage
	// FinishReason is why the model stopped generating, e.g. "stop",
	// or "length" when the reply was cut short by the output limit.
	FinishReason string
}

// Client abstracts the LLM operations the autonomous agents need.
// Implementations live in this package (ChatCompletionsClient for real
// OpenAI-compatible APIs, Fake for tests); activities and their tests
// depend on this interface, so no LLM API is called from test code.
type Client interface {
	// Chat sends the system prompt and the conversation history (user
	// and assistant turns) to the model and returns its reply. An
	// empty system prompt sends no system message.
	Chat(ctx context.Context, system string, messages []Message) (Response, error)

	// ChatJSON is Chat in JSON mode: the provider is asked to answer
	// with a single JSON object, and the reply is parsed into out (a
	// pointer to the value to unmarshal into). Use it where the reply
	// must parse reliably, e.g. generating implementation tickets.
	//
	// The reply should be a single JSON object — top-level arrays are
	// not portable across providers, so ask for an object wrapping
	// the list ("{"tickets": [...]}") instead of a bare array. The
	// returned Response is the unparsed exchange (Content carries the
	// raw JSON string), so token usage is available even when parsing
	// fails with ErrInvalidJSON: the tokens were spent either way.
	ChatJSON(ctx context.Context, system string, messages []Message, out any) (Response, error)
}

// APIError is an LLM API error response outside the 2xx range. The
// StatusCode carries the provider's HTTP status; Message carries the
// provider's error message when it sent one. Rate limits (429) and 5xx
// responses are retried with backoff before an APIError is returned;
// every other status fails the call immediately.
type APIError struct {
	// StatusCode is the HTTP status the provider answered.
	StatusCode int
	// Status is the HTTP status line, e.g. "429 Too Many Requests".
	Status string
	// Message is the provider's error message, if any.
	Message string
	// RetryAfter is the pause the provider asked for before the next
	// attempt (Retry-After header), when sent and parseable.
	RetryAfter time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("llm api error: %s", e.Status)
	}
	return fmt.Sprintf("llm api error: %s: %s", e.Status, e.Message)
}

// ErrInvalidJSON reports that a JSON-mode reply was not a parseable
// JSON object, after the client asked the provider for one.
var ErrInvalidJSON = errors.New("llm response is not valid json")

// validateHistory checks the message history Chat and ChatJSON share:
// at least one message, each a non-empty user or assistant turn. Both
// implementations reject the same inputs, so tests against Fake
// exercise the same input contract as production.
func validateHistory(messages []Message) error {
	if len(messages) == 0 {
		return errors.New("at least one message is required")
	}
	for i, m := range messages {
		switch m.Role {
		case RoleUser, RoleAssistant:
		default:
			return fmt.Errorf("message %d has invalid role %q: want %q or %q",
				i, m.Role, RoleUser, RoleAssistant)
		}
		if strings.TrimSpace(m.Content) == "" {
			return fmt.Errorf("message %d is empty", i)
		}
	}
	return nil
}

// parseJSONContent parses a JSON-mode reply into out. Providers asked
// for a JSON object usually answer with one, but some (especially
// self-hosted models) wrap it in a markdown code fence or surround it
// with prose, so the JSON object is located before parsing: the
// content as-is, then fence-stripped, then the outermost {...} slice.
func parseJSONContent(content string, out any) error {
	trimmed := strings.TrimSpace(content)
	candidates := []string{trimmed, stripCodeFence(trimmed)}
	if obj := sliceJSONObject(trimmed); obj != "" {
		candidates = append(candidates, obj)
	}
	var err error
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if err = json.Unmarshal([]byte(c), out); err == nil {
			return nil
		}
	}
	if err == nil {
		err = errors.New("empty reply")
	}
	return fmt.Errorf("%w: %v; reply: %.200s", ErrInvalidJSON, err, trimmed)
}

// stripCodeFence removes the markdown code fence some models wrap
// JSON replies in: a first line starting with ``` (often ```json)
// and a closing ``` line.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// sliceJSONObject returns the outermost JSON object in s: from the
// first '{' to the last '}', or "" when s has none.
func sliceJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}
