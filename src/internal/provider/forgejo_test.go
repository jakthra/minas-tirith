package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const testToken = "test-token"

// recordedRequest is one request the test server received.
type recordedRequest struct {
	Method      string
	Path        string // URL path, e.g. /api/v1/repos/root/test/contents/README.md
	EscapedPath string // URL path as sent, e.g. /api/v1/repos/root/test/contents/my%20file.md
	Query       string // raw query string
	Auth        string // Authorization header
	Body        string // raw request body, empty for requests without one
}

// requests records what the test server received, safe for the server
// goroutine and the test goroutine.
type requests struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (rs *requests) add(r *http.Request, body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.reqs = append(rs.reqs, recordedRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		EscapedPath: r.URL.EscapedPath(),
		Query:       r.URL.RawQuery,
		Auth:        r.Header.Get("Authorization"),
		Body:        body,
	})
}

func (rs *requests) all() []recordedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]recordedRequest, len(rs.reqs))
	copy(out, rs.reqs)
	return out
}

// last returns the most recent recorded request.
func (rs *requests) last() recordedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.reqs[len(rs.reqs)-1]
}

// newTestForgejo returns a client against a test server that records
// every request before dispatching to handle.
func newTestForgejo(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) (*ForgejoClient, *requests) {
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
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := NewForgejoClient(srv.URL, testToken)
	if err != nil {
		t.Fatalf("NewForgejoClient: %v", err)
	}
	return c, rec
}

// respondJSON writes a status with a JSON body.
func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// forgejoError is a Forgejo error payload, as the API answers on
// failure: {"message": "...", "url": ..., "errors": ...}.
func forgejoError(message string) map[string]any {
	return map[string]any{"message": message, "url": "http://test/api/swagger", "errors": []any{}}
}

// fileContents is a Forgejo contents response for one file, matching
// the shape captured from the live instance.
func fileContents(path, content string, sha string, size int64) map[string]any {
	return map[string]any{
		"name":     path[strings.LastIndex(path, "/")+1:],
		"path":     path,
		"sha":      sha,
		"type":     "file",
		"size":     size,
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString([]byte(content)),
	}
}

func TestNewForgejoClient(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		c, err := NewForgejoClient("http://localhost:3000/", "token")
		if err != nil {
			t.Fatalf("NewForgejoClient: %v", err)
		}
		if c.baseURL != "http://localhost:3000" {
			t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
		}
	})
	t.Run("empty url", func(t *testing.T) {
		if _, err := NewForgejoClient("", "token"); err == nil {
			t.Error("empty url accepted")
		}
	})
	t.Run("invalid url", func(t *testing.T) {
		if _, err := NewForgejoClient("://nope", "token"); err == nil {
			t.Error("invalid url accepted")
		}
	})
	t.Run("empty token", func(t *testing.T) {
		if _, err := NewForgejoClient("http://localhost:3000", " "); err == nil {
			t.Error("empty token accepted")
		}
	})
}

func TestForgejoClientGetFile(t *testing.T) {
	c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/root/test/contents/backlog/tasks/task-1.md" {
			respondJSON(w, http.StatusOK, fileContents("backlog/tasks/task-1.md", "# Task 1\n", "blobsha1", 9))
			return
		}
		respondJSON(w, http.StatusNotFound, forgejoError("The target couldn't be found."))
	})

	file, err := c.GetFile(context.Background(), "root/test", "backlog/tasks/task-1.md", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if want := "# Task 1\n"; string(file.Content) != want {
		t.Errorf("content = %q, want %q", file.Content, want)
	}
	if file.SHA != "blobsha1" || file.Size != 9 || file.Path != "backlog/tasks/task-1.md" {
		t.Errorf("file = %+v, want sha blobsha1, size 9, full path", file)
	}

	got := rec.last()
	if got.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.Method)
	}
	if got.Auth != "token "+testToken {
		t.Errorf("auth = %q, want %q", got.Auth, "token "+testToken)
	}
	if got.Query != "ref=main" {
		t.Errorf("query = %q, want ref=main", got.Query)
	}

	t.Run("missing file", func(t *testing.T) {
		_, err := c.GetFile(context.Background(), "root/test", "nope.md", "main")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
	t.Run("missing repo", func(t *testing.T) {
		_, err := c.GetFile(context.Background(), "root/missing", "nope.md", "main")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
	t.Run("default ref omits query", func(t *testing.T) {
		c.GetFile(context.Background(), "root/test", "backlog/tasks/task-1.md", "")
		if got := rec.last(); got.Query != "" {
			t.Errorf("query = %q, want empty for default ref", got.Query)
		}
	})
	t.Run("directory is not a file", func(t *testing.T) {
		c2, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, map[string]any{"path": "backlog", "type": "dir", "sha": "x", "size": 0})
		})
		if _, err := c2.GetFile(context.Background(), "root/test", "backlog", "main"); err == nil {
			t.Error("directory accepted as file")
		}
	})
}

