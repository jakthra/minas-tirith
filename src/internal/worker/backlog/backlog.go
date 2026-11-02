// Package backlog is the single decision point that classifies the backlog
// files a commit adds or modifies as backlog tasks or user stories, and
// that owns the agent-branch rules (IsAgentBranch).
//
// Several agents react to the same commits (the user story review agent
// refines stories, the task implementation agent implements tasks), so the
// classification lives here rather than in either agent: both are routed
// by it, and exactly one of them acts on each backlog file. The
// classification is a pure function of the file content (Classify); the
// activity in activities.go reads the content from the provider and
// applies it, so the trigger layer can make the routing decision before
// any workflow starts. The frontmatter type decides: "type: task" is a
// task, anything else a user story (see Classify). The id does not: the
// backlog CLI names every item TASK-NNN, stories included.
package backlog

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the repository directory holding backlog.md tasks and user
// stories.
const Dir = "backlog/tasks/"

// Agent branch prefixes: the branches the backlog agents themselves
// create and commit to. refine/ branches carry the review agent's
// generated ticket files; implement/ branches carry the implementation
// agent's work.
const (
	RefinementBranchPrefix     = "refine/"
	ImplementationBranchPrefix = "implement/"
)

// IsAgentBranch reports whether branch is one the backlog agents
// themselves maintain (refine/* or implement/*). Commits on these
// branches are agent-generated work, never new backlog files, so no
// backlog agent may start from them: acting on them would make the
// agents re-trigger on their own output in an endless loop.
func IsAgentBranch(branch string) bool {
	return strings.HasPrefix(branch, RefinementBranchPrefix) ||
		strings.HasPrefix(branch, ImplementationBranchPrefix)
}

// Kind is what a backlog file is: a task to implement, or a user story to
// refine.
type Kind string

const (
	// KindTask is a backlog task (frontmatter "type: task").
	KindTask Kind = "task"
	// KindStory is a backlog user story (frontmatter "type: feature", or
	// any type that is not a task).
	KindStory Kind = "story"
)

// IsFile reports whether path is a backlog.md file: a markdown file
// directly under backlog/tasks/ (no subdirectories).
func IsFile(path string) bool {
	if !strings.HasPrefix(path, Dir) {
		return false
	}
	rest := strings.TrimPrefix(path, Dir)
	if rest == "" || strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".md")
}

// Classify reports whether a backlog file's content is a task or a user
// story. The frontmatter type decides: "type: task" is a task and
// "type: feature" (or any other type) is a user story. The id does not
// decide — the backlog CLI names every item it creates TASK-NNN,
// stories included — so a file without a usable type is treated as a
// user story: it is the kind the refinement agent can act on, while
// implementing it would guess.
func Classify(content string) Kind {
	meta := parseFrontmatter(content)
	if meta.Type == "task" {
		return KindTask
	}
	return KindStory
}

// meta is the part of a backlog.md file's frontmatter the classification
// reads.
type meta struct {
	ID   string
	Type string
}

// parseFrontmatter extracts the id and type from a backlog.md file's YAML
// frontmatter. A file without parseable frontmatter yields empty fields.
func parseFrontmatter(content string) meta {
	var m meta
	block, ok := FrontmatterBlock(content)
	if !ok {
		return m
	}
	var fm struct {
		ID   string `yaml:"id"`
		Type string `yaml:"type"`
	}
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return m
	}
	m.ID = strings.TrimSpace(fm.ID)
	m.Type = strings.ToLower(strings.TrimSpace(fm.Type))
	return m
}

// FrontmatterBlock returns the YAML frontmatter of a backlog.md file: the
// lines between the opening and closing --- lines. It reports false when
// the content has no frontmatter.
func FrontmatterBlock(content string) (string, bool) {
	s := strings.TrimLeft(content, " \t\r\n")
	lines := strings.Split(s, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], "\r") != "---" {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			return strings.Join(lines[1:i], "\n"), true
		}
	}
	return "", false
}
