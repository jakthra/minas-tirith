package taskimplementation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.temporal.io/sdk/activity"

	"minas-tirith/internal/llm"
)

// This file holds the implementation agent's reasoning step: the
// GenerateImplementation activity, which sends the task, the repository
// context, and the implementation prompt to the LLM and returns the
// proposed file changes together with the model's structured outcome.
// The LLM API call lives here, never in the workflow body, so the
// workflow stays deterministic.

// maxContextFileBytes bounds the content of one file sent to the model,
// so a large generated or vendored file cannot crowd the task out of the
// prompt. Larger files are truncated with a marker, and the model is
// told to report a partial outcome when it needs the rest.
const maxContextFileBytes = 64 << 10

// maxContextFiles bounds how many files one GenerateImplementation call
// sends, keeping the prompt within the model's context window.
const maxContextFiles = 40

// ImplementationChange is one file the model proposes to write: the
// repository-relative path and the complete new file content.
type ImplementationChange struct {
	// Path is the file's path from the repository root.
	Path string `json:"path"`
	// Content is the complete new file content.
	Content string `json:"content"`
}

// GenerateImplementationInput is the input to the GenerateImplementation
// activity: everything the model needs to implement the task.
type GenerateImplementationInput struct {
	// Repository is the provider full repo name, for the prompt.
	Repository string `json:"repository"`
	// Ref is the commit the implementation starts from (the task
	// commit).
	Ref string `json:"ref"`
	// Task is the backlog task to implement.
	Task Task `json:"task"`
	// Files lists the repository's files (the relevant ones, or the
	// whole listing when nothing narrower could be chosen).
	Files []FileEntry `json:"files,omitempty"`
	// Contents are the existing contents of the relevant files.
	Contents []ReadFile `json:"contents,omitempty"`
	// Truncated reports that Files is only part of the repository
	// listing.
	Truncated bool `json:"truncated,omitempty"`
}

// Implementation is the result of the GenerateImplementation activity:
// the model's proposed changes and its structured outcome.
type Implementation struct {
	// TaskID is the implemented task's id, e.g. "TASK-23".
	TaskID string `json:"task_id"`
	// Complete reports whether the model fulfilled the task. When
	// false, Reason and OpenQuestions explain why.
	Complete bool `json:"complete"`
	// Reason explains a partial outcome; empty for a complete one.
	Reason string `json:"reason,omitempty"`
	// OpenQuestions lists the questions blocking a complete
	// implementation; empty for a complete one.
	OpenQuestions []string `json:"open_questions,omitempty"`
	// Changes are the files to write, in the model's order.
	Changes []ImplementationChange `json:"changes,omitempty"`
	// PromptVersion is the implementation prompt version that
	// produced the result.
	PromptVersion string `json:"prompt_version"`
	// Model is the model that answered, as the provider reports it.
	Model string `json:"model,omitempty"`
}

// GenerateImplementation asks the LLM to implement the task against the
// repository context and returns its proposed file changes with the
// structured outcome. It runs as a Temporal activity so the LLM API call
// stays out of the workflow body. A reply that is not a valid
// implementation (unknown outcome, a partial outcome without a reason,
// a change outside the repository, the same path twice) fails the
// activity: the workflow must not commit a change set it cannot
// interpret.
func (a *Activities) GenerateImplementation(ctx context.Context, in GenerateImplementationInput) (Implementation, error) {
	if a.llm == nil {
		return Implementation{}, errors.New("taskimplementation: no llm client configured")
	}
	if strings.TrimSpace(in.Task.ID) == "" {
		return Implementation{}, errors.New("generate implementation: task id is required")
	}
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("generating implementation",
			"task", in.Task.ID, "repository", in.Repository, "ref", ShortSHA(in.Ref),
			"files", len(in.Files), "contents", len(in.Contents))
	}

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: implementationUserPrompt(in)},
	}
	var reply ImplementationReply
	resp, err := a.llm.ChatJSON(ctx, ImplementationPrompt, messages, &reply)
	if err != nil {
		return Implementation{}, fmt.Errorf("generate implementation for %s: %w", in.Task.ID, err)
	}
	impl, err := implementationFromReply(in.Task.ID, reply)
	if err != nil {
		return Implementation{}, fmt.Errorf("generate implementation for %s: %w", in.Task.ID, err)
	}
	impl.PromptVersion = ImplementationPromptVersion
	impl.Model = resp.Model
	if activity.IsActivity(ctx) {
		logger := activity.GetLogger(ctx)
		logger.Info("generated implementation",
			"task", in.Task.ID, "complete", impl.Complete,
			"changes", len(impl.Changes), "questions", len(impl.OpenQuestions))
	}
	return impl, nil
}