func TestForgejoClientListFiles(t *testing.T) {
	c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/root/test/contents/backlog":
			respondJSON(w, http.StatusOK, []map[string]any{
				{"path": "backlog/tasks", "type": "dir", "sha": "d1", "size": 0},
				{"path": "backlog/README.md", "type": "file", "sha": "f1", "size": 3},
			})
		case "/api/v1/repos/root/test/contents/backlog/tasks":
			respondJSON(w, http.StatusOK, []map[string]any{
				{"path": "backlog/tasks/task-1.md", "type": "file", "sha": "f2", "size": 9},
				{"path": "backlog/tasks/archive", "type": "dir", "sha": "d2", "size": 0},
			})
		case "/api/v1/repos/root/test/contents/backlog/tasks/archive":
			respondJSON(w, http.StatusOK, []map[string]any{
				{"path": "backlog/tasks/archive/old.md", "type": "file", "sha": "f3", "size": 6},
			})
		default:
			respondJSON(w, http.StatusNotFound, forgejoError("The target couldn't be found."))
		}
	})

	infos, err := c.ListFiles(context.Background(), "root/test", "backlog", "main")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	want := []FileInfo{
		{Path: "backlog/README.md", SHA: "f1", Size: 3},
		{Path: "backlog/tasks/archive/old.md", SHA: "f3", Size: 6},
		{Path: "backlog/tasks/task-1.md", SHA: "f2", Size: 9},
	}
	if !reflect.DeepEqual(infos, want) {
		t.Errorf("infos = %+v, want %+v", infos, want)
	}
	// Recursive listing: backlog, then its tasks subdir, then the archive.
	if got := len(rec.all()); got != 3 {
		t.Errorf("requests = %d, want 3 (root, tasks, archive)", got)
	}
	if got := rec.last(); got.Path != "/api/v1/repos/root/test/contents/backlog/tasks/archive" || got.Query != "ref=main" {
		t.Errorf("last request = %s?%s, want nested archive dir with ref", got.Path, got.Query)
	}

	t.Run("file path lists itself", func(t *testing.T) {
		c2, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, fileContents("README.md", "hi", "f1", 2))
		})
		infos, err := c2.ListFiles(context.Background(), "root/test", "README.md", "main")
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		if len(infos) != 1 || infos[0].Path != "README.md" {
			t.Errorf("infos = %+v, want the single file", infos)
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		if _, err := c.ListFiles(context.Background(), "root/test", "missing", "main"); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("empty path lists the repository root", func(t *testing.T) {
		c2, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/repos/root/test/contents" {
				respondJSON(w, http.StatusOK, []map[string]any{
					{"path": "README.md", "type": "file", "sha": "f1", "size": 3},
					{"path": "backlog", "type": "dir", "sha": "d1", "size": 0},
				})
				return
			}
			if r.URL.Path == "/api/v1/repos/root/test/contents/backlog" {
				respondJSON(w, http.StatusOK, []map[string]any{
					{"path": "backlog/story.md", "type": "file", "sha": "f2", "size": 9},
				})
				return
			}
			t.Errorf("unexpected request %s", r.URL.Path)
			respondJSON(w, http.StatusNotFound, forgejoError("unexpected"))
		})
		infos, err := c2.ListFiles(context.Background(), "root/test", "", "main")
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		want := []FileInfo{
			{Path: "README.md", SHA: "f1", Size: 3},
			{Path: "backlog/story.md", SHA: "f2", Size: 9},
		}
		if !reflect.DeepEqual(infos, want) {
			t.Errorf("infos = %+v, want %+v", infos, want)
		}
		if got := rec.all()[0].Path; got != "/api/v1/repos/root/test/contents" {
			t.Errorf("root request = %q, want contents without a path suffix", got)
		}
	})
}

