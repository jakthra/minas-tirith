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
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var _ Client = (*ChatCompletionsClient)(nil)

const (
	testAPIKey = "test-api-key"
	testModel  = "test-model"
)

// recordedRequest is one request the test server received.
type recordedRequest struct {
	Method string
	Path   string // URL path, e.g. /chat/completions
	Auth   string // Authorization header
	Body   string // raw request body
}

// requests records what the test server received, safe for the server
// goroutine(s) and the test goroutine.
type requests struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (rs *requests) add(r *http.Request, body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.reqs = append(rs.reqs, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Auth:   r.Header.Get("Authorization"),
		Body:   body,
	})
}

func (rs *requests) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.reqs)
}

func (rs *requests) last() recordedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.reqs[len(rs.reqs)-1]
}

// newTestClient returns a client against a test server that records
// every request before dispatching to handle, which receives the
// 1-based attempt number. No retry test actually sleeps: install a
// sleep recorder with recordSleep.
func newTestClient(t *testing.T, handle func(attempt int, w http.ResponseWriter, r *http.Request)) (*ChatCompletionsClient, *requests) {
	t.Helper()
	rec := &requests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body string
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			body = string(b)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		rec.add(r, body)
		handle(rec.count(), w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := NewChatCompletionsClient(srv.URL, testAPIKey, testModel)
	if err != nil {
		t.Fatalf("NewChatCompletionsClient: %v", err)
	}
	// Tests that assert logs replace this with captureLogs; the rest
	// keep the test output clean.
	c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return c, rec
}

// recordSleep intercepts the client's backoff sleeps and records the
// requested durations, so retry tests assert the backoff without
// waiting for it.
func recordSleep(c *ChatCompletionsClient) *[]time.Duration {
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return &waits
}

// captureLogs routes the client's logs into a buffer, for asserting
// the token usage log.
func captureLogs(c *ChatCompletionsClient) *bytes.Buffer {
	buf := &bytes.Buffer{}
	c.logger = slog.New(slog.NewTextHandler(buf, nil))
	return buf
}

// respondJSON writes a status with a JSON body.
func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// completion writes a successful chat completion response.
func completion(w http.ResponseWriter, model, content string, inputTokens, outputTokens int) {
	respondJSON(w, http.StatusOK, map[string]any{
		"id":      "cmpl-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	})
}

// apiErrorBody is an OpenAI-shaped error payload (nested message);
// the flat Mistral shape is exercised in TestChatAPIError.
func apiErrorBody(message string) map[string]any {
	return map[string]any{"error": map[string]any{
		"message": message,
		"type":    "invalid_request_error",
		"code":    "",
	}}
}

// chatHistory is a small conversation used across tests.
func chatHistory() []Message {
	return []Message{
		{Role: RoleUser, Content: "Refine the login user story."},
		{Role: RoleAssistant, Content: "I reviewed the story."},
		{Role: RoleUser, Content: "Generate the implementation tickets."},
	}
}

// decodeRequest decodes the last recorded request body.
func decodeRequest(t *testing.T, rec *requests) chatCompletionsRequest {
	t.Helper()
	var req chatCompletionsRequest
	if err := json.Unmarshal([]byte(rec.last().Body), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return req
}

func TestNewChatCompletionsClient(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		apiKey  string
		model   string
		wantErr string
	}{
		{name: "valid", baseURL: "https://api.mistral.ai/v1", apiKey: "key", model: "mistral-small-latest"},
		{name: "empty base url", baseURL: "", apiKey: "key", model: "m", wantErr: "llm base url is required"},
		{name: "base url without host", baseURL: "not a url", apiKey: "key", model: "m", wantErr: `invalid llm base url "not a url"`},
		{name: "empty api key", baseURL: "https://api.mistral.ai/v1", apiKey: "", model: "m", wantErr: "llm api key is required"},
		{name: "empty model", baseURL: "https://api.mistral.ai/v1", apiKey: "key", model: "", wantErr: "llm model is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewChatCompletionsClient(tt.baseURL, tt.apiKey, tt.model)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewChatCompletionsClient: %v", err)
				}
				if c == nil {
					t.Fatal("client is nil")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}

	t.Run("trailing slash is trimmed", func(t *testing.T) {
		c, err := NewChatCompletionsClient("http://localhost:11434/v1/", "key", "m")
		if err != nil {
			t.Fatal(err)
		}
		if c.baseURL != "http://localhost:11434/v1" {
			t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
		}
	})
}

func TestChat(t *testing.T) {
	system := "You refine user stories into implementation tickets."
	c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
		completion(w, testModel, "Here is the refined story.", 42, 7)
	})
	logs := captureLogs(c)

	resp, err := c.Chat(context.Background(), system, chatHistory())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// The request carries model, system prompt, history, and auth.
	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1", rec.count())
	}
	last := rec.last()
	if last.Method != http.MethodPost || last.Path != "/chat/completions" {
		t.Errorf("request = %s %s, want POST /chat/completions", last.Method, last.Path)
	}
	if last.Auth != "Bearer "+testAPIKey {
		t.Errorf("Authorization = %q, want %q", last.Auth, "Bearer "+testAPIKey)
	}
	req := decodeRequest(t, rec)
	if req.Model != testModel {
		t.Errorf("model = %q, want %q", req.Model, testModel)
	}
	wantMessages := []wireMessage{
		{Role: RoleSystem, Content: system},
		{Role: RoleUser, Content: "Refine the login user story."},
		{Role: RoleAssistant, Content: "I reviewed the story."},
		{Role: RoleUser, Content: "Generate the implementation tickets."},
	}
	if !reflect.DeepEqual(req.Messages, wantMessages) {
		t.Errorf("messages = %+v, want %+v", req.Messages, wantMessages)
	}
	if req.ResponseFormat != nil {
		t.Errorf("response_format = %+v, want none in plain chat", req.ResponseFormat)
	}

	// The response is mapped and the token usage logged.
	want := Response{
		Content:      "Here is the refined story.",
		Model:        testModel,
		Usage:        Usage{InputTokens: 42, OutputTokens: 7},
		FinishReason: "stop",
	}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}
	for _, wantLog := range []string{
		"llm chat completion",
		"model=" + testModel,
		"input_tokens=42",
		"output_tokens=7",
	} {
		if !strings.Contains(logs.String(), wantLog) {
			t.Errorf("usage log missing %q in:\n%s", wantLog, logs.String())
		}
	}
}

