package taskimplementation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"minas-tirith/internal/llm"
)

// This file holds the implementation agent's context protocol: the
// structured requests through which the model inspects the repository
// itself (list a directory, search the repository, read specific files)
// before it proposes changes, and the RequestContext activity that runs
// one round of that conversation. The LLM client is single-shot and has
// no tool calling, so the model answers with a JSON object of the same
// shape as its implementation reply: either a set of context requests,
// or the implementation. The workflow executes the requests through the
// repository tooling activities (repository.go) and feeds the results
// back; the loop itself lives in the workflow body (workflow.go), which
// keeps every provider and LLM call inside an activity and stays
// deterministic.

// Context request kinds, as the model names them in its reply.
const (
	// ContextRequestList lists a directory (or the whole repository when
	// Path is empty).
	ContextRequestList = "list"
	// ContextRequestSearch finds repository files whose path contains
	// Query.
	ContextRequestSearch = "search"
	// ContextRequestRead reads specific files.
	ContextRequestRead = "read"
)

// maxContextRounds bounds how many request rounds the workflow runs
// before the final propose-only round. Each round is one LLM call that
// may issue requests; a model that keeps asking for more context is
// stopped here and the run reports a partial outcome naming the bound.
const maxContextRounds = 3

// maxContextBudgetBytes bounds the aggregate size of the repository
// context the workflow assembles across all rounds: the seed contents
// and every file and listing a request added. A request that would push
// the context past it is refused, the refusal is recorded, and the model
// is told which requests were refused, so a context-starved result is
// never misreported as missing information. The seed context always fits:
// maxContextFiles files of at most maxContextFileBytes each, plus the
// listing, are smaller than this bound (see seedContext).
const maxContextBudgetBytes = 4 << 20

// maxContextRequestItems bounds the files one request may read or the
// entries one search may return, so a single request cannot exhaust the
// budget in one step.
const maxContextRequestItems = 20

// maxContextSearchMatches bounds the matches one search reports.
const maxContextSearchMatches = 50

// ContextRequest is one repository context request the model makes: list
// a directory, search the repository, or read files.
type ContextRequest struct {
	// Kind is ContextRequestList, ContextRequestSearch, or
	// ContextRequestRead.
	Kind string `json:"kind"`
	// Path is the directory to list (empty lists the whole repository),
	// in a list request.
	Path string `json:"path,omitempty"`
	// Query is the path substring to search for, in a search request.
	Query string `json:"query,omitempty"`
	// Paths are the files to read, in a read request.
	Paths []string `json:"paths,omitempty"`
}

// String renders the request for prompts and outcome reasons.
func (r ContextRequest) String() string {
	switch r.Kind {
	case ContextRequestList:
		if r.Path == "" {
			return "list the repository root"
		}
		return "list " + r.Path
	case ContextRequestSearch:
		return fmt.Sprintf("search for %q", r.Query)
	case ContextRequestRead:
		return "read " + strings.Join(r.Paths, ", ")
	default:
		return fmt.Sprintf("%s request", r.Kind)
	}
}

// RequestContextInput is the input to the RequestContext activity: one
// round of the context conversation. It carries the task, the context
// assembled so far, and the conversation the previous rounds produced.
type RequestContextInput struct {
	// Repository is the provider full repo name, for the prompt.
	Repository string `json:"repository"`
	// Ref is the commit the implementation starts from (the task
	// commit).
	Ref string `json:"ref"`
	// Task is the backlog task to implement.
	Task Task `json:"task"`
	// Context is the repository context assembled so far: the seed from
	// the task and everything the previous rounds' requests added.
	Context Context `json:"context"`
	// History is the conversation so far: the seed prompt, then each
	// request round's reply and its results, oldest first. It is passed
	// back so a retried activity replays the same conversation.
	History []llm.Message `json:"history,omitempty"`
	// Round is the 1-based number of this request round.
	Round int `json:"round"`
	// MaxRounds is the number of request rounds the workflow allows, for
	// the prompt.
	MaxRounds int `json:"max_rounds"`
	// BudgetBytes is the aggregate context budget, for the prompt.
	BudgetBytes int `json:"budget_bytes"`
}