func TestForgejoClientCreateBranch(t *testing.T) {
	c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusCreated, map[string]any{
			"name": "agent/story-1",
			"commit": map[string]any{
				"id":      "abc123",
				"message": "Initial commit\n",
			},
		})
	})

	branch, err := c.CreateBranch(context.Background(), "root/test", "agent/story-1", "main")
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if branch.Name != "agent/story-1" || branch.SHA != "abc123" {
		t.Errorf("branch = %+v, want name agent/story-1, sha abc123", branch)
	}

	got := rec.last()
	if got.Method != http.MethodPost || got.Path != "/api/v1/repos/root/test/branches" {
		t.Errorf("request = %s %s, want POST /api/v1/repos/root/test/branches", got.Method, got.Path)
	}
	var body struct {
		NewBranchName string `json:"new_branch_name"`
		OldRefName    string `json:"old_ref_name"`
	}
	if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.NewBranchName != "agent/story-1" || body.OldRefName != "main" {
		t.Errorf("body = %+v, want new branch from main", body)
	}
}

func TestForgejoClientCommit(t *testing.T) {
	var posted changeFilesRequest
	c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/root/test/contents/README.md":
			respondJSON(w, http.StatusOK, fileContents("README.md", "old", "oldblob", 3))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/root/test/contents/backlog/tasks/new.md":
			respondJSON(w, http.StatusNotFound, forgejoError("The target couldn't be found."))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/root/test/contents":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode commit request: %v", err)
			}
			respondJSON(w, http.StatusCreated, map[string]any{
				"commit": map[string]any{
					"sha":      "newcommit",
					"html_url": "http://test/root/test/commit/newcommit",
				},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			respondJSON(w, http.StatusNotFound, forgejoError("unexpected"))
		}
	})

	result, err := c.Commit(context.Background(), "root/test", "agent/story-1", "Add ticket", []FileChange{
		{Path: "README.md", Content: []byte("new")},
		{Path: "backlog/tasks/new.md", Content: []byte("# New\n")},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.SHA != "newcommit" || result.URL != "http://test/root/test/commit/newcommit" {
		t.Errorf("result = %+v, want newcommit sha and url", result)
	}

	if len(posted.Files) != 2 {
		t.Fatalf("posted files = %+v, want 2", posted.Files)
	}
	if posted.Branch != "agent/story-1" || posted.Message != "Add ticket" {
		t.Errorf("posted = %+v, want branch agent/story-1 and the message", posted)
	}
	for _, op := range posted.Files {
		switch op.Path {
		case "README.md":
			if op.Operation != "update" || op.SHA != "oldblob" {
				t.Errorf("README op = %+v, want update with old blob sha", op)
			}
		case "backlog/tasks/new.md":
			if op.Operation != "create" || op.SHA != "" {
				t.Errorf("new op = %+v, want create without sha", op)
			}
		}
		if got, err := base64.StdEncoding.DecodeString(op.Content); err != nil || len(got) == 0 {
			t.Errorf("content = %q, want base64-encoded file content", op.Content)
		}
	}
	// Two existence lookups (one per change) plus the commit POST.
	if got := len(rec.all()); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}

	t.Run("no changes", func(t *testing.T) {
		if _, err := c.Commit(context.Background(), "root/test", "agent/story-1", "m", nil); err == nil {
			t.Error("commit without changes accepted")
		}
	})
	t.Run("missing branch", func(t *testing.T) {
		c2, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusNotFound, forgejoError("The target couldn't be found."))
		})
		_, err := c2.Commit(context.Background(), "root/test", "missing", "m",
			[]FileChange{{Path: "a.md", Content: []byte("x")}})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
	t.Run("server rejects commit", func(t *testing.T) {
		c2, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				respondJSON(w, http.StatusUnprocessableEntity, forgejoError("branch is protected"))
				return
			}
			respondJSON(w, http.StatusNotFound, forgejoError("The target couldn't be found."))
		})
		_, err := c2.Commit(context.Background(), "root/test", "main", "m",
			[]FileChange{{Path: "a.md", Content: []byte("x")}})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("error = %v, want 422 APIError", err)
		}
	})
}

