package taskimplementation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"go.temporal.io/sdk/activity"
	"gopkg.in/yaml.v3"

	"minas-tirith/internal/provider"
	"minas-tirith/internal/worker/backlog"
)

// This file holds the implementation agent's repository tooling: the
// Temporal activities through which the workflow reads the task it must
// implement, discovers the repository layout, reads the source files it
// is about to change, and commits its changes to a branch it owns. Every
// provider API call happens here, never in the workflow body, so the
// workflow stays deterministic. The activities are deliberately narrow —
// a small, testable toolset, not a general-purpose coding agent — and
// they are the only way the implementation workflow touches the
// repository.

// maxListedFiles bounds the result of one ListRepository call, so a
// repository the agent cannot navigate in one step still returns a
// usable, bounded listing instead of an unbounded payload.
const maxListedFiles = 2000

// taskFrontmatter is the backlog.md task frontmatter the implementation
// agent needs: what the task is and what it must achieve. The body's
// acceptance criteria section is parsed separately (see parseTask).
type taskFrontmatter struct {
	// ID is the task id, e.g. "TASK-22".
	ID string `yaml:"id"`
	// Title is the task title.
	Title string `yaml:"title"`
	// Status is the task status, e.g. "To Do".
	Status string `yaml:"status"`
	// Type is the backlog item type ("task" for an implementable task).
	Type string `yaml:"type"`
}

// Task is the backlog task the implementation agent must implement, as
// ReadTask returns it.
type Task struct {
	// Path is the task file's path from the repository root.
	Path string `json:"path"`
	// Ref is the ref the task was read at (the workflow's task commit).
	Ref string `json:"ref"`
	// ID is the task id from the frontmatter, e.g. "TASK-22".
	ID string `json:"id"`
	// Title is the task title from the frontmatter.
	Title string `json:"title"`
	// Status is the task status from the frontmatter.
	Status string `json:"status"`
	// Description is the task's description section body, without the
	// section markers.
	Description string `json:"description"`
	// AcceptanceCriteria lists the task's acceptance criteria in order,
	// each without its checkbox and criterion number.
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	// Content is the task file's complete markdown content, so the agent
	// sees everything the task says, sections the parsed fields do not
	// cover included.
	Content string `json:"content"`
}

// ReadTaskInput is the input to the ReadTask activity.
type ReadTaskInput struct {
	// Repository is the provider full repo name, e.g. "root/BeanieGenie".
	Repository string `json:"repository"`
	// Path is the task file's path from the repository root.
	Path string `json:"path"`
	// Ref is the commit the workflow started on (the task commit); the
	// task is read at that commit, not at a branch head that may have
	// moved since.
	Ref string `json:"ref"`
}

// ReadTask reads the backlog task file the agent must implement and
// returns its content with the parsed frontmatter (id, title, status),
// its description, and its acceptance criteria. It runs as a Temporal
// activity so the provider API call stays out of the workflow body. It
// fails with provider.ErrNotFound when the task file does not exist at
// ref, and with a validation error when path is not a backlog task file.
func (a *Activities) ReadTask(ctx context.Context, in ReadTaskInput) (Task, error) {
	if err := a.requireProvider("ReadTask"); err != nil {
		return Task{}, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return Task{}, err
	}
	if !backlog.IsFile(in.Path) {
		return Task{}, fmt.Errorf("read task: %q is not a backlog task file under %s", in.Path, backlog.Dir)
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("reading implementation task",
			"repository", in.Repository, "path", in.Path, "ref", ShortSHA(in.Ref))
	}

	file, err := a.provider.GetFile(ctx, in.Repository, in.Path, in.Ref)
	if err != nil {
		return Task{}, fmt.Errorf("read task %s at %s in %s: %w", in.Path, ShortSHA(in.Ref), in.Repository, err)
	}
	task, err := parseTask(in.Path, in.Ref, string(file.Content))
	if err != nil {
		return Task{}, err
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("read implementation task",
			"task", task.ID, "criteria", len(task.AcceptanceCriteria))
	}
	return task, nil
}