func TestChatJSON(t *testing.T) {
	const reply = `{"tickets":[{"title":"Add login form"}]}`
	t.Run("asks for json and parses the reply", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			completion(w, testModel, reply, 10, 20)
		})

		var out struct {
			Tickets []struct {
				Title string `json:"title"`
			} `json:"tickets"`
		}
		resp, err := c.ChatJSON(context.Background(),
			"You refine user stories into tickets.", chatHistory(), &out)
		if err != nil {
			t.Fatalf("ChatJSON: %v", err)
		}
		if len(out.Tickets) != 1 || out.Tickets[0].Title != "Add login form" {
			t.Errorf("out = %+v, want the scripted ticket", out)
		}
		if resp.Content != reply {
			t.Errorf("Content = %q, want the raw JSON string", resp.Content)
		}
		if resp.Usage != (Usage{InputTokens: 10, OutputTokens: 20}) {
			t.Errorf("Usage = %+v, want the reported tokens", resp.Usage)
		}

		req := decodeRequest(t, rec)
		if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
			t.Errorf("response_format = %+v, want json_object", req.ResponseFormat)
		}
	})

	t.Run("appends the json instruction when the prompt does not ask for json", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			completion(w, testModel, reply, 1, 2)
		})
		if _, err := c.ChatJSON(context.Background(), "Refine stories into tickets.", chatHistory(), &struct{}{}); err != nil {
			t.Fatalf("ChatJSON: %v", err)
		}
		req := decodeRequest(t, rec)
		want := "Refine stories into tickets.\n\nRespond with a single JSON object."
		if len(req.Messages) == 0 || req.Messages[0].Content != want {
			t.Errorf("system = %+v, want %q", req.Messages, want)
		}
	})

	t.Run("keeps the system prompt when it already mentions json", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			completion(w, testModel, reply, 1, 2)
		})
		system := "Answer as JSON with the tickets."
		if _, err := c.ChatJSON(context.Background(), system, chatHistory(), &struct{}{}); err != nil {
			t.Fatalf("ChatJSON: %v", err)
		}
		req := decodeRequest(t, rec)
		if len(req.Messages) == 0 || req.Messages[0].Content != system {
			t.Errorf("system = %+v, want %q unchanged", req.Messages, system)
		}
	})

	t.Run("empty system prompt sends only the json instruction", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			completion(w, testModel, reply, 1, 2)
		})
		if _, err := c.ChatJSON(context.Background(), "", chatHistory(), &struct{}{}); err != nil {
			t.Fatalf("ChatJSON: %v", err)
		}
		req := decodeRequest(t, rec)
		if len(req.Messages) == 0 || req.Messages[0].Content != "Respond with a single JSON object." {
			t.Errorf("system = %+v, want the instruction alone", req.Messages)
		}
	})
}