func TestForgejoClientMergeRequests(t *testing.T) {
	pull := func() map[string]any {
		return map[string]any{
			"number":   7,
			"title":    "Refine story 1",
			"body":     "Implementation tickets",
			"state":    "open",
			"html_url": "http://test/root/test/pulls/7",
			"user":     map[string]any{"login": "agent"},
			"head":     map[string]any{"ref": "agent/story-1"},
			"base":     map[string]any{"ref": "main"},
		}
	}
	wantMR := MergeRequest{
		Number:      7,
		Title:       "Refine story 1",
		Description: "Implementation tickets",
		State:       "open",
		Head:        "agent/story-1",
		Base:        "main",
		URL:         "http://test/root/test/pulls/7",
		Author:      "agent",
	}

	t.Run("create", func(t *testing.T) {
		c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusCreated, pull())
		})
		mr, err := c.CreateMergeRequest(context.Background(), "root/test", "Refine story 1", "Implementation tickets", "agent/story-1", "main")
		if err != nil {
			t.Fatalf("CreateMergeRequest: %v", err)
		}
		if mr != wantMR {
			t.Errorf("mr = %+v, want %+v", mr, wantMR)
		}
		got := rec.last()
		if got.Method != http.MethodPost || got.Path != "/api/v1/repos/root/test/pulls" {
			t.Errorf("request = %s %s, want POST pulls", got.Method, got.Path)
		}
		var body createPullRequestRequest
		if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Title != "Refine story 1" || body.Body != "Implementation tickets" || body.Head != "agent/story-1" || body.Base != "main" {
			t.Errorf("body = %+v, want title/description/head/base", body)
		}
	})

	t.Run("get", func(t *testing.T) {
		c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, pull())
		})
		mr, err := c.GetMergeRequest(context.Background(), "root/test", 7)
		if err != nil {
			t.Fatalf("GetMergeRequest: %v", err)
		}
		if mr != wantMR {
			t.Errorf("mr = %+v, want %+v", mr, wantMR)
		}
		got := rec.last()
		if got.Method != http.MethodGet || got.Path != "/api/v1/repos/root/test/pulls/7" {
			t.Errorf("request = %s %s, want GET pulls/7", got.Method, got.Path)
		}
	})

	t.Run("get missing", func(t *testing.T) {
		c, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusNotFound, forgejoError("The target couldn't be found."))
		})
		if _, err := c.GetMergeRequest(context.Background(), "root/test", 99); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
}