// parseTask parses a backlog.md task file into a Task: the frontmatter
// (id, title, status) and the body's description and acceptance criteria
// sections. It is the pure logic of ReadTask, separated so unit tests can
// exercise it without a provider.
func parseTask(path, ref, content string) (Task, error) {
	task := Task{Path: path, Ref: ref, Content: content}

	block, ok := backlog.FrontmatterBlock(content)
	if !ok {
		return Task{}, fmt.Errorf("parse task %s: no frontmatter", path)
	}
	var fm taskFrontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return Task{}, fmt.Errorf("parse task %s frontmatter: %w", path, err)
	}
	task.ID = strings.TrimSpace(fm.ID)
	task.Title = strings.TrimSpace(fm.Title)
	task.Status = strings.TrimSpace(fm.Status)
	if task.ID == "" {
		return Task{}, fmt.Errorf("parse task %s: frontmatter has no id", path)
	}

	sections := splitSections(bodyAfterFrontmatter(content, block))
	task.Description = sections["description"]
	task.AcceptanceCriteria = parseCriteria(sections["acceptance criteria"])
	return task, nil
}

// bodyAfterFrontmatter returns content after its closing frontmatter
// line. block is the frontmatter block FrontmatterBlock returned.
func bodyAfterFrontmatter(content, block string) string {
	i := strings.Index(content, "\n"+block)
	if i < 0 {
		return ""
	}
	rest := content[i+1+len(block):]
	j := strings.IndexByte(rest, '\n')
	if j < 0 {
		return ""
	}
	return rest[j+1:]
}

// sectionMarkerPrefix starts a backlog.md section marker line, e.g.
// "<!-- SECTION:DESCRIPTION:BEGIN -->".
const sectionMarkerPrefix = "<!-- SECTION:"

// splitSections splits a markdown body into its level-two sections,
// keyed by the lower-cased heading text. Section marker comments
// (<!-- SECTION:NAME:BEGIN --> ... <!-- SECTION:NAME:END -->) are
// stripped, so the section body is the text a reader sees.
func splitSections(body string) map[string]string {
	sections := make(map[string]string)
	var name string
	var lines []string
	flush := func() {
		if name != "" {
			sections[name] = strings.TrimSpace(strings.Join(lines, "\n"))
		}
		name, lines = "", nil
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, sectionMarkerPrefix) {
			continue
		}
		if heading, ok := strings.CutPrefix(trimmed, "## "); ok {
			flush()
			name = strings.ToLower(strings.TrimSpace(heading))
			continue
		}
		if name != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return sections
}

// parseCriteria extracts the acceptance criteria from an acceptance
// criteria section: one entry per "- [ ]" or "- [x]" checkbox line, with
// the checkbox and a leading criterion number ("#1 ") removed.
func parseCriteria(section string) []string {
	var criteria []string
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		text, ok := strings.CutPrefix(trimmed, "- [ ] ")
		if !ok {
			text, ok = strings.CutPrefix(trimmed, "- [x] ")
		}
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if rest, isNumbered := cutCriterionNumber(text); isNumbered {
			text = rest
		}
		if text != "" {
			criteria = append(criteria, text)
		}
	}
	return criteria
}

// cutCriterionNumber removes a leading "#<digits> " criterion number.
func cutCriterionNumber(s string) (string, bool) {
	if !strings.HasPrefix(s, "#") {
		return s, false
	}
	rest := s[1:]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(rest) || rest[i] != ' ' {
		return s, false
	}
	return strings.TrimSpace(rest[i+1:]), true
}

// ListRepositoryInput is the input to the ListRepository activity.
type ListRepositoryInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Path narrows the listing to a directory (or a single file); empty
	// lists the whole repository.
	Path string `json:"path,omitempty"`
	// Ref is the branch or commit to list; empty means the repository's
	// default branch.
	Ref string `json:"ref"`
}