func TestChatJSONParsesRobustReplies(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "plain object", content: `{"tickets":[]}`},
		{name: "fenced json block", content: "```json\n{\"tickets\":[]}\n```"},
		{name: "fenced block without language", content: "```\n{\"tickets\":[]}\n```"},
		{name: "prose around the object", content: "Sure, here you go:\n{\"tickets\":[]}\nHope that helps."},
		{name: "prose without any object", content: "I cannot produce that.", wantErr: true},
		{name: "empty reply", content: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
				completion(w, testModel, tt.content, 5, 9)
			})
			var out map[string]any
			resp, err := c.ChatJSON(context.Background(), "Answer as JSON.", chatHistory(), &out)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidJSON) {
					t.Fatalf("error = %v, want ErrInvalidJSON", err)
				}
				// The tokens were spent either way: usage is
				// reported alongside the parse error.
				if resp.Usage != (Usage{InputTokens: 5, OutputTokens: 9}) {
					t.Errorf("Usage = %+v, want the reported tokens", resp.Usage)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChatJSON: %v", err)
			}
			if _, ok := out["tickets"]; !ok {
				t.Errorf("out = %+v, want the tickets object parsed", out)
			}
		})
	}
}

func TestChatJSONNilOut(t *testing.T) {
	c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
		completion(w, testModel, "{}", 1, 1)
	})
	_, err := c.ChatJSON(context.Background(), "Answer as JSON.", chatHistory(), nil)
	if err == nil || !strings.Contains(err.Error(), "out must be a non-nil pointer") {
		t.Errorf("error = %v, want the nil out rejection", err)
	}
	if rec.count() != 0 {
		t.Errorf("requests = %d, want 0: nil out must fail before the API call", rec.count())
	}
}

func TestChatValidatesHistory(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		wantErr  string
	}{
		{name: "no messages", messages: nil, wantErr: "at least one message is required"},
		{
			name:     "invalid role",
			messages: []Message{{Role: "tool", Content: "hi"}},
			wantErr:  `message 0 has invalid role "tool": want "user" or "assistant"`,
		},
		{
			name:     "system role in history",
			messages: []Message{{Role: RoleSystem, Content: "hi"}},
			wantErr:  `message 0 has invalid role "system": want "user" or "assistant"`,
		},
		{name: "empty content", messages: []Message{{Role: RoleUser, Content: "  "}}, wantErr: "message 0 is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
				t.Error("server must not be called with an invalid history")
			})
			if _, err := c.Chat(context.Background(), "system", tt.messages); err == nil ||
				!strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Chat error = %v, want %q", err, tt.wantErr)
			}
			// ChatJSON shares the validation.
			if _, err := c.ChatJSON(context.Background(), "system", tt.messages, &struct{}{}); err == nil ||
				!strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ChatJSON error = %v, want %q", err, tt.wantErr)
			}
			if rec.count() != 0 {
				t.Errorf("requests = %d, want 0", rec.count())
			}
		})
	}
}

func TestChatAPIError(t *testing.T) {
	t.Run("non-retryable status maps to APIError", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			if attempt == 1 {
				respondJSON(w, http.StatusUnauthorized, apiErrorBody("Incorrect API key provided"))
				return
			}
			t.Errorf("server saw %d attempts, want 1: client errors must not be retried", attempt)
		})
		waits := recordSleep(c)

		_, err := c.Chat(context.Background(), "system", chatHistory())
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *APIError", err)
		}
		want := &APIError{StatusCode: 401, Status: "401 Unauthorized", Message: "Incorrect API key provided"}
		if *apiErr != *want {
			t.Errorf("APIError = %+v, want %+v", *apiErr, *want)
		}
		if rec.count() != 1 || len(*waits) != 0 {
			t.Errorf("requests = %d, waits = %v, want 1 request and no waits", rec.count(), *waits)
		}
	})

	t.Run("flat error body is read too", func(t *testing.T) {
		c, _ := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusBadRequest, map[string]any{
				"message": "model not found",
				"type":    "invalid_request_error",
			})
		})
		_, err := c.Chat(context.Background(), "system", chatHistory())
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Message != "model not found" {
			t.Errorf("error = %v, want APIError with the flat message", err)
		}
	})

	t.Run("error body without a message keeps the status", func(t *testing.T) {
		c, _ := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusPaymentRequired, map[string]any{})
		})
		_, err := c.Chat(context.Background(), "system", chatHistory())
		if err == nil || !strings.Contains(err.Error(), "402") {
			t.Errorf("error = %v, want the status in the message", err)
		}
	})
}

func TestChatRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name string
		// handle answers the attempts; 1-based.
		handle       func(attempt int, w http.ResponseWriter, r *http.Request)
		wantContent  string
		wantAPIErr   *APIError // compared when non-nil
		wantRequests int
		wantWaits    []time.Duration
	}{
		{
			name: "rate limit honors retry-after",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				if attempt == 1 {
					w.Header().Set("Retry-After", "2")
					respondJSON(w, http.StatusTooManyRequests, apiErrorBody("rate limit"))
					return
				}
				completion(w, testModel, "recovered", 1, 2)
			},
			wantContent:  "recovered",
			wantRequests: 2,
			wantWaits:    []time.Duration{2 * time.Second},
		},
		{
			name: "rate limit without retry-after uses the backoff",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				if attempt == 1 {
					respondJSON(w, http.StatusTooManyRequests, apiErrorBody("rate limit"))
					return
				}
				completion(w, testModel, "recovered", 1, 2)
			},
			wantContent:  "recovered",
			wantRequests: 2,
			wantWaits:    []time.Duration{defaultInitialBackoff},
		},
		{
			name: "server error then success",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				if attempt == 1 {
					respondJSON(w, http.StatusInternalServerError, apiErrorBody("boom"))
					return
				}
				completion(w, testModel, "recovered", 1, 2)
			},
			wantContent:  "recovered",
			wantRequests: 2,
			wantWaits:    []time.Duration{defaultInitialBackoff},
		},
		{
			name: "transport error then success",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				if attempt == 1 {
					// Abort the connection: the client sees a
					// transport error, not a response.
					panic(http.ErrAbortHandler)
				}
				completion(w, testModel, "recovered", 1, 2)
			},
			wantContent:  "recovered",
			wantRequests: 2,
			wantWaits:    []time.Duration{defaultInitialBackoff},
		},
		{
			name: "retries exhausted double the backoff each attempt",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				respondJSON(w, http.StatusServiceUnavailable, apiErrorBody("overloaded"))
			},
			wantAPIErr:   &APIError{StatusCode: 503, Status: "503 Service Unavailable", Message: "overloaded"},
			wantRequests: defaultMaxAttempts,
			wantWaits:    []time.Duration{defaultInitialBackoff, 2 * defaultInitialBackoff},
		},
		{
			name: "long retry-after fails fast instead of sleeping",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3600")
				respondJSON(w, http.StatusTooManyRequests, apiErrorBody("rate limit"))
			},
			wantAPIErr:   &APIError{StatusCode: 429, Status: "429 Too Many Requests", Message: "rate limit", RetryAfter: time.Hour},
			wantRequests: 1,
			wantWaits:    nil,
		},
		{
			name: "client error does not retry",
			handle: func(attempt int, w http.ResponseWriter, r *http.Request) {
				respondJSON(w, http.StatusBadRequest, apiErrorBody("bad request"))
			},
			wantAPIErr:   &APIError{StatusCode: 400, Status: "400 Bad Request", Message: "bad request"},
			wantRequests: 1,
			wantWaits:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newTestClient(t, tt.handle)
			waits := recordSleep(c)
			logs := captureLogs(c)

			resp, err := c.Chat(context.Background(), "system", chatHistory())

			if tt.wantAPIErr != nil {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("error = %v, want *APIError", err)
				}
				if *apiErr != *tt.wantAPIErr {
					t.Errorf("APIError = %+v, want %+v", *apiErr, *tt.wantAPIErr)
				}
				if resp != (Response{}) {
					t.Errorf("response = %+v, want the zero response", resp)
				}
			} else if err != nil || resp.Content != tt.wantContent {
				t.Fatalf("Chat = (%+v, %v), want content %q", resp, err, tt.wantContent)
			}
			if rec.count() != tt.wantRequests {
				t.Errorf("requests = %d, want %d", rec.count(), tt.wantRequests)
			}
			if !reflect.DeepEqual(*waits, tt.wantWaits) {
				t.Errorf("waits = %v, want %v", *waits, tt.wantWaits)
			}
			// Attempts that were actually retried are visible in
			// the logs before the call gives up; fail-fast paths
			// log nothing.
			logsWantRetry := strings.Contains(logs.String(), "retrying")
			if tt.wantRequests > 1 && !logsWantRetry {
				t.Errorf("logs missing the retry notice:\n%s", logs.String())
			}
			if tt.wantRequests == 1 && logsWantRetry {
				t.Errorf("logs contain a retry notice on a fail-fast path:\n%s", logs.String())
			}
		})
	}
}