func TestForgejoClientMergeRequestComments(t *testing.T) {
	comment := func(id int64, body string) map[string]any {
		return map[string]any{
			"id":         id,
			"body":       body,
			"created_at": "2026-10-27T12:00:00Z",
			"user":       map[string]any{"login": "reviewer"},
		}
	}

	t.Run("list", func(t *testing.T) {
		c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, []map[string]any{comment(1, "first"), comment(2, "second")})
		})
		comments, err := c.ListMergeRequestComments(context.Background(), "root/test", 7)
		if err != nil {
			t.Fatalf("ListMergeRequestComments: %v", err)
		}
		want := []Comment{
			{ID: 1, Body: "first", Author: "reviewer", CreatedAt: "2026-10-27T12:00:00Z"},
			{ID: 2, Body: "second", Author: "reviewer", CreatedAt: "2026-10-27T12:00:00Z"},
		}
		if !reflect.DeepEqual(comments, want) {
			t.Errorf("comments = %+v, want %+v", comments, want)
		}
		got := rec.last()
		if got.Method != http.MethodGet || got.Path != "/api/v1/repos/root/test/issues/7/comments" {
			t.Errorf("request = %s %s, want GET issues/7/comments", got.Method, got.Path)
		}
	})

	t.Run("add", func(t *testing.T) {
		c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusCreated, comment(3, "looks good"))
		})
		added, err := c.AddMergeRequestComment(context.Background(), "root/test", 7, "looks good")
		if err != nil {
			t.Fatalf("AddMergeRequestComment: %v", err)
		}
		if added.ID != 3 || added.Body != "looks good" || added.Author != "reviewer" {
			t.Errorf("comment = %+v, want id 3 with body and author", added)
		}
		got := rec.last()
		if got.Method != http.MethodPost || got.Path != "/api/v1/repos/root/test/issues/7/comments" {
			t.Errorf("request = %s %s, want POST issues/7/comments", got.Method, got.Path)
		}
		if got.Body != `{"body":"looks good"}` {
			t.Errorf("body = %s, want the comment body", got.Body)
		}
	})
}

func TestForgejoClientAPIError(t *testing.T) {
	c, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusForbidden, forgejoError("token lacks scope"))
	})
	_, err := c.GetFile(context.Background(), "root/test", "README.md", "main")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Message != "token lacks scope" {
		t.Errorf("message = %q, want the provider message", apiErr.Message)
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "token lacks scope") {
		t.Errorf("error text = %q, want status and message", err.Error())
	}
}

func TestForgejoClientInvalidRepo(t *testing.T) {
	called := false
	c, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		respondJSON(w, http.StatusOK, fileContents("README.md", "x", "s", 1))
	})
	for _, repo := range []string{"justaname", "a/b/c", "owner/", "/repo"} {
		if _, err := c.GetFile(context.Background(), repo, "README.md", "main"); err == nil {
			t.Errorf("repo %q accepted", repo)
		}
	}
	if called {
		t.Error("invalid repo names reached the API")
	}
}

func TestForgejoClientPathSafety(t *testing.T) {
	served := false
	c, rec := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		served = true
		respondJSON(w, http.StatusOK, fileContents("README.md", "x", "s", 1))
	})
	for _, path := range []string{"", "/abs.md", "../escape.md", "a/../b.md"} {
		if _, err := c.GetFile(context.Background(), "root/test", path, "main"); err == nil {
			t.Errorf("path %q accepted", path)
		}
	}
	if served || len(rec.all()) != 0 {
		t.Error("invalid paths reached the API")
	}

	// Valid paths with special characters escape per segment.
	c2, rec2 := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, fileContents("my file.md", "x", "s", 1))
	})
	if _, err := c2.GetFile(context.Background(), "root/test", "my file.md", "main"); err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got := rec2.last().EscapedPath; got != "/api/v1/repos/root/test/contents/my%20file.md" {
		t.Errorf("escaped path = %q, want per-segment escaping", got)
	}
}

func TestForgejoClientRequestURL(t *testing.T) {
	var gotURL string
	c, _ := newTestForgejo(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		respondJSON(w, http.StatusOK, fileContents("README.md", "x", "s", 1))
	})
	if _, err := c.GetFile(context.Background(), "root/test", "README.md", "main"); err != nil {
		t.Fatal(err)
	}
	if want := "/api/v1/repos/root/test/contents/README.md"; gotURL != want {
		t.Errorf("url = %q, want %q", gotURL, want)
	}
	if !strings.HasPrefix(fmt.Sprint(c.baseURL), "http://127.0.0.1") {
		t.Errorf("baseURL = %q, want the test server", c.baseURL)
	}
}