// FileEntry identifies one file in a ListRepository result.
type FileEntry struct {
	// Path is the file's path from the repository root.
	Path string `json:"path"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
}

// RepositoryListing is the result of the ListRepository activity.
type RepositoryListing struct {
	// Files lists the files under the requested path, sorted by path.
	Files []FileEntry `json:"files"`
	// Total is the number of files the provider reported, before the
	// result was bounded.
	Total int `json:"total"`
	// Truncated reports that Files holds only the first
	// maxListedFiles entries of Total.
	Truncated bool `json:"truncated,omitempty"`
}

// ListRepository lists the repository files on the task branch, so the
// agent can discover the layout and the existing code it must fit into.
// The result is bounded to maxListedFiles entries; Truncated and Total
// report when the bound applied. It runs as a Temporal activity so the
// provider API call stays out of the workflow body.
func (a *Activities) ListRepository(ctx context.Context, in ListRepositoryInput) (RepositoryListing, error) {
	if err := a.requireProvider("ListRepository"); err != nil {
		return RepositoryListing{}, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return RepositoryListing{}, err
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("listing repository files",
			"repository", in.Repository, "path", in.Path, "ref", in.Ref)
	}

	infos, err := a.provider.ListFiles(ctx, in.Repository, in.Path, in.Ref)
	if err != nil {
		return RepositoryListing{}, fmt.Errorf("list %q on %s in %s: %w", in.Path, in.Ref, in.Repository, err)
	}
	listing := RepositoryListing{Files: make([]FileEntry, 0, min(len(infos), maxListedFiles))}
	for _, info := range infos {
		if len(listing.Files) == maxListedFiles {
			listing.Truncated = true
			break
		}
		listing.Files = append(listing.Files, FileEntry{Path: info.Path, Size: info.Size})
	}
	listing.Total = len(infos)
	return listing, nil
}

// SearchRepositoryInput is the input to the SearchRepository activity.
type SearchRepositoryInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Query is a case-insensitive substring of the file path to find.
	Query string `json:"query"`
	// Ref is the branch or commit to search; empty means the
	// repository's default branch.
	Ref string `json:"ref"`
}

// SearchResult is the result of the SearchRepository activity.
type SearchResult struct {
	// Files lists the matching files, sorted by path.
	Files []FileEntry `json:"files"`
	// Total is the number of matching files before the result was
	// bounded.
	Total int `json:"total"`
	// Truncated reports that Files holds only the first
	// maxContextSearchMatches matches of Total, or that the listing the
	// search ran over was itself truncated by maxListedFiles (so the
	// search may miss files).
	Truncated bool `json:"truncated,omitempty"`
}

// SearchRepository finds the repository files whose path contains query,
// case-insensitively. It is a listing-based search — the provider client
// has no search operation — so it runs over the same bounded listing
// ListRepository returns; Truncated reports both a bounded result and a
// bounded listing, so a search is never silently incomplete. An empty
// query fails the activity. It runs as a Temporal activity so the
// provider API call stays out of the workflow body.
func (a *Activities) SearchRepository(ctx context.Context, in SearchRepositoryInput) (SearchResult, error) {
	if err := a.requireProvider("SearchRepository"); err != nil {
		return SearchResult{}, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return SearchResult{}, err
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	if query == "" {
		return SearchResult{}, errors.New("search repository: query is required")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("searching repository files",
			"repository", in.Repository, "query", query, "ref", in.Ref)
	}

	infos, err := a.provider.ListFiles(ctx, in.Repository, "", in.Ref)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search %q in %s at %s: %w", query, in.Repository, in.Ref, err)
	}
	result := SearchResult{Files: make([]FileEntry, 0, min(len(infos), maxContextSearchMatches))}
	for _, info := range infos {
		if !strings.Contains(strings.ToLower(info.Path), query) {
			continue
		}
		result.Total++
		if len(result.Files) == maxContextSearchMatches {
			result.Truncated = true
			continue
		}
		result.Files = append(result.Files, FileEntry{Path: info.Path, Size: info.Size})
	}
	if len(infos) > maxListedFiles {
		// The listing the search ran over was bounded, so files past
		// the bound were not searched.
		result.Truncated = true
	}
	return result, nil
}

// ReadFilesInput is the input to the ReadFiles activity.
type ReadFilesInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Paths are the files to read, relative to the repository root.
	Paths []string `json:"paths"`
	// Ref is the branch or commit to read; empty means the repository's
	// default branch.
	Ref string `json:"ref"`
}

// ReadFile is one file the ReadFiles activity read.
type ReadFile struct {
	// Path is the file's path from the repository root.
	Path string `json:"path"`
	// Content is the file content.
	Content string `json:"content"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
}

