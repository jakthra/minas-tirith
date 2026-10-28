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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ForgejoClient is a Client speaking the Forgejo REST API (v1). Gitea
// shares the same API, so it serves Gitea instances too. Authentication
// is a provider access token (Authorization: token header).
type ForgejoClient struct {
	baseURL string // instance root, no trailing slash, e.g. http://localhost:3000
	token   string
	http    *http.Client
}

// NewForgejoClient returns a client for the Forgejo (or Gitea) instance
// at baseURL, authenticating with the provider access token. Both are
// required: configure them via FORGEJO_URL and FORGEJO_TOKEN (or the
// -forgejo-url and -forgejo-token flags).
func NewForgejoClient(baseURL, token string) (*ForgejoClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("forgejo url is required")
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid forgejo url %q", baseURL)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("forgejo token is required")
	}
	return &ForgejoClient{
		baseURL: u.String(),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// GetFile implements Client.
func (c *ForgejoClient) GetFile(ctx context.Context, repo, path, ref string) (File, error) {
	if err := validPath(path); err != nil {
		return File{}, err
	}
	var resp contentsResponse
	if err := c.getContents(ctx, repo, path, ref, &resp); err != nil {
		return File{}, err
	}
	if resp.Type != "file" {
		return File{}, fmt.Errorf("%s in %s is a %s, not a file", path, repo, resp.Type)
	}
	content, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		return File{}, fmt.Errorf("decode content of %s in %s: %w", path, repo, err)
	}
	return File{Path: resp.Path, Content: content, SHA: resp.SHA, Size: resp.Size}, nil
}

// ListFiles implements Client. Results are sorted by path, so callers
// get a stable order regardless of the provider's directory traversal.
func (c *ForgejoClient) ListFiles(ctx context.Context, repo, path, ref string) ([]FileInfo, error) {
	var infos []FileInfo
	if err := c.listFiles(ctx, repo, path, ref, &infos); err != nil {
		return nil, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos, nil
}

// listFiles appends every file under path (recursively) to infos.
func (c *ForgejoClient) listFiles(ctx context.Context, repo, path, ref string, infos *[]FileInfo) error {
	var raw json.RawMessage
	if err := c.getContents(ctx, repo, path, ref, &raw); err != nil {
		return err
	}
	entries, err := contentsEntries(raw)
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch e.Type {
		case "file":
			*infos = append(*infos, FileInfo{Path: e.Path, SHA: e.SHA, Size: e.Size})
		case "dir":
			if err := c.listFiles(ctx, repo, e.Path, ref, infos); err != nil {
				return err
			}
		}
	}
	return nil
}

// CreateBranch implements Client.
func (c *ForgejoClient) CreateBranch(ctx context.Context, repo, branch, fromRef string) (Branch, error) {
	path, err := repoPath(repo, "branches")
	if err != nil {
		return Branch{}, err
	}
	body := createBranchRequest{NewBranchName: branch, OldRefName: fromRef}
	var resp branchResponse
	if err := c.do(ctx, http.MethodPost, path, nil, body, &resp); err != nil {
		return Branch{}, err
	}
	return Branch{Name: resp.Name, SHA: resp.Commit.ID}, nil
}

// Commit implements Client. It resolves each change against the branch
// (an existing file becomes an update carrying its blob SHA, a missing
// file a create), then submits them as one commit.
func (c *ForgejoClient) Commit(ctx context.Context, repo, branch, message string, files []FileChange) (CommitResult, error) {
	if len(files) == 0 {
		return CommitResult{}, errors.New("no file changes to commit")
	}
	ops := make([]changeFileOperation, 0, len(files))
	for _, f := range files {
		op, err := c.changeOperation(ctx, repo, branch, f)
		if err != nil {
			return CommitResult{}, err
		}
		ops = append(ops, op)
	}
	path, err := repoPath(repo, "contents")
	if err != nil {
		return CommitResult{}, err
	}
	body := changeFilesRequest{Branch: branch, Message: message, Files: ops}
	var resp filesResponse
	if err := c.do(ctx, http.MethodPost, path, nil, body, &resp); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{SHA: resp.Commit.SHA, URL: resp.Commit.HTMLURL}, nil
}

// changeOperation resolves one file change against the branch: an
// existing file updates (carrying the blob SHA the provider requires),
// a missing file is created.
func (c *ForgejoClient) changeOperation(ctx context.Context, repo, branch string, f FileChange) (changeFileOperation, error) {
	if err := validPath(f.Path); err != nil {
		return changeFileOperation{}, err
	}
	op := changeFileOperation{
		Operation: "create",
		Path:      f.Path,
		Content:   base64.StdEncoding.EncodeToString(f.Content),
	}
	current, err := c.GetFile(ctx, repo, f.Path, branch)
	if err == nil {
		op.Operation = "update"
		op.SHA = current.SHA
	} else if !errors.Is(err, ErrNotFound) {
		return changeFileOperation{}, err
	}
	return op, nil
}

// CreateMergeRequest implements Client.
func (c *ForgejoClient) CreateMergeRequest(ctx context.Context, repo, title, description, head, base string) (MergeRequest, error) {
	path, err := repoPath(repo, "pulls")
	if err != nil {
		return MergeRequest{}, err
	}
	body := createPullRequestRequest{Title: title, Body: description, Head: head, Base: base}
	var resp pullRequestResponse
	if err := c.do(ctx, http.MethodPost, path, nil, body, &resp); err != nil {
		return MergeRequest{}, err
	}
	return mergeRequestFrom(resp), nil
}

