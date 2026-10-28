package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"minas-tirith/internal/config"
	"minas-tirith/internal/configstore"
	"minas-tirith/internal/event"
)

const serverTestSecret = "server-test-secret"

func newTestServer(t *testing.T) (*Server, *recorder) {
	t.Helper()
	srv, err := New(context.Background(), &config.Config{
		ListenAddr: ":0",
		Store:      configstore.Static{{Project: "BeanieGenie", Secret: serverTestSecret}},
		DedupeTTL:  time.Hour,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rec := &recorder{}
	srv.Subscribe(rec.record)
	return srv, rec
}

// recorder is a subscribed consumer for tests.
type recorder struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *recorder) record(ctx context.Context, ev event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) snapshot() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.Event(nil), r.events...)
}

func sign(t *testing.T, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(serverTestSecret))
	mac.Write(body)
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func post(t *testing.T, h http.Handler, target, forgejoEvent string, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forgejo-Event", forgejoEvent)
	if signature != "" {
		req.Header.Set("X-Forgejo-Signature", signature)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServerCommitEventEndToEnd(t *testing.T) {
	srv, rec := newTestServer(t)
	body := readFixture(t, "forgejo-push.json")

	resp := post(t, srv.Handler(), "/v1/BeanieGenie/webhooks/forgejo", "push", body, sign(t, body))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.Code, resp.Body.String())
	}

	// The subscribed consumer receives the commit event (async dispatch).
	waitFor(t, 2*time.Second, func() bool { return len(rec.snapshot()) == 1 })
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("consumer received %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != event.KindCommit {
		t.Errorf("kind = %q, want commit", ev.Kind)
	}
	if ev.Commit == nil {
		t.Fatal("commit payload missing")
	}
	if ev.Project != "BeanieGenie" {
		t.Errorf("project = %q, want BeanieGenie", ev.Project)
	}
}

func TestServerCommentEventEndToEnd(t *testing.T) {
	srv, rec := newTestServer(t)
	body := readFixture(t, "forgejo-pr-comment.json")

	resp := post(t, srv.Handler(), "/v1/BeanieGenie/webhooks/forgejo", "issue_comment", body, sign(t, body))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.Code, resp.Body.String())
	}

	waitFor(t, 2*time.Second, func() bool { return len(rec.snapshot()) == 1 })
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("consumer received %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != event.KindComment {
		t.Errorf("kind = %q, want comment", ev.Kind)
	}
	if ev.Comment == nil || ev.Comment.MR == nil {
		t.Fatal("comment or merge request payload missing")
	}
	if ev.Comment.MR.Number != 1 {
		t.Errorf("mr number = %d, want 1", ev.Comment.MR.Number)
	}
}

func TestServerDuplicateDeliverySuppressed(t *testing.T) {
	srv, rec := newTestServer(t)
	body := readFixture(t, "forgejo-push.json")
	sig := sign(t, body)

	for i := 0; i < 3; i++ {
		resp := post(t, srv.Handler(), "/v1/BeanieGenie/webhooks/forgejo", "push", body, sig)
		if resp.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d", i+1, resp.Code)
		}
	}

	waitFor(t, 2*time.Second, func() bool { return len(rec.snapshot()) >= 1 })
	if got := len(rec.snapshot()); got != 1 {
		t.Errorf("consumer received %d events after 3 redeliveries, want 1", got)
	}
}

func TestServerUnauthenticatedRejected(t *testing.T) {
	srv, rec := newTestServer(t)
	body := readFixture(t, "forgejo-push.json")

	resp := post(t, srv.Handler(), "/v1/BeanieGenie/webhooks/forgejo", "push", body, "deadbeef")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("consumer received %d events, want 0", got)
	}
}

func TestServerHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	req, err := http.NewRequest(http.MethodGet, "/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// --- helpers ---

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "source", "webhook", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
