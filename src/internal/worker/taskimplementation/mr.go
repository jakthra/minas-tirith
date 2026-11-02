package taskimplementation

import (
	"fmt"
	"strings"
)

// This file holds the implementation agent's merge request: the title and
// description of the MR that carries an implementation, and the refiner
// hand-off a partial implementation posts on it. The MR is the visible
// output of the agent — a regular MR for a complete implementation, a
// draft MR for a partial one — so its text is built by pure functions
// here and reviewed like the prompt. The submission itself (provider
// calls) lives in mrsubmission.go.

// RefinerAgentHandle is the handle the implementation agent tags on a
// draft MR when it cannot fulfill the task. The tag starts a refinement
// round through the refinement agent's existing merge request comment
// loop (TASK-11): the agent reads the comment, answers the open
// questions, and applies the resulting changes to the MR's branch.
const RefinerAgentHandle = "@refiner"

// MergeRequestContent is everything the implementation merge request
// title and description summarize: the implementation outcome, the task
// it was produced for, and the commit that triggered it.
type MergeRequestContent struct {
	// Implementation is the outcome the MR carries.
	Implementation TaskImplementation
	// Repository is the provider full repo name, for the task link.
	Repository string
	// Branch is the branch the task commit landed on: the MR's base.
	Branch string
	// CommitSHA is the triggering commit's hash.
	CommitSHA string
	// CommitMessage is the triggering commit's message.
	CommitMessage string
	// CommitAuthor is the triggering commit's author.
	CommitAuthor string
	// ProviderURL is the provider's web base URL (e.g.
	// "http://localhost:3000"), used to link the task file. Empty when
	// unknown; the description then names the path without a link.
	ProviderURL string
}

// buildMergeRequestTitle returns the MR title: the task id and title,
// so the ticket number is visible in the MR list. A partial
// implementation is marked as such; the draft state carries the same
// meaning in the provider UI.
func buildMergeRequestTitle(c MergeRequestContent) string {
	title := strings.TrimSpace(c.Implementation.TaskTitle)
	if title == "" {
		title = taskFileStem(c.Implementation.TaskPath)
	}
	id := strings.TrimSpace(c.Implementation.TaskID)
	if id == "" {
		id = taskFileStem(c.Implementation.TaskPath)
	}

	var b strings.Builder
	if c.Implementation.Complete {
		b.WriteString("Implement ")
	} else {
		b.WriteString("Implement (draft) ")
	}
	b.WriteString(id)
	if title != "" {
		b.WriteString(": ")
		b.WriteString(title)
	}
	return b.String()
}

// buildMergeRequestDescription returns the MR body: the task the MR
// implements (linked when the provider URL is known), the outcome, the
// changed files, and — for a partial implementation — the reason it
// could not be fulfilled and the open questions the refiner must answer.
func buildMergeRequestDescription(c MergeRequestContent) string {
	impl := c.Implementation
	id := strings.TrimSpace(impl.TaskID)
	if id == "" {
		id = taskFileStem(impl.TaskPath)
	}
	title := strings.TrimSpace(impl.TaskTitle)
	link := taskLink(c.ProviderURL, c.Repository, impl.TaskPath)

	var b strings.Builder
	switch {
	case title != "" && link != "":
		fmt.Fprintf(&b, "Implements backlog task [**%s: %s**](%s) (`%s`).\n\n", id, title, link, impl.TaskPath)
	case title != "":
		fmt.Fprintf(&b, "Implements backlog task **%s: %s** (`%s`).\n\n", id, title, impl.TaskPath)
	case link != "":
		fmt.Fprintf(&b, "Implements backlog task [**%s**](%s) (`%s`).\n\n", id, link, impl.TaskPath)
	default:
		fmt.Fprintf(&b, "Implements backlog task **%s** (`%s`).\n\n", id, impl.TaskPath)
	}
	fmt.Fprintf(&b, "Triggered by commit `%s` \"%s\" by %s on branch `%s`.\n\n",
		ShortSHA(c.CommitSHA), FirstLine(c.CommitMessage), c.CommitAuthor, c.Branch)

	if impl.Complete {
		b.WriteString("The implementation agent reports this task as **complete**: every acceptance criterion is implemented.\n")
	} else {
		b.WriteString("The implementation agent could not fulfill this task, so this is a **draft** merge request.\n\n")
		b.WriteString("## Why it could not be fulfilled\n\n")
		reason := strings.TrimSpace(impl.Reason)
		if reason == "" {
			reason = "the implementation agent gave no reason."
		}
		fmt.Fprintf(&b, "%s\n", reason)
		if len(impl.OpenQuestions) > 0 {
			b.WriteString("\n## Open questions\n\n")
			for i, q := range impl.OpenQuestions {
				fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(q))
			}
		}
	}

	if len(impl.ChangedFiles) > 0 {
		fmt.Fprintf(&b, "\n## Changes\n\n")
		for _, f := range impl.ChangedFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
	} else {
		b.WriteString("\nNo safe change was proposed; the branch only carries the task commit.\n")
	}

	if !impl.Complete {
		fmt.Fprintf(&b, "\n%s, please refine this task with the open questions above; "+
			"your answer is applied to this merge request's branch.\n", RefinerAgentHandle)
	}
	return b.String()
}

// buildRefinerHandOff returns the comment that hands a partial
// implementation to the refiner agent: the tag that starts a refinement
// round, the reason, and the open questions. It is empty for a complete
// implementation, which never involves the refiner.
func buildRefinerHandOff(c MergeRequestContent) string {
	impl := c.Implementation
	if impl.Complete {
		return ""
	}
	id := strings.TrimSpace(impl.TaskID)
	if id == "" {
		id = taskFileStem(impl.TaskPath)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: the implementation agent could not fulfill **%s** and opened this draft merge request.\n\n",
		RefinerAgentHandle, id)
	reason := strings.TrimSpace(impl.Reason)
	if reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n\n", reason)
	}
	if len(impl.OpenQuestions) > 0 {
		b.WriteString("Open questions:\n\n")
		for _, q := range impl.OpenQuestions {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(q))
		}
		b.WriteString("\n")
	}
	b.WriteString("Please refine the task with these questions answered, so the implementation can be completed on this branch.")
	return b.String()
}

// taskLink returns a provider URL for the task file, or "" when the
// provider URL or repository is unknown.
func taskLink(providerURL, repository, path string) string {
	base := strings.TrimRight(strings.TrimSpace(providerURL), "/")
	if base == "" || strings.TrimSpace(repository) == "" || path == "" {
		return ""
	}
	return base + "/" + repository + "/src/branch/HEAD/" + strings.ReplaceAll(path, " ", "%20")
}

// taskFileStem returns a task file's name without directory or .md
// extension, e.g. "task-24 - Submit-the-implementation-MR".
func taskFileStem(path string) string {
	stem := path
	if i := strings.LastIndex(stem, "/"); i >= 0 {
		stem = stem[i+1:]
	}
	return strings.TrimSuffix(stem, ".md")
}