// ContextRound is the result of one RequestContext round: either the
// model's context requests (the workflow executes them and runs another
// round), or its implementation (the conversation is over).
type ContextRound struct {
	// Requests are the context requests the model made this round. Empty
	// when the model proposed the implementation instead.
	Requests []ContextRequest `json:"requests,omitempty"`
	// Implementation is the model's proposal, when it made one. Nil when
	// it made requests.
	Implementation *Implementation `json:"implementation,omitempty"`
	// PromptVersion is the implementation prompt version that produced
	// the round.
	PromptVersion string `json:"prompt_version"`
	// Model is the model that answered, as the provider reports it.
	Model string `json:"model,omitempty"`
}

// RequestContext runs one round of the context conversation: it sends the
// task and the context assembled so far to the LLM and returns either
// the model's context requests or its implementation. It runs as a
// Temporal activity so the LLM API call stays out of the workflow body. A
// reply that is neither a valid request set nor a valid implementation
// fails the activity: the workflow must not act on a reply it cannot
// interpret.
func (a *Activities) RequestContext(ctx context.Context, in RequestContextInput) (ContextRound, error) {
	if a.llm == nil {
		return ContextRound{}, errors.New("taskimplementation: no llm client configured")
	}
	if strings.TrimSpace(in.Task.ID) == "" {
		return ContextRound{}, errors.New("request context: task id is required")
	}
	if in.Round < 1 {
		return ContextRound{}, fmt.Errorf("request context: round %d is not a request round", in.Round)
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("requesting implementation context",
			"task", in.Task.ID, "repository", in.Repository, "ref", ShortSHA(in.Ref),
			"round", in.Round, "files", len(in.Context.Files), "history", len(in.History))
	}

	messages := append(append([]llm.Message(nil), in.History...), llm.Message{
		Role:    llm.RoleUser,
		Content: contextRoundPrompt(in),
	})
	var reply ImplementationReply
	resp, err := a.llm.ChatJSON(ctx, ImplementationPrompt, messages, &reply)
	if err != nil {
		return ContextRound{}, fmt.Errorf("request context for %s: %w", in.Task.ID, err)
	}
	round, err := contextRoundFromReply(in.Task.ID, reply)
	if err != nil {
		return ContextRound{}, fmt.Errorf("request context for %s: %w", in.Task.ID, err)
	}
	round.PromptVersion = ImplementationPromptVersion
	round.Model = resp.Model
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("requested implementation context",
			"task", in.Task.ID, "round", in.Round,
			"requests", len(round.Requests), "proposed", round.Implementation != nil)
	}
	return round, nil
}

// contextRoundFromReply validates the model's reply to a request round and
// converts it into a ContextRound: the context requests, or the
// implementation. The pure logic of RequestContext, separated so unit
// tests can exercise it without an LLM.
func contextRoundFromReply(taskID string, reply ImplementationReply) (ContextRound, error) {
	if ImplementationOutcome(strings.ToLower(strings.TrimSpace(reply.Outcome))) == OutcomeRequest {
		requests, err := contextRequestsFromReply(reply.Requests)
		if err != nil {
			return ContextRound{}, err
		}
		return ContextRound{Requests: requests}, nil
	}
	impl, err := implementationFromReply(taskID, reply)
	if err != nil {
		return ContextRound{}, err
	}
	return ContextRound{Implementation: &impl}, nil
}

