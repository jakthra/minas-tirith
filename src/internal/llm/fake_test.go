package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

var _ Client = (*Fake)(nil)

func TestFakeReplaysScriptedResponses(t *testing.T) {
	first := Response{Content: "first", Model: "test-model", Usage: Usage{InputTokens: 1, OutputTokens: 1}, FinishReason: "stop"}
	second := Response{Content: "second", Model: "test-model", Usage: Usage{InputTokens: 2, OutputTokens: 2}, FinishReason: "stop"}
	f := NewFake(first, second)
	history := []Message{{Role: RoleUser, Content: "Refine the story."}}

	got1, err := f.Chat(context.Background(), "system one", history)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got1 != first {
		t.Errorf("first response = %+v, want %+v", got1, first)
	}
	got2, err := f.Chat(context.Background(), "system two", history)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got2 != second {
		t.Errorf("second response = %+v, want %+v", got2, second)
	}

	// The conversations are recorded, prompts and all, so tests can
	// assert what the agent asked.
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	want := Conversation{
		System:   "system one",
		Messages: []Message{{Role: RoleUser, Content: "Refine the story."}},
		JSON:     false,
	}
	if !reflect.DeepEqual(calls[0], want) {
		t.Errorf("call 0 = %+v, want %+v", calls[0], want)
	}
	if calls[1].System != "system two" {
		t.Errorf("call 1 system = %q, want %q", calls[1].System, "system two")
	}
}

func TestFakeChatJSON(t *testing.T) {
	const reply = `{"tickets":[{"title":"Add login form"}]}`
	f := NewFake(Response{Content: reply, Model: "test-model", Usage: Usage{InputTokens: 3, OutputTokens: 5}})

	var out struct {
		Tickets []struct {
			Title string `json:"title"`
		} `json:"tickets"`
	}
	resp, err := f.ChatJSON(context.Background(), "Generate tickets as JSON.",
		[]Message{{Role: RoleUser, Content: "The login story."}}, &out)
	if err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if len(out.Tickets) != 1 || out.Tickets[0].Title != "Add login form" {
		t.Errorf("out = %+v, want the scripted ticket", out)
	}
	if resp.Content != reply {
		t.Errorf("Content = %q, want the raw JSON string", resp.Content)
	}
	calls := f.Calls()
	if len(calls) != 1 || !calls[0].JSON {
		t.Errorf("calls = %+v, want the JSON mode recorded", calls)
	}
}

func TestFakeErrors(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: "Refine the story."}}

	t.Run("no scripted response left", func(t *testing.T) {
		f := NewFake(Response{Content: "only one"})
		if _, err := f.Chat(context.Background(), "", history); err != nil {
			t.Fatalf("first Chat: %v", err)
		}
		_, err := f.Chat(context.Background(), "", history)
		if err == nil || err.Error() != "fake llm: no scripted response left" {
			t.Errorf("error = %v, want the exhausted scripted responses", err)
		}
	})

	t.Run("injected error", func(t *testing.T) {
		f := NewFake(Response{Content: "unused"})
		f.Err = errors.New("boom")
		if _, err := f.Chat(context.Background(), "", history); err == nil || err.Error() != "boom" {
			t.Errorf("error = %v, want the injected error", err)
		}
	})

	t.Run("nil out", func(t *testing.T) {
		f := NewFake(Response{Content: "{}"})
		if _, err := f.ChatJSON(context.Background(), "", history, nil); err == nil {
			t.Error("ChatJSON with nil out accepted")
		}
	})

	t.Run("invalid scripted json", func(t *testing.T) {
		f := NewFake(Response{Content: "not json"})
		var out map[string]any
		_, err := f.ChatJSON(context.Background(), "", history, &out)
		if !errors.Is(err, ErrInvalidJSON) {
			t.Errorf("error = %v, want ErrInvalidJSON", err)
		}
	})
}

func TestFakeValidatesInputs(t *testing.T) {
	f := NewFake(Response{Content: "unused"})
	if _, err := f.Chat(context.Background(), "", nil); err == nil ||
		err.Error() != "at least one message is required" {
		t.Errorf("error = %v, want the empty history rejected", err)
	}
	if _, err := f.ChatJSON(context.Background(), "", []Message{{Role: "tool", Content: "hi"}}, &struct{}{}); err == nil ||
		err.Error() != `message 0 has invalid role "tool": want "user" or "assistant"` {
		t.Errorf("error = %v, want the invalid role rejected", err)
	}
	// Validation failures are not recorded as conversations.
	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("calls = %d, want 0: rejected chats are not recorded", len(calls))
	}
}