// GetMergeRequest implements Client.
func (c *ForgejoClient) GetMergeRequest(ctx context.Context, repo string, number int64) (MergeRequest, error) {
	path, err := repoPath(repo, "pulls/"+strconv.FormatInt(number, 10))
	if err != nil {
		return MergeRequest{}, err
	}
	var resp pullRequestResponse
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &resp); err != nil {
		return MergeRequest{}, err
	}
	return mergeRequestFrom(resp), nil
}

// ListMergeRequestComments implements Client. Merge requests are issues
// in the Forgejo API, so comments live under the issues endpoint.
func (c *ForgejoClient) ListMergeRequestComments(ctx context.Context, repo string, number int64) ([]Comment, error) {
	path, err := repoPath(repo, "issues/"+strconv.FormatInt(number, 10)+"/comments")
	if err != nil {
		return nil, err
	}
	var resp []commentResponse
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &resp); err != nil {
		return nil, err
	}
	comments := make([]Comment, 0, len(resp))
	for _, rc := range resp {
		comments = append(comments, commentFrom(rc))
	}
	return comments, nil
}

// AddMergeRequestComment implements Client.
func (c *ForgejoClient) AddMergeRequestComment(ctx context.Context, repo string, number int64, body string) (Comment, error) {
	path, err := repoPath(repo, "issues/"+strconv.FormatInt(number, 10)+"/comments")
	if err != nil {
		return Comment{}, err
	}
	var resp commentResponse
	req := createIssueCommentRequest{Body: body}
	if err := c.do(ctx, http.MethodPost, path, nil, req, &resp); err != nil {
		return Comment{}, err
	}
	return commentFrom(resp), nil
}

// --- HTTP plumbing ---

// do performs one API request against /api/v1: body (when non-nil) is
// sent as JSON, and a 2xx response is decoded into out (when non-nil).
// Errors map to ErrNotFound for 404 responses and *APIError for every
// other non-2xx status.
func (c *ForgejoClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + "/api/v1/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s request: %w", method, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(method, path, resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// responseError turns a non-2xx response into an error: 404 wraps
// ErrNotFound, everything else becomes an *APIError.
func responseError(method, path string, resp *http.Response) error {
	var payload struct {
		Message string `json:"message"`
	}
	// A truncated or non-JSON error body must not mask the status.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s: %s", ErrNotFound, method, path, payload.Message)
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    payload.Message,
	}
}

// getContents fetches a contents resource (a file object or a directory
// listing) into out. An empty path lists the repository root.
func (c *ForgejoClient) getContents(ctx context.Context, repo, path, ref string, out any) error {
	resource := "contents"
	if path != "" {
		if err := validPath(path); err != nil {
			return err
		}
		resource += "/" + escapePath(path)
	}
	p, err := repoPath(repo, resource)
	if err != nil {
		return err
	}
	var query url.Values
	if ref != "" {
		query = url.Values{"ref": []string{ref}}
	}
	return c.do(ctx, http.MethodGet, p, query, nil, out)
}

// --- request/response wire types (only the fields the client uses) ---

type contentsResponse struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Type     string `json:"type"` // file, dir, symlink, submodule
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// contentsEntries decodes a contents response that is either a single
// file object or a directory listing.
func contentsEntries(raw json.RawMessage) ([]contentsResponse, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var entries []contentsResponse
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("parse directory listing: %w", err)
		}
		return entries, nil
	}
	var one contentsResponse
	if err := json.Unmarshal(trimmed, &one); err != nil {
		return nil, fmt.Errorf("parse contents response: %w", err)
	}
	return []contentsResponse{one}, nil
}

type branchResponse struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type createBranchRequest struct {
	NewBranchName string `json:"new_branch_name"`
	OldRefName    string `json:"old_ref_name"`
}

type changeFileOperation struct {
	Operation string `json:"operation"` // create or update
	Path      string `json:"path"`
	Content   string `json:"content"` // base64-encoded
	SHA       string `json:"sha"`     // current blob SHA, for updates
}

type changeFilesRequest struct {
	Branch  string                `json:"branch"`
	Message string                `json:"message"`
	Files   []changeFileOperation `json:"files"`
}

type filesResponse struct {
	Commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	} `json:"commit"`
}

type createPullRequestRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

type pullRequestResponse struct {
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func mergeRequestFrom(resp pullRequestResponse) MergeRequest {
	return MergeRequest{
		Number:      resp.Number,
		Title:       resp.Title,
		Description: resp.Body,
		State:       resp.State,
		Head:        resp.Head.Ref,
		Base:        resp.Base.Ref,
		URL:         resp.HTMLURL,
		Author:      resp.User.Login,
	}
}

type commentResponse struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type createIssueCommentRequest struct {
	Body string `json:"body"`
}

func commentFrom(resp commentResponse) Comment {
	return Comment{
		ID:        resp.ID,
		Body:      resp.Body,
		Author:    resp.User.Login,
		CreatedAt: resp.CreatedAt,
	}
}

// --- path helpers ---

// repoPath builds an API path for one repository resource:
// repos/{owner}/{name}/{rest}. The repository is the provider full
// name, e.g. "root/BeanieGenie".
func repoPath(repo, rest string) (string, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("invalid repository %q: want the provider full name, e.g. \"root/BeanieGenie\"", repo)
	}
	p := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	if rest != "" {
		p += "/" + rest
	}
	return p, nil
}

// escapePath escapes a repository file path so every segment is a valid
// path element while "/" separators survive.
func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// validPath rejects file paths the API could not address safely.
func validPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("file path is required")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("file path %q must be relative to the repository root", path)
	}
	for _, s := range strings.Split(path, "/") {
		if s == ".." {
			return fmt.Errorf("file path %q must not contain \"..\"", path)
		}
	}
	return nil
}
