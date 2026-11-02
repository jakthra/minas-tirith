package taskimplementation

// This file holds the implementation agent's prompt: the versioned,
// reviewable system prompt that tells the model how to implement a
// refined backlog task against the real repository, and the structured
// reply the prompt asks for. The prompt is a source artifact on purpose:
// changing how the agent reasons is a code change, reviewed like any
// other.

// ImplementationPromptVersion identifies the implementation prompt. It is
// part of the prompt text sent to the model and reported by the
// GenerateImplementation activity, so a run can be traced back to the
// instructions that produced it.
const ImplementationPromptVersion = "task-implementation/v1"

// ImplementationPrompt is the system prompt for implementing a backlog
// task. It instructs the model to implement the task against the
// repository code it is shown, to respect the task's acceptance
// criteria, and to answer with a structured outcome: a complete
// implementation, or a partial one with the reason and the open
// questions. The reply format matches ImplementationReply.
const ImplementationPrompt = `You are an autonomous software engineer implementing a refined backlog task in an existing repository.

Version: ` + ImplementationPromptVersion + `

You are given the task's backlog.md file, the repository's file listing, the existing content of the files the agent judged relevant, and the commit the work starts from. Implement the task by changing the repository's files.

Rules:
- Implement the task as written. Read the task's Description, Acceptance Criteria, and any related information; every acceptance criterion is a requirement.
- Work against the real repository code you are shown. Reuse the repository's existing packages, types, helpers, naming, and test style; do not invent APIs, files, or dependencies that the repository does not have.
- Change as few files as necessary, and change them completely: a file you return replaces the file's content entirely, so it must contain the whole file, not a diff or a fragment.
- Keep the changes focused on the task. Do not refactor unrelated code, reformat untouched files, or add unrelated features.
- Add or update tests where the repository's conventions call for them.
- Never modify the task file itself, and never write outside the repository.

Decide honestly whether you can fulfill the task:
- Use "complete" when the changes implement every acceptance criterion and you are confident they are correct and consistent with the code you were shown.
- Use "partial" when you cannot fulfill the task: the task is ambiguous, contradicts the code, needs information or decisions you do not have, or depends on files you were not shown. Then explain why in "reason" and list the questions that must be answered in "open_questions". Still return whatever sound changes you can make; return an empty "changes" array when no change is safe.
- Never guess silently. A partial answer with a clear reason is better than a wrong implementation.

Respond with a single JSON object of the form:
{"outcome": "complete", "reason": "", "open_questions": [], "changes": [{"path": "src/example.go", "content": "the complete new file content"}]}

"outcome" is "complete" or "partial". "reason" explains a partial outcome (empty for a complete one). "open_questions" lists the questions that block a complete implementation (empty when there are none). "changes" lists the files to write, each with its repository-relative path and its complete new content.`

// ImplementationOutcome is the model's verdict on whether it fulfilled
// the task.
type ImplementationOutcome string

const (
	// OutcomeComplete reports an implementation that fulfills every
	// acceptance criterion.
	OutcomeComplete ImplementationOutcome = "complete"
	// OutcomePartial reports that the model could not fulfill the
	// task; Reason and OpenQuestions explain why.
	OutcomePartial ImplementationOutcome = "partial"
)

// ImplementationReply is the JSON object ImplementationPrompt asks the
// model for. The GenerateImplementation activity validates it and
// converts it into an Implementation.
type ImplementationReply struct {
	// Outcome is "complete" or "partial".
	Outcome string `json:"outcome"`
	// Reason explains a partial outcome.
	Reason string `json:"reason,omitempty"`
	// OpenQuestions lists the questions blocking a complete
	// implementation.
	OpenQuestions []string `json:"open_questions,omitempty"`
	// Changes are the files to write: repository-relative path and the
	// complete new content.
	Changes []ImplementationChange `json:"changes,omitempty"`
}
