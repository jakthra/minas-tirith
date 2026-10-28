package llm

import (
	"context"
	"errors"
	"sync"
)

// Fake is an in-memory Client for tests: Temporal activity tests (and
// any other test) substitute it to exercise agent logic without real
// LLM API calls.
//
// A Fake replays the scripted Responses in order, one per call, and
// records every conversation it was asked (Calls), so tests assert
// both the answers and the prompts that produced them. Inputs are
// validated exactly like ChatCompletionsClient, so tests exercise the
// same input contract as production. Calls fail when Err is set or the
// scripted responses run out: a Fake never invents an answer.
type Fake struct {
	// Err, when non-nil, is returned by every operation, for testing
	// error handling.
	Err error

	mu        sync.Mutex
	responses []Response
	calls     []Conversation
}

// Conversation is one chat a Fake was asked, as recorded by Calls.
type Conversation struct {
	// System is the system prompt of the chat ("" when none was
	// given).
	System string
	// Messages are the history messages of the chat.
	Messages []Message
	// JSON reports whether the chat asked for a JSON object reply.
	JSON bool
}

// NewFake returns a Fake answering with the scripted responses, in
// order, one per call.
func NewFake(responses ...Response) *Fake {
	return &Fake{responses: append([]Response(nil), responses...)}
}

// Calls returns the conversations the Fake was asked, oldest first.
func (f *Fake) Calls() []Conversation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Conversation, len(f.calls))
	copy(out, f.calls)
	return out
}

// Chat implements Client.
func (f *Fake) Chat(_ context.Context, system string, messages []Message) (Response, error) {
	return f.chat(system, messages, false)
}

// ChatJSON implements Client.
func (f *Fake) ChatJSON(_ context.Context, system string, messages []Message, out any) (Response, error) {
	if out == nil {
		return Response{}, errors.New("out must be a non-nil pointer")
	}
	resp, err := f.chat(system, messages, true)
	if err != nil {
		return Response{}, err
	}
	if err := parseJSONContent(resp.Content, out); err != nil {
		return resp, err
	}
	return resp, nil
}

// chat validates and records one conversation, then replays the next
// scripted response. The caller must handle the JSON-mode parsing.
func (f *Fake) chat(system string, messages []Message, jsonMode bool) (Response, error) {
	if err := validateHistory(messages); err != nil {
		return Response{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Conversation{
		System:   system,
		Messages: append([]Message(nil), messages...),
		JSON:     jsonMode,
	})
	if f.Err != nil {
		return Response{}, f.Err
	}
	if len(f.responses) == 0 {
		return Response{}, errors.New("fake llm: no scripted response left")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}
