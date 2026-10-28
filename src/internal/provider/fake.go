package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Fake is an in-memory Client for tests: Temporal activity tests (and
// any other test) substitute it to exercise agent logic without real
// provider API calls.
//
// A Fake holds one or more repositories, each with a default "main"
// branch; SeedFiles arranges file content on a branch. Each branch has
// its own files, so branch-then-commit-then-MR flows behave
// realistically enough for logic tests. Commit SHAs are generated
// strings that GetFile and ListFiles also accept as refs. It is not a
// git engine: history, diffs, and merge semantics do not exist.
type Fake struct {
	// Err, when non-nil, is returned by every operation, for testing
	// error handling.
	Err error

	mu    sync.Mutex
	repos map[string]*fakeRepo
}

// fakeRepo is one repository held by a Fake.
type fakeRepo struct {
	defaultBranch string
	branches      map[string]*fakeBranch
	shas          map[string]*fakeBranch // commit SHA -> branch state at that commit
	mergeRequests map[int64]MergeRequest
	comments      map[int64][]Comment
	nextNumber    int64
	nextSHA       int
}

// fakeBranch is one branch's state: its name, head SHA, and files.
type fakeBranch struct {
	name  string
	sha   string
	files map[string]File
}

// NewFake returns a Fake serving the given repositories, each addressed
// by its provider full name ("owner/repo"). Every repository starts
// with an empty default branch ("main"); seed content with SeedFiles.
func NewFake(repos ...string) *Fake {
	f := &Fake{repos: map[string]*fakeRepo{}}
	for _, repo := range repos {
		f.repo(repo)
	}
	return f
}

// repo returns (creating if needed) the state of one repository.
// The caller must hold f.mu or be initializing.
func (f *Fake) repo(repo string) *fakeRepo {
	if f.repos == nil {
		f.repos = map[string]*fakeRepo{}
	}
	r, ok := f.repos[repo]
	if !ok {
		r = &fakeRepo{
			defaultBranch: "main",
			branches:      map[string]*fakeBranch{"main": newFakeBranch("main")},
			shas:          map[string]*fakeBranch{},
			mergeRequests: map[int64]MergeRequest{},
			comments:      map[int64][]Comment{},
		}
		f.repos[repo] = r
	}
	return r
}

func newFakeBranch(name string) *fakeBranch {
	return &fakeBranch{name: name, files: map[string]File{}}
}

// SeedFiles stores file content on a repository branch, for arranging
// test state without Commit. It creates the branch when it does not
// exist.
func (f *Fake) SeedFiles(repo, branch string, files map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	b, ok := r.branches[branch]
	if !ok {
		b = newFakeBranch(branch)
		r.branches[branch] = b
	}
	for path, content := range files {
		b.files[path] = File{
			Path:    path,
			Content: []byte(content),
			SHA:     fakeBlobSHA(repo, path),
			Size:    int64(len(content)),
		}
	}
}

// GetFile implements Client.
func (f *Fake) GetFile(_ context.Context, repo, path, ref string) (File, error) {
	if err := f.op(); err != nil {
		return File{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, b, err := f.resolveRef(repo, ref)
	if err != nil {
		return File{}, err
	}
	file, ok := b.files[path]
	if !ok {
		return File{}, fmt.Errorf("%w: file %s in %s", ErrNotFound, path, repo)
	}
	return file, nil
}

// ListFiles implements Client.
func (f *Fake) ListFiles(_ context.Context, repo, path, ref string) ([]FileInfo, error) {
	if err := f.op(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, b, err := f.resolveRef(repo, ref)
	if err != nil {
		return nil, err
	}
	var infos []FileInfo
	for p, file := range b.files {
		if under(path, p) {
			infos = append(infos, FileInfo{Path: p, SHA: file.SHA, Size: file.Size})
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos, nil
}

// CreateBranch implements Client.
func (f *Fake) CreateBranch(_ context.Context, repo, branch, fromRef string) (Branch, error) {
	if err := f.op(); err != nil {
		return Branch{}, err
	}
	if strings.TrimSpace(branch) == "" {
		return Branch{}, errors.New("branch name is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repo]
	if !ok {
		return Branch{}, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	if _, exists := r.branches[branch]; exists {
		return Branch{}, &APIError{StatusCode: 409, Status: "409 Conflict", Message: "branch already exists"}
	}
	from, ok := r.branches[fromRef]
	if !ok {
		if fromRef == "" {
			from, ok = r.branches[r.defaultBranch]
		} else if b, okSHA := r.shas[fromRef]; okSHA {
			from, ok = b, true
		}
	}
	if !ok {
		return Branch{}, fmt.Errorf("%w: ref %s in %s", ErrNotFound, fromRef, repo)
	}
	created := newFakeBranch(branch)
	created.sha = from.sha
	for p, file := range from.files {
		created.files[p] = file
	}
	r.branches[branch] = created
	return Branch{Name: branch, SHA: created.sha}, nil
}

// Commit implements Client.
func (f *Fake) Commit(_ context.Context, repo, branch, _ string, files []FileChange) (CommitResult, error) {
	if err := f.op(); err != nil {
		return CommitResult{}, err
	}
	if len(files) == 0 {
		return CommitResult{}, errors.New("no file changes to commit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repo]
	if !ok {
		return CommitResult{}, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	b, ok := r.branches[branch]
	if !ok {
		return CommitResult{}, fmt.Errorf("%w: branch %s in %s", ErrNotFound, branch, repo)
	}
	for _, fc := range files {
		if err := validPath(fc.Path); err != nil {
			return CommitResult{}, err
		}
		b.files[fc.Path] = File{
			Path:    fc.Path,
			Content: fc.Content,
			SHA:     fakeBlobSHA(repo, fc.Path),
			Size:    int64(len(fc.Content)),
		}
	}
	r.nextSHA++
	sha := "fakesha" + strconv.Itoa(r.nextSHA)
	b.sha = sha
	r.shas[sha] = b
	return CommitResult{SHA: sha, URL: "http://fake/" + repo + "/commit/" + sha}, nil
}

// CreateMergeRequest implements Client.
func (f *Fake) CreateMergeRequest(_ context.Context, repo, title, description, head, base string) (MergeRequest, error) {
	if err := f.op(); err != nil {
		return MergeRequest{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repo]
	if !ok {
		return MergeRequest{}, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	r.nextNumber++
	number := r.nextNumber
	mr := MergeRequest{
		Number:      number,
		Title:       title,
		Description: description,
		State:       "open",
		Head:        head,
		Base:        base,
		URL:         "http://fake/" + repo + "/pulls/" + strconv.FormatInt(number, 10),
		Author:      "fake",
	}
	r.mergeRequests[number] = mr
	return mr, nil
}

// GetMergeRequest implements Client.
func (f *Fake) GetMergeRequest(_ context.Context, repo string, number int64) (MergeRequest, error) {
	if err := f.op(); err != nil {
		return MergeRequest{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repo]
	if !ok {
		return MergeRequest{}, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	mr, ok := r.mergeRequests[number]
	if !ok {
		return MergeRequest{}, fmt.Errorf("%w: merge request %d in %s", ErrNotFound, number, repo)
	}
	return mr, nil
}

// ListMergeRequestComments implements Client.
func (f *Fake) ListMergeRequestComments(_ context.Context, repo string, number int64) ([]Comment, error) {
	if err := f.op(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repo]
	if !ok {
		return nil, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	comments := r.comments[number]
	out := make([]Comment, len(comments))
	copy(out, comments)
	return out, nil
}

// AddMergeRequestComment implements Client.
func (f *Fake) AddMergeRequestComment(_ context.Context, repo string, number int64, body string) (Comment, error) {
	if err := f.op(); err != nil {
		return Comment{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repo]
	if !ok {
		return Comment{}, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	if _, ok := r.mergeRequests[number]; !ok {
		return Comment{}, fmt.Errorf("%w: merge request %d in %s", ErrNotFound, number, repo)
	}
	comment := Comment{
		ID:     int64(len(r.comments[number]) + 1),
		Body:   body,
		Author: "fake",
	}
	r.comments[number] = append(r.comments[number], comment)
	return comment, nil
}

// op applies the injected error, if any.
func (f *Fake) op() error { return f.Err }

// resolveRef finds the branch state a ref names: a branch name, a
// commit SHA the fake issued, or the repository's default branch when
// empty.
func (f *Fake) resolveRef(repo, ref string) (*fakeRepo, *fakeBranch, error) {
	r, ok := f.repos[repo]
	if !ok {
		return nil, nil, fmt.Errorf("%w: repository %s", ErrNotFound, repo)
	}
	if ref == "" {
		ref = r.defaultBranch
	}
	if b, ok := r.branches[ref]; ok {
		return r, b, nil
	}
	if b, ok := r.shas[ref]; ok {
		return r, b, nil
	}
	return nil, nil, fmt.Errorf("%w: ref %s in %s", ErrNotFound, ref, repo)
}

// under reports whether path lies at or below dir.
func under(dir, path string) bool {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return true
	}
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}

func fakeBlobSHA(repo, path string) string {
	return "fakeblob" + strconv.Itoa(len(repo)+len(path))
}