// implementationFromReply validates the model's reply and converts it
// into an Implementation. It is the pure logic of GenerateImplementation,
// separated so unit tests can exercise it without an LLM.
func implementationFromReply(taskID string, reply ImplementationReply) (Implementation, error) {
	impl := Implementation{TaskID: taskID}
	switch ImplementationOutcome(strings.ToLower(strings.TrimSpace(reply.Outcome))) {
	case OutcomeComplete:
		impl.Complete = true
	case OutcomePartial:
		impl.Complete = false
	default:
		return Implementation{}, fmt.Errorf("reply has unknown outcome %q: want %q or %q",
			reply.Outcome, OutcomeComplete, OutcomePartial)
	}
	impl.Reason = strings.TrimSpace(reply.Reason)
	if !impl.Complete && impl.Reason == "" {
		return Implementation{}, errors.New("reply reports a partial outcome without a reason")
	}

	seen := make(map[string]bool, len(reply.Changes))
	for _, change := range reply.Changes {
		if err := validateRepoPath(change.Path); err != nil {
			return Implementation{}, fmt.Errorf("reply proposes an invalid change: %w", err)
		}
		if seen[change.Path] {
			return Implementation{}, fmt.Errorf("reply proposes %q twice", change.Path)
		}
		seen[change.Path] = true
		impl.Changes = append(impl.Changes, ImplementationChange{
			Path:    change.Path,
			Content: change.Content,
		})
	}
	for _, q := range reply.OpenQuestions {
		if q = strings.TrimSpace(q); q != "" {
			impl.OpenQuestions = append(impl.OpenQuestions, q)
		}
	}
	return impl, nil
}

// implementationUserPrompt renders the user turn of the implementation
// chat: the task, the repository listing, and the relevant file
// contents, in that order (the task first, so it survives any
// truncation of the context).
func implementationUserPrompt(in GenerateImplementationInput) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Task %s: %s\n\n", in.Task.ID, in.Task.Title)
	fmt.Fprintf(&b, "Repository: %s\nStarting commit: %s\nFile: %s\n\n", in.Repository, in.Ref, in.Task.Path)
	b.WriteString("## Task file\n\n")
	b.WriteString(strings.TrimSpace(in.Task.Content))
	b.WriteString("\n\n")

	b.WriteString("## Repository files\n\n")
	if len(in.Files) == 0 {
		b.WriteString("(no file listing was provided)\n\n")
	} else {
		fmt.Fprintf(&b, "%d file(s) listed", len(in.Files))
		if in.Truncated {
			b.WriteString(" (the listing is truncated)")
		}
		b.WriteString(":\n\n```\n")
		for _, f := range in.Files {
			b.WriteString(f.Path)
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}

	b.WriteString("## File contents\n\n")
	if len(in.Contents) == 0 {
		b.WriteString("(none of the files were provided; propose new files, or report a partial outcome when you need to see existing code)\n")
		return b.String()
	}
	for _, f := range in.Contents {
		fmt.Fprintf(&b, "### %s\n\n```\n", f.Path)
		b.WriteString(truncateContext(f.Content))
		if !strings.HasSuffix(f.Content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}
	return b.String()
}

// truncateContext bounds one file's content to maxContextFileBytes on a
// rune boundary, marking the cut so the model knows the file continues.
func truncateContext(content string) string {
	if len(content) <= maxContextFileBytes {
		return content
	}
	cut := maxContextFileBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + "\n[... file truncated: it is longer than the prompt allows ...]"
}
