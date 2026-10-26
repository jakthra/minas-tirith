package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"minas-tirith/internal/event"
	"minas-tirith/internal/source"
)

// newTestSource builds a webhook source with the forgejo adapter and a
// collecting dispatcher.
func newTestSource(t *testing.T, secret string, watched ...string) (*Source, *collector) {
	t.Helper()
	src := NewSource(Config{
		Secret:    secret,
		Watched:   watched,
		DedupeTTL: time.Hour,
	})
	src.RegisterProvider(NewForgejoAdapter("forgejo"))
	col := &collector{}
	routes, err := src.Start(context.Background(), col)
	if err != nil {
		t.Fatalf("start source: %v", err)
	}
	if len(routes) != 1 || routes[0].Pattern != RoutePattern {
		t.Fatalf("unexpected routes: %+v", routes)
	}
	return src, col
}

// collector is a test consumer that records dispatched events.
type collector struct {
	mu     sync.Mutex
	events []event.Event
}

func (c *collector) Dispatch(ctx context.Context, ev event.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *collector) snapshot() []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.Event(nil), c.events...)
}

// serve mounts the source's route on a ServeMux the way the server does and
// serves the request.
func serve(t *testing.T, src *Source, dispatch source.Dispatcher, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	routes, err := src.Start(context.Background(), dispatch)
	if err != nil {
		t.Fatalf("start source: %v", err)
	}
	mux := http.NewServeMux()
	for _, r := range routes {
		mux.Handle(r.Method+" "+r.Pattern, r.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDeliveryAcceptedAndDispatched(t *testing.T) {
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	body := loadFixture(t, "forgejo-push.json")
	req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "push", body, testSecret)

	rec := serve(t, src, col, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp DeliveryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Dispatched != 1 || resp.Duplicates != 0 {
		t.Errorf("response = %+v, want dispatched=1 duplicates=0", resp)
	}

	events := col.snapshot()
	if len(events) != 1 {
		t.Fatalf("consumer received %d events, want 1", len(events))
	}
	if events[0].Kind != event.KindCommit {
		t.Errorf("event kind = %q, want commit", events[0].Kind)
	}
}

func TestDuplicateDeliveryDispatchedOnce(t *testing.T) {
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	body := loadFixture(t, "forgejo-pr-comment.json")

	// Forgejo redeliveries: same payload, different delivery ID.
	first := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "issue_comment", body, testSecret)
	first.Header.Set("X-Forgejo-Delivery", "delivery-1")
	second := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "issue_comment", body, testSecret)
	second.Header.Set("X-Forgejo-Delivery", "delivery-2")

	for i, req := range []*http.Request{first, second} {
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i+1, rec.Code)
		}
	}

	events := col.snapshot()
	if len(events) != 1 {
		t.Fatalf("consumer received %d events after redelivery, want 1", len(events))
	}
}

func TestUnauthenticatedDeliveryRejected(t *testing.T) {
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	body := loadFixture(t, "forgejo-push.json")

	tests := []struct {
		name string
		mut  func(req *http.Request)
	}{
		{"no signature", func(req *http.Request) { req.Header.Del("X-Forgejo-Signature") }},
		{"wrong signature", func(req *http.Request) {
			req.Header.Set("X-Forgejo-Signature", signBody(t, "wrong-secret", body))
		}},
		{"garbage signature", func(req *http.Request) {
			req.Header.Set("X-Forgejo-Signature", "zz-not-hex")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "push", body, testSecret)
			tt.mut(req)
			rec := serve(t, src, col, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := len(col.snapshot()); got != 0 {
				t.Fatalf("consumer received %d events, want 0", got)
			}
		})
	}
}

func TestRouting(t *testing.T) {
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	body := loadFixture(t, "forgejo-push.json")

	t.Run("unwatched project is not found", func(t *testing.T) {
		req := signedRequest(t, "POST", "/v1/Other/webhooks/forgejo", "push", body, testSecret)
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("unknown provider is not found", func(t *testing.T) {
		req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/github", "push", body, testSecret)
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("GET is not allowed", func(t *testing.T) {
		req := signedRequest(t, "GET", "/v1/BeanieGenie/webhooks/forgejo", "push", body, testSecret)
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("wrong content type is unsupported", func(t *testing.T) {
		req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "push", body, testSecret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want 415", rec.Code)
		}
	})
}

func TestIgnoredEventStillSucceeds(t *testing.T) {
	// Forgejo redelivers anything that is not a 2xx, so deliveries the
	// listener does not care about must still return success.
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "repository", []byte(`{}`), testSecret)

	rec := serve(t, src, col, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp DeliveryResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Dispatched != 0 {
		t.Errorf("dispatched = %d, want 0", resp.Dispatched)
	}
	if got := len(col.snapshot()); got != 0 {
		t.Errorf("consumer received %d events, want 0", got)
	}
}

func TestMalformedPayloadRejected(t *testing.T) {
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "push", []byte(`{"commits": 3}`), testSecret)

	rec := serve(t, src, col, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestStartValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no secret", Config{Watched: []string{"BeanieGenie"}}},
		{"no watched repos", Config{Secret: "s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := NewSource(tt.cfg)
			src.RegisterProvider(NewForgejoAdapter("forgejo"))
			if _, err := src.Start(context.Background(), &collector{}); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}

	t.Run("no providers", func(t *testing.T) {
		src := NewSource(Config{Secret: "s", Watched: []string{"BeanieGenie"}})
		if _, err := src.Start(context.Background(), &collector{}); err == nil {
			t.Fatal("expected error when no provider is registered")
		}
	})
}

func TestDuplicateDetectionAcrossBodyCopies(t *testing.T) {
	// Same comment delivered with byte-different but semantically identical
	// payloads must still dedupe: the identity comes from the parsed event,
	// not the raw body.
	src, col := newTestSource(t, testSecret, "BeanieGenie")
	body := loadFixture(t, "forgejo-pr-comment.json")
	var pretty map[string]any
	if err := json.Unmarshal(body, &pretty); err != nil {
		t.Fatal(err)
	}
	reordered, _ := json.Marshal(pretty)

	for i, b := range [][]byte{body, reordered} {
		req := signedRequest(t, "POST", "/v1/BeanieGenie/webhooks/forgejo", "issue_comment", b, testSecret)
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d", i+1, rec.Code)
		}
	}
	if got := len(col.snapshot()); got != 1 {
		t.Errorf("consumer received %d events, want 1", got)
	}
}