func TestChatRetryContextCanceled(t *testing.T) {
	t.Run("canceled before the call", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			// The request is answered; the error surfaces before
			// any retry either way.
			respondJSON(w, http.StatusTooManyRequests, apiErrorBody("rate limit"))
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Chat(ctx, "system", chatHistory())
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled in the chain", err)
		}
		if rec.count() > 1 {
			t.Errorf("requests = %d, want at most 1", rec.count())
		}
	})

	t.Run("canceled while waiting to retry", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusInternalServerError, apiErrorBody("boom"))
		})
		c.sleep = func(ctx context.Context, d time.Duration) error {
			return context.Canceled // the wait was cut short
		}

		_, err := c.Chat(context.Background(), "system", chatHistory())
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if rec.count() != 1 {
			t.Errorf("requests = %d, want 1: no retry after cancellation", rec.count())
		}
	})
}

// TestSleepContext covers the client's default sleep directly: the
// retry tests intercept it, so it needs its own coverage.
func TestSleepContext(t *testing.T) {
	t.Run("waits for the duration", func(t *testing.T) {
		start := time.Now()
		if err := sleepContext(context.Background(), 5*time.Millisecond); err != nil {
			t.Fatalf("sleepContext: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
			t.Errorf("elapsed = %v, want at least the duration", elapsed)
		}
	})

	t.Run("returns when the context is done", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})
}

func TestChatMalformedSuccessResponses(t *testing.T) {
	// A malformed 2xx body is retried like a transient failure (a
	// flaky proxy can garble one response of several), so these tests
	// intercept the backoff.
	t.Run("no choices", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, map[string]any{"id": "cmpl", "object": "chat.completion", "choices": nil})
		})
		recordSleep(c)
		_, err := c.Chat(context.Background(), "system", chatHistory())
		if err == nil || !strings.Contains(err.Error(), "no choices") {
			t.Errorf("error = %v, want the missing choices rejection", err)
		}
		if rec.count() != defaultMaxAttempts {
			t.Errorf("requests = %d, want the retries exhausted", rec.count())
		}
	})

	t.Run("model falls back to the requested one", func(t *testing.T) {
		c, _ := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			completion(w, "", "hi", 1, 1) // provider does not echo a model
		})
		resp, err := c.Chat(context.Background(), "system", chatHistory())
		if err != nil {
			t.Fatal(err)
		}
		if resp.Model != testModel {
			t.Errorf("model = %q, want the requested %q", resp.Model, testModel)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		c, rec := newTestClient(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		})
		recordSleep(c)
		_, err := c.Chat(context.Background(), "system", chatHistory())
		if err == nil || !strings.Contains(err.Error(), "decode chat completions response") {
			t.Errorf("error = %v, want the decode failure", err)
		}
		if rec.count() != defaultMaxAttempts {
			t.Errorf("requests = %d, want the retries exhausted", rec.count())
		}
	})
}

// TestClientIsMockable demonstrates what Temporal activity code does:
// depend on the Client interface, so tests substitute a Fake without
// real API calls.
func TestClientIsMockable(t *testing.T) {
	ask := func(ctx context.Context, c Client, story string) (string, error) {
		resp, err := c.Chat(ctx, "Refine user stories.", []Message{{Role: RoleUser, Content: story}})
		if err != nil {
			return "", fmt.Errorf("ask the llm: %w", err)
		}
		return resp.Content, nil
	}

	answer, err := ask(context.Background(), NewFake(Response{Content: "refined!"}), "the login story")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if answer != "refined!" {
		t.Errorf("answer = %q, want the fake's scripted reply", answer)
	}

	_, err = ask(context.Background(), failingFake(), "the login story")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the fake's injected error", err)
	}
}

// failingFake returns a Fake whose every call fails, for the error
// path of the mockability demonstration.
func failingFake() *Fake {
	f := NewFake(Response{Content: "x"})
	f.Err = errors.New("boom")
	return f
}