// ReadFiles reads one or more source files at ref. A file that does not
// exist is reported per file (Missing lists it) rather than failing the
// whole call, so the agent can tell "this file is new" from a provider
// failure; any other provider error fails the activity. Paths outside
// the repository fail the activity before any file is read. It runs as a
// Temporal activity so the provider API calls stay out of the workflow
// body.
func (a *Activities) ReadFiles(ctx context.Context, in ReadFilesInput) ([]ReadFile, error) {
	if err := a.requireProvider("ReadFiles"); err != nil {
		return nil, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return nil, err
	}
	if len(in.Paths) == 0 {
		return nil, errors.New("read files: no paths given")
	}
	for _, path := range in.Paths {
		if err := validateRepoPath(path); err != nil {
			return nil, fmt.Errorf("read files: %w", err)
		}
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("reading source files",
			"repository", in.Repository, "ref", in.Ref, "files", len(in.Paths))
	}

	files := make([]ReadFile, 0, len(in.Paths))
	for _, path := range in.Paths {
		file, err := a.provider.GetFile(ctx, in.Repository, path, in.Ref)
		if err != nil {
			if errors.Is(err, provider.ErrNotFound) {
				if activity.IsActivity(ctx) {
					logger := activity.GetLogger(ctx)
					logger.Info("source file does not exist", "path", path, "ref", in.Ref)
				}
				continue
			}
			return nil, fmt.Errorf("read file %s at %s in %s: %w", path, in.Ref, in.Repository, err)
		}
		files = append(files, ReadFile{Path: file.Path, Content: string(file.Content), Size: file.Size})
	}
	return files, nil
}

// CreateImplementationBranchInput is the input to the
// CreateImplementationBranch activity.
type CreateImplementationBranchInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// TaskID is the backlog task id, e.g. "TASK-22"; it names the branch.
	TaskID string `json:"task_id"`
	// TaskTitle is the task title; it slugs the branch name.
	TaskTitle string `json:"task_title"`
	// FromRef is the task commit the branch starts from.
	FromRef string `json:"from_ref"`
}

// ImplementationBranch is the result of the CreateImplementationBranch
// activity.
type ImplementationBranch struct {
	// Name is the implementation branch name.
	Name string `json:"name"`
	// SHA is the branch's head commit; empty when an existing branch was
	// reused.
	SHA string `json:"sha,omitempty"`
	// Reused reports that the branch already existed and was reused.
	Reused bool `json:"reused,omitempty"`
}

// CreateImplementationBranch creates the implementation branch from the
// task commit and returns it. The branch is always named
// implement/<task-slug>-<short-sha> by this activity (see
// ImplementationBranchName), so the workflow cannot create a branch
// outside the agent's namespace; a branch that already exists is reused,
// so a retried activity succeeds. It runs as a Temporal activity so the
// provider API call stays out of the workflow body.
func (a *Activities) CreateImplementationBranch(ctx context.Context, in CreateImplementationBranchInput) (ImplementationBranch, error) {
	if err := a.requireProvider("CreateImplementationBranch"); err != nil {
		return ImplementationBranch{}, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return ImplementationBranch{}, err
	}
	if strings.TrimSpace(in.FromRef) == "" {
		return ImplementationBranch{}, errors.New("create implementation branch: from_ref is required")
	}
	branch := ImplementationBranchName(in.TaskID, in.TaskTitle, in.FromRef)
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("creating implementation branch",
			"repository", in.Repository, "branch", branch, "from", ShortSHA(in.FromRef))
	}

	created, err := a.provider.CreateBranch(ctx, in.Repository, branch, in.FromRef)
	if err != nil {
		var apiErr *provider.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			if activity.IsActivity(ctx) {
				logger := activity.GetLogger(ctx)
				logger.Info("implementation branch already exists; reusing it", "branch", branch)
			}
			return ImplementationBranch{Name: branch, Reused: true}, nil
		}
		return ImplementationBranch{}, fmt.Errorf("create implementation branch %s in %s: %w", branch, in.Repository, err)
	}
	return ImplementationBranch{Name: created.Name, SHA: created.SHA}, nil
}

// ImplementationBranchName returns the implementation branch name for a
// task: implement/<task-slug>-<short-sha>, e.g.
// "implement/task-022-add-repository-tooling-8ef0de217a18". The short SHA
// keeps branches for different commits of the same task apart.
func ImplementationBranchName(taskID, taskTitle, sha string) string {
	return backlog.ImplementationBranchPrefix + taskSlug(taskID, taskTitle) + "-" + ShortSHA(sha)
}

// taskSlug builds the branch's task part: the lower-cased task id
// followed by the title slug, or just the id when the title is empty.
func taskSlug(taskID, taskTitle string) string {
	id := strings.ToLower(strings.TrimSpace(taskID))
	title := branchSlug(taskTitle)
	switch {
	case id == "" && title == "":
		return "task"
	case id == "":
		return title
	case title == "":
		return id
	}
	return id + "-" + title
}

