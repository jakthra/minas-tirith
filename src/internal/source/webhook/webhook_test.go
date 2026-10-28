package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"minas-tirith/internal/configstore"
	"minas-tirith/internal/event"
	"minas-tirith/internal/source"
)

// newTestSource builds a webhook source with the forgejo adapter and a
// collecting dispatcher. Every watched project shares the secret; tests
// that need per-repo secrets use newTestSourceRepos.
func newTestSource(t *testing.T, secret string, watched ...string) (*Source, *collector) {
	t.Helper()
	repos := make(configstore.Static, 0, len(watched))
	for _, p := range watched {
		repos = append(repos, configstore.Repository{Project: p, Secret: secret})
	}
	return newTestSourceRepos(t, repos...)
}

// newTestSourceRepos builds a webhook source watching repos, each with
// its own webhook secret.
func newTestSourceRepos(t *testing.T, repos ...configstore.Repository) (*Source, *collector) {
	t.Helper()
	src := NewSource(Config{
		Store:     configstore.Static(repos),
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

func TestPerRepoSecrets(t *testing.T) {
	// Two watched repositories, each with its own webhook secret: a
	// delivery is authenticated only by the secret of the project it is
	// addressed to.
	src, col := newTestSourceRepos(t,
		configstore.Repository{Project: "BeanieGenie", Secret: "beanie-secret"},
		configstore.Repository{Project: "Garden", Secret: "garden-secret"},
	)
	body := loadFixture(t, "forgejo-push.json")

	// The fixture's commit SHA is fixed, so each project gets its own
	// commit SHA to keep the two deliveries distinct events.
	gardenBody := commitWithSHA(t, body, "77f0de217a1823ebe9e4afffe7ff1dce0912f1c0")

	t.Run("accepted with its own secret", func(t *testing.T) {
		deliveries := []struct {
			project string
			body    []byte
			secret  string
		}{
			{"BeanieGenie", body, "beanie-secret"},
			{"Garden", gardenBody, "garden-secret"},
		}
		for _, d := range deliveries {
			req := signedRequest(t, "POST", "/v1/"+d.project+"/webhooks/forgejo", "push", d.body, d.secret)
			rec := serve(t, src, col, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200; body: %s", d.project, rec.Code, rec.Body.String())
			}
			var resp DeliveryResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("%s: decode response: %v", d.project, err)
			}
			if resp.Dispatched != 1 || resp.Duplicates != 0 {
				t.Errorf("%s: response = %+v, want dispatched=1 duplicates=0", d.project, resp)
			}
		}
		if got := len(col.snapshot()); got != 2 {
			t.Errorf("consumer received %d events, want 2", got)
		}
	})

	t.Run("rejected with another repo's secret", func(t *testing.T) {
		req := signedRequest(t, "POST", "/v1/Garden/webhooks/forgejo", "push", gardenBody, "beanie-secret")
		rec := serve(t, src, col, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if got := len(col.snapshot()); got != 2 {
			t.Errorf("consumer received %d events, want the 2 accepted before", got)
		}
	})
}

// commitWithSHA returns body with every commit SHA (and the after SHA)
// replaced, yielding a distinct but otherwise identical delivery.
func commitWithSHA(t *testing.T, body []byte, sha string) []byte {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	p["after"] = sha
	commits, ok := p["commits"].([]any)
	if !ok {
		t.Fatal("push fixture has no commits")
	}
	for _, c := range commits {
		if m, ok := c.(map[string]any); ok {
			m["id"] = sha
		}
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStartValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no store", Config{}},
		{"store error", Config{Store: errStore{}}},
		{"no watched repos", Config{Store: configstore.Static{}}},
		{"repo without a project", Config{Store: configstore.Static{{Secret: "s"}}}},
		{"repo without a secret", Config{Store: configstore.Static{{Project: "BeanieGenie"}}}},
		{"duplicate repos", Config{Store: configstore.Static{
			{Project: "BeanieGenie", Secret: "a"},
			{Project: "BeanieGenie", Secret: "b"},
		}}},
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
		src := NewSource(Config{Store: configstore.Static{{Project: "BeanieGenie", Secret: "s"}}})
		if _, err := src.Start(context.Background(), &collector{}); err == nil {
			t.Fatal("expected error when no provider is registered")
		}
	})
}

// errStore is a Store whose declarative source fails to load.
type errStore struct{}

func (errStore) Repositories() ([]configstore.Repository, error) {
	return nil, errors.New("store unavailable")
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