// contextRequestsFromReply validates the requests of a request reply: at
// least one request, each a known kind with the fields that kind needs,
// and no more than maxContextRequestItems files or search matches in one
// request.
func contextRequestsFromReply(requests []ContextRequest) ([]ContextRequest, error) {
	if len(requests) == 0 {
		return nil, errors.New("reply reports a context request without any requests")
	}
	out := make([]ContextRequest, 0, len(requests))
	for _, request := range requests {
		normalized, err := normalizeContextRequest(request)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

// normalizeContextRequest validates one request and trims its fields.
func normalizeContextRequest(request ContextRequest) (ContextRequest, error) {
	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	switch kind {
	case ContextRequestList:
		path := strings.TrimSpace(request.Path)
		if path != "" {
			if err := validateRepoPath(path); err != nil {
				return ContextRequest{}, fmt.Errorf("list request: %w", err)
			}
		}
		return ContextRequest{Kind: kind, Path: path}, nil
	case ContextRequestSearch:
		query := strings.TrimSpace(request.Query)
		if query == "" {
			return ContextRequest{}, errors.New("search request has no query")
		}
		return ContextRequest{Kind: kind, Query: query}, nil
	case ContextRequestRead:
		var paths []string
		seen := make(map[string]bool)
		for _, path := range request.Paths {
			path = strings.TrimSpace(path)
			if path == "" || seen[path] {
				continue
			}
			if err := validateRepoPath(path); err != nil {
				return ContextRequest{}, fmt.Errorf("read request: %w", err)
			}
			seen[path] = true
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			return ContextRequest{}, errors.New("read request has no paths")
		}
		if len(paths) > maxContextRequestItems {
			return ContextRequest{}, fmt.Errorf("read request asks for %d files: at most %d per request",
				len(paths), maxContextRequestItems)
		}
		return ContextRequest{Kind: kind, Paths: paths}, nil
	default:
		return ContextRequest{}, fmt.Errorf("request has unknown kind %q: want %q, %q, or %q",
			request.Kind, ContextRequestList, ContextRequestSearch, ContextRequestRead)
	}
}

// Context is the repository context the workflow assembles for the
// implementation: the seed chosen from the task, plus everything the
// model requested. It is the workflow's own state, carried as activity
// input, so a replayed workflow rebuilds the same context.
type Context struct {
	// Files lists the repository's files, sorted by path: the whole
	// listing when it fit the budget, otherwise the files the context
	// carries.
	Files []FileEntry `json:"files,omitempty"`
	// Truncated reports that Files is only part of the repository
	// listing.
	Truncated bool `json:"truncated,omitempty"`
	// Total is the number of files the provider reported.
	Total int `json:"total,omitempty"`
	// Contents are the file contents the model has seen, sorted by path.
	Contents []ReadFile `json:"contents,omitempty"`
	// Notes record what the context requests added and what they could
	// not: missing files, truncated listings, refused requests. They are
	// rendered into the prompt so the model knows what it has and what
	// it does not.
	Notes []string `json:"notes,omitempty"`
}

// Bytes is the aggregate size of the context's file contents and file
// listing, the quantity maxContextBudgetBytes bounds.
func (c Context) Bytes() int {
	total := 0
	for _, f := range c.Contents {
		total += len(f.Content)
	}
	for _, f := range c.Files {
		total += len(f.Path) + 1
	}
	return total
}

// hasContent reports whether the context already carries the file.
func (c Context) hasContent(path string) bool {
	for _, f := range c.Contents {
		if f.Path == path {
			return true
		}
	}
	return false
}

// addFile adds one file to the context, replacing an earlier copy of the
// same path and keeping Contents sorted by path.
func (c *Context) addFile(file ReadFile) {
	for i, f := range c.Contents {
		if f.Path == file.Path {
			c.Contents[i] = file
			return
		}
	}
	c.Contents = append(c.Contents, file)
	sort.Slice(c.Contents, func(i, j int) bool { return c.Contents[i].Path < c.Contents[j].Path })
}

// mergeFiles adds the files a read request returned.
func (c *Context) mergeFiles(files []ReadFile) {
	for _, f := range files {
		c.addFile(f)
	}
}

// mergeListing replaces the context's listing with a narrower one (a
// listed directory or a search result). The listing is prompt material,
// not evidence: the files the model read stay.
func (c *Context) mergeListing(listing RepositoryListing) {
	c.Files = listing.Files
	c.Truncated = listing.Truncated
	c.Total = listing.Total
}

// note records a context note, skipping duplicates.
func (c *Context) note(note string) {
	for _, n := range c.Notes {
		if n == note {
			return
		}
	}
	c.Notes = append(c.Notes, note)
}

// ContextRefusal is a context request the workflow refused because it
// would exceed the aggregate context budget.
type ContextRefusal struct {
	// Request is the refused request.
	Request ContextRequest `json:"request"`
	// Reason explains the refusal.
	Reason string `json:"reason"`
}

// String renders the refusal for prompts and outcome reasons.
func (r ContextRefusal) String() string {
	return fmt.Sprintf("%s: %s", r.Request, r.Reason)
}

// executeContextRequests runs the model's context requests against the
// repository and merges their results into the context, within the
// aggregate budget. It is the workflow-side half of the context loop; it
// only sequences activities, so it stays deterministic. A request that
// would push the context past maxContextBudgetBytes is refused (and
// returned as a ContextRefusal), never failed; a provider or activity
// failure is returned as an error.
func executeContextRequests(ctx workflow.Context, in Input, task Task, c *Context, requests []ContextRequest) ([]ContextRefusal, error) {
	var refusals []ContextRefusal
	for _, request := range requests {
		switch request.Kind {
		case ContextRequestList:
			var listing RepositoryListing
			if err := workflow.ExecuteActivity(ctx, ListRepositoryName, ListRepositoryInput{
				Repository: in.Repository,
				Path:       request.Path,
				Ref:        in.SHA,
			}).Get(ctx, &listing); err != nil {
				return nil, fmt.Errorf("list %q for %s: %w", request.Path, task.ID, err)
			}
			// A listing replaces the previous one, so its size is what
			// the budget gains.
			gained := listingBytes(listing) - listingBytes(RepositoryListing{Files: c.Files})
			if c.Bytes()+gained > maxContextBudgetBytes {
				refusals = append(refusals, refusal(request, c, gained))
				continue
			}
			c.mergeListing(listing)
			note := fmt.Sprintf("%s: %d file(s)", request, len(listing.Files))
			if listing.Truncated {
				note += fmt.Sprintf(" of %d (the listing is truncated; narrow the path)", listing.Total)
			}
			c.note(note)
		case ContextRequestSearch:
			var result SearchResult
			if err := workflow.ExecuteActivity(ctx, SearchRepositoryName, SearchRepositoryInput{
				Repository: in.Repository,
				Query:      request.Query,
				Ref:        in.SHA,
			}).Get(ctx, &result); err != nil {
				return nil, fmt.Errorf("search %q for %s: %w", request.Query, task.ID, err)
			}
			listing := RepositoryListing{Files: result.Files, Total: result.Total, Truncated: result.Truncated}
			gained := listingBytes(listing) - listingBytes(RepositoryListing{Files: c.Files})
			if c.Bytes()+gained > maxContextBudgetBytes {
				refusals = append(refusals, refusal(request, c, gained))
				continue
			}
			c.mergeListing(listing)
			note := fmt.Sprintf("%s: %d match(es)", request, len(result.Files))
			if result.Truncated {
				note += fmt.Sprintf(" of %d (the results are truncated; narrow the query)", result.Total)
			}
			c.note(note)
		case ContextRequestRead:
			var paths []string
			var missing []string
			for _, path := range request.Paths {
				switch {
				case path == task.Path:
					missing = append(missing, path+" (the task file is already in the prompt)")
				case c.hasContent(path):
					missing = append(missing, path+" (already in the context)")
				case len(paths) >= maxContextRequestItems:
					missing = append(missing, path+" (the per-request file limit was reached)")
				default:
					paths = append(paths, path)
				}
			}
			if len(paths) == 0 {
				c.note(fmt.Sprintf("%s: nothing new to read", request))
				continue
			}
			var files []ReadFile
			if err := workflow.ExecuteActivity(ctx, ReadFilesName, ReadFilesInput{
				Repository: in.Repository,
				Paths:      paths,
				Ref:        in.SHA,
			}).Get(ctx, &files); err != nil {
				return nil, fmt.Errorf("read files for %s: %w", task.ID, err)
			}
			read := make(map[string]bool, len(files))
			var accepted []ReadFile
			for _, file := range files {
				read[file.Path] = true
				if c.Bytes()+len(file.Content) > maxContextBudgetBytes {
					refusals = append(refusals, refusal(
						ContextRequest{Kind: ContextRequestRead, Paths: []string{file.Path}}, c, len(file.Content)))
					continue
				}
				accepted = append(accepted, file)
			}
			c.mergeFiles(accepted)
			for _, path := range paths {
				if !read[path] {
					missing = append(missing, path+" (does not exist at "+ShortSHA(in.SHA)+")")
				}
			}
			c.note(fmt.Sprintf("%s: read %d file(s)", request, len(accepted)))
			for _, m := range missing {
				c.note("not provided: " + m)
			}
		}
	}
	return refusals, nil
}

// refusal builds the refusal of one request that would exceed the budget.
func refusal(request ContextRequest, c *Context, gained int) ContextRefusal {
	return ContextRefusal{
		Request: request,
		Reason: fmt.Sprintf("it needs %d more byte(s) and the context already holds %d of its %d-byte budget",
			gained, c.Bytes(), maxContextBudgetBytes),
	}
}

// listingBytes is the size a listing contributes to the context: one
// path per line.
func listingBytes(listing RepositoryListing) int {
	total := 0
	for _, f := range listing.Files {
		total += len(f.Path) + 1
	}
	return total
}

// contextRoundPrompt renders the user turn of one request round: the
// context assembled so far and the round's place in the loop. The seed
// prompt (seedPrompt) is the first user turn of the conversation and
// carries the task itself.
func contextRoundPrompt(in RequestContextInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Context round %d of %d\n\n", in.Round, in.MaxRounds)
	if in.Round >= in.MaxRounds {
		fmt.Fprintf(&b, "This is the last context round: it is your final chance to request files. The next round is propose-only and cannot issue requests.\n\n")
	} else {
		fmt.Fprintf(&b, "Request more repository context if you need it, or propose the implementation now. You have at most %d more request round(s) after this one.\n\n",
			in.MaxRounds-in.Round)
	}
	fmt.Fprintf(&b, "The context holds %d of its %d-byte budget.\n\n", in.Context.Bytes(), in.BudgetBytes)
	b.WriteString(renderContext(in.Context, in.Task.Path))
	return b.String()
}

// renderContext renders the context's listing, file contents, and notes.
// The task's own file is never rendered as content: it is already in the
// prompt.
func renderContext(c Context, taskPath string) string {
	var b strings.Builder

	b.WriteString("## Repository files\n\n")
	if len(c.Files) == 0 {
		b.WriteString("(no file listing was provided)\n\n")
	} else {
		fmt.Fprintf(&b, "%d file(s) listed", len(c.Files))
		if c.Truncated {
			b.WriteString(" (the listing is truncated)")
		}
		b.WriteString(":\n\n```\n")
		for _, f := range c.Files {
			b.WriteString(f.Path)
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}

	b.WriteString("## File contents\n\n")
	wrote := false
	for _, f := range c.Contents {
		if f.Path == taskPath {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n```\n", f.Path)
		b.WriteString(truncateContext(f.Content))
		if !strings.HasSuffix(f.Content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
		wrote = true
	}
	if !wrote {
		b.WriteString("(no file contents yet; request the files you need, or propose new files)\n\n")
	}

	if len(c.Notes) > 0 {
		b.WriteString("## Context notes\n\n")
		for _, note := range c.Notes {
			b.WriteString("- ")
			b.WriteString(note)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}