// branchSlug turns a task title into a branch-name slug: lower-cased,
// with every run of characters that is not a letter or digit collapsed
// to a single dash.
func branchSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// CommitImplementationInput is the input to the CommitImplementation
// activity.
type CommitImplementationInput struct {
	// Repository is the provider full repo name.
	Repository string `json:"repository"`
	// Branch is the implementation branch the changes are committed to.
	// It must be an implement/* branch this agent owns.
	Branch string `json:"branch"`
	// Message is the commit message.
	Message string `json:"message"`
	// Files are the agent-produced file changes to commit.
	Files []provider.FileChange `json:"files"`
}

// CommitImplementationResult is the result of the CommitImplementation
// activity.
type CommitImplementationResult struct {
	// SHA is the new commit's hash.
	SHA string `json:"sha"`
	// URL is the commit's page on the provider.
	URL string `json:"url,omitempty"`
	// Files lists the committed paths, in input order.
	Files []string `json:"files"`
}

// CommitImplementation commits the agent-produced file changes to the
// implementation branch as one commit. It refuses paths outside the
// repository (absolute paths, ".." segments, empty paths) and any branch
// that is not an implement/* branch, so the agent can never write to a
// branch it does not own (a refinement branch, a default branch, or a
// branch outside the agent namespace). It runs as a Temporal activity so
// the provider API call stays out of the workflow body.
func (a *Activities) CommitImplementation(ctx context.Context, in CommitImplementationInput) (CommitImplementationResult, error) {
	if err := a.requireProvider("CommitImplementation"); err != nil {
		return CommitImplementationResult{}, err
	}
	if err := validateRepository(in.Repository); err != nil {
		return CommitImplementationResult{}, err
	}
	if !isImplementationBranch(in.Branch) {
		return CommitImplementationResult{}, fmt.Errorf(
			"commit implementation: branch %q is not an implementation branch (%s*) this agent owns",
			in.Branch, backlog.ImplementationBranchPrefix)
	}
	if len(in.Files) == 0 {
		return CommitImplementationResult{}, errors.New("commit implementation: no file changes given")
	}
	paths := make([]string, 0, len(in.Files))
	seen := make(map[string]bool)
	changes := make([]provider.FileChange, 0, len(in.Files))
	for _, change := range in.Files {
		if err := validateRepoPath(change.Path); err != nil {
			return CommitImplementationResult{}, fmt.Errorf("commit implementation: %w", err)
		}
		if seen[change.Path] {
			return CommitImplementationResult{}, fmt.Errorf("commit implementation: path %q is given twice", change.Path)
		}
		seen[change.Path] = true
		paths = append(paths, change.Path)
		changes = append(changes, change)
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("committing implementation",
			"repository", in.Repository, "branch", in.Branch, "files", len(changes))
	}

	message := strings.TrimSpace(in.Message)
	if message == "" {
		message = "Implement " + in.Branch
	}
	commit, err := a.provider.Commit(ctx, in.Repository, in.Branch, message, changes)
	if err != nil {
		return CommitImplementationResult{}, fmt.Errorf("commit implementation to %s in %s: %w", in.Branch, in.Repository, err)
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("committed implementation",
			"branch", in.Branch, "commit", ShortSHA(commit.SHA), "files", len(paths))
	}
	return CommitImplementationResult{SHA: commit.SHA, URL: commit.URL, Files: paths}, nil
}

// --- validation helpers ---

// requireProvider reports a clear error when the activities were built
// without a provider client (the listener registers the template without
// executing activities).
func (a *Activities) requireProvider(op string) error {
	if a.provider == nil {
		return fmt.Errorf("taskimplementation: %s: no provider client configured", op)
	}
	return nil
}

// validateRepository reports an empty repository name.
func validateRepository(repository string) error {
	if strings.TrimSpace(repository) == "" {
		return errors.New("repository is required")
	}
	return nil
}

// validateRepoPath reports a path that is not a file path inside the
// repository: empty, absolute, or containing "." or ".." segments.
func validateRepoPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("file path is required")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("file path %q must be relative to the repository root", path)
	}
	if strings.HasSuffix(path, "/") {
		return fmt.Errorf("file path %q must name a file, not a directory", path)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." || segment == "." {
			return fmt.Errorf("file path %q must not contain %q segments", path, segment)
		}
	}
	return nil
}

// isImplementationBranch reports whether branch is an implement/* branch
// the implementation agent owns.
func isImplementationBranch(branch string) bool {
	rest, ok := strings.CutPrefix(branch, backlog.ImplementationBranchPrefix)
	return ok && strings.TrimSpace(rest) != ""
}
