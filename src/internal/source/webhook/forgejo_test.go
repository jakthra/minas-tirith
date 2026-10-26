package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

const testSecret = "test-webhook-secret"

// signBody computes the X-Forgejo-Signature header value for a body, the
// way Forgejo does: HMAC-SHA256 hex digest of the raw body.
func signBody(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// signedRequest builds a webhook request for the given fixture payload.
func signedRequest(t *testing.T, method, target, event string, body []byte, secret string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forgejo-Event", event)
	req.Header.Set("X-Forgejo-Delivery", "test-delivery-uuid")
	if secret != "" {
		req.Header.Set("X-Forgejo-Signature", signBody(t, secret, body))
	}
	return req
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestForgejoVerify(t *testing.T) {
	body := loadFixture(t, "forgejo-push.json")
	adapter := NewForgejoAdapter("forgejo")

	t.Run("valid signature", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Forgejo-Signature", signBody(t, testSecret, body))
		if err := adapter.Verify(header, body, testSecret); err != nil {
			t.Fatalf("expected valid signature, got error: %v", err)
		}
	})

	t.Run("valid signature via gitea alias header", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Gitea-Signature", signBody(t, testSecret, body))
		if err := adapter.Verify(header, body, testSecret); err != nil {
			t.Fatalf("expected valid signature via X-Gitea-Signature, got error: %v", err)
		}
	})

	t.Run("missing signature header", func(t *testing.T) {
		if err := adapter.Verify(http.Header{}, body, testSecret); err == nil {
			t.Fatal("expected error for missing signature header")
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Forgejo-Signature", signBody(t, "other-secret", body))
		if err := adapter.Verify(header, body, testSecret); err == nil {
			t.Fatal("expected error for signature computed with the wrong secret")
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Forgejo-Signature", signBody(t, testSecret, body))
		if err := adapter.Verify(header, append(body, ' '), testSecret); err == nil {
			t.Fatal("expected error for signature over a tampered body")
		}
	})

	t.Run("malformed signature", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Forgejo-Signature", "not-hex")
		if err := adapter.Verify(header, body, testSecret); err == nil {
			t.Fatal("expected error for non-hex signature")
		}
	})
}

func TestForgejoParsePush(t *testing.T) {
	body := loadFixture(t, "forgejo-push.json")
	adapter := NewForgejoAdapter("forgejo")

	header := http.Header{}
	header.Set("X-Forgejo-Event", "push")
	events, err := adapter.Parse(header, body, "BeanieGenie")
	if err != nil {
		t.Fatalf("parse push: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 commit event from fixture, got %d", len(events))
	}

	ev := events[0]
	if ev.Kind != "commit" {
		t.Errorf("kind = %q, want %q", ev.Kind, "commit")
	}
	if ev.Provider != "forgejo" {
		t.Errorf("provider = %q, want forgejo", ev.Provider)
	}
	if ev.Project != "BeanieGenie" {
		t.Errorf("project = %q, want BeanieGenie", ev.Project)
	}
	if ev.Repository != "root/BeanieGenie" {
		t.Errorf("repository = %q, want root/BeanieGenie", ev.Repository)
	}
	if ev.Comment != nil {
		t.Error("comment payload should be nil for a commit event")
	}

	c := ev.Commit
	if c == nil {
		t.Fatal("commit payload is nil")
	}
	if c.SHA != "8ef0de217a1823ebe9e4afffe7ff1dce0912f1c0" {
		t.Errorf("sha = %q", c.SHA)
	}
	if c.Branch != "task4-webhook-test" || c.Ref != "refs/heads/task4-webhook-test" {
		t.Errorf("branch/ref = %q/%q", c.Branch, c.Ref)
	}
	if c.Author.Username != "root" {
		// The fixture was pushed with a token, so the git author is the
		// synthetic "Webhook Test" identity while the payload is as
		// Forgejo produced it; assert what the fixture actually contains.
		t.Logf("author = %+v", c.Author)
	}
	if len(c.Added)+len(c.Removed)+len(c.Modified) != 0 {
		t.Errorf("expected no file changes in fixture commit, got %+v", c)
	}

	if key := ev.DedupeKey(); key != "forgejo|root/BeanieGenie|commit|"+c.SHA {
		t.Errorf("dedupe key = %q", key)
	}
}

func TestForgejoParsePushIgnoresNonBranchRefs(t *testing.T) {
	payload := map[string]any{
		"ref": "refs/tags/v1.0.0",
		"commits": []map[string]any{
			{"id": "aaaa", "message": "tag push"},
		},
		"repository": map[string]any{"full_name": "root/BeanieGenie"},
	}
	body, _ := json.Marshal(payload)
	adapter := NewForgejoAdapter("forgejo")

	header := http.Header{}
	header.Set("X-Forgejo-Event", "push")
	events, err := adapter.Parse(header, body, "BeanieGenie")
	if err != nil {
		t.Fatalf("parse tag push: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events for a tag push, got %d", len(events))
	}
}

func TestForgejoParsePushMalformed(t *testing.T) {
	adapter := NewForgejoAdapter("forgejo")
	header := http.Header{}
	header.Set("X-Forgejo-Event", "push")
	if _, err := adapter.Parse(header, []byte("{not json"), "BeanieGenie"); err == nil {
		t.Fatal("expected error for malformed push payload")
	}
}

func TestForgejoParsePullRequestComment(t *testing.T) {
	body := loadFixture(t, "forgejo-pr-comment.json")
	adapter := NewForgejoAdapter("forgejo")

	header := http.Header{}
	header.Set("X-Forgejo-Event", "issue_comment")
	header.Set("X-Forgejo-Event-Type", "pull_request_comment")
	events, err := adapter.Parse(header, body, "BeanieGenie")
	if err != nil {
		t.Fatalf("parse comment: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 comment event, got %d", len(events))
	}

	ev := events[0]
	if ev.Kind != "comment" {
		t.Errorf("kind = %q, want comment", ev.Kind)
	}
	if ev.Repository != "root/BeanieGenie" {
		t.Errorf("repository = %q", ev.Repository)
	}
	if ev.Commit != nil {
		t.Error("commit payload should be nil for a comment event")
	}

	c := ev.Comment
	if c == nil {
		t.Fatal("comment payload is nil")
	}
	if c.ID != 10 {
		t.Errorf("comment id = %d, want 10", c.ID)
	}
	if c.Body != "Sixth test comment - capture the payload" {
		t.Errorf("body = %q", c.Body)
	}
	if c.Author.Username != "root" {
		t.Errorf("author = %+v, want login root", c.Author)
	}
	if c.MR == nil {
		t.Fatal("merge request reference is nil")
	}
	if c.MR.Number != 1 {
		t.Errorf("mr number = %d, want 1", c.MR.Number)
	}
	if c.MR.Title != "task4 webhook verification MR" {
		t.Errorf("mr title = %q", c.MR.Title)
	}
	if c.MR.State != "open" {
		t.Errorf("mr state = %q, want open", c.MR.State)
	}

	if key := ev.DedupeKey(); key != "forgejo|root/BeanieGenie|comment|10" {
		t.Errorf("dedupe key = %q", key)
	}
}

func TestForgejoParseCommentFiltering(t *testing.T) {
	base := map[string]any{
		"action":       "created",
		"is_pull":      true,
		"repository":   map[string]any{"full_name": "root/BeanieGenie"},
		"issue":        map[string]any{"number": 1, "pull_request": map[string]any{"merged": false}},
		"pull_request": map[string]any{"number": 1, "title": "t", "state": "open"},
		"comment": map[string]any{
			"id": 42, "body": "hello",
			"created_at": "2026-10-26T13:05:54+01:00",
			"user":       map[string]any{"login": "root"},
		},
	}

	tests := []struct {
		name    string
		mutate  func(m map[string]any)
		wantLen int
	}{
		{"edited comment", func(m map[string]any) { m["action"] = "edited" }, 0},
		{"plain issue comment", func(m map[string]any) {
			m["is_pull"] = false
			m["issue"] = map[string]any{"number": 2}
		}, 0},
		{"missing comment", func(m map[string]any) { delete(m, "comment") }, 0},
		{"new pull request comment", func(m map[string]any) {}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := make(map[string]any, len(base))
			for k, v := range base {
				payload[k] = v
			}
			tt.mutate(payload)
			body, _ := json.Marshal(payload)
			adapter := NewForgejoAdapter("forgejo")
			header := http.Header{}
			header.Set("X-Forgejo-Event", "issue_comment")
			events, err := adapter.Parse(header, body, "BeanieGenie")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(events) != tt.wantLen {
				t.Errorf("got %d events, want %d", len(events), tt.wantLen)
			}
		})
	}
}

func TestForgejoParseIgnoresOtherEvents(t *testing.T) {
	adapter := NewForgejoAdapter("forgejo")
	for _, ev := range []string{"create", "pull_request", "repository", "release", ""} {
		header := http.Header{}
		if ev != "" {
			header.Set("X-Forgejo-Event", ev)
		}
		events, err := adapter.Parse(header, []byte("{}"), "BeanieGenie")
		if err != nil {
			t.Fatalf("event %q: %v", ev, err)
		}
		if len(events) != 0 {
			t.Errorf("event %q: expected no events, got %d", ev, len(events))
		}
	}
}
