package agent

import (
	"fmt"
	"os"
	"strings"

	"sidekick/internal/config"
	"sidekick/internal/run"
)

// ExpandPrompt substitutes $SIDEKICK_* run variables in a prompt template.
func ExpandPrompt(tmpl string, state run.State) string {
	return os.Expand(tmpl, func(key string) string {
		switch key {
		case "SIDEKICK_RUN_ID":
			return state.ID
		case "SIDEKICK_RUN_DIR":
			return state.RunDir
		case "SIDEKICK_TASK_FILE":
			return state.TaskFile
		case "SIDEKICK_PLAN_FILE":
			return state.PlanFile
		case "SIDEKICK_MEMORY_FILE":
			return state.MemoryFile
		case "SIDEKICK_WORKTREE":
			return state.WorktreePath
		default:
			return ""
		}
	})
}

func PlannerPrompt(state run.State, a config.AgentConfig) string {
	if strings.TrimSpace(a.Prompt) != "" {
		return ExpandPrompt(a.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick planning task (interactive)

You are the planning agent for Sidekick run %s. This is a back-and-forth chat
with the human. Discuss and refine the plan with them until they are satisfied.

Read the task in:
%s

Prior Sidekick runs recorded lessons for this repo at:
%s
If it exists, read it first for context and known pitfalls.

Produce a concrete, reachable implementation plan an implementation agent can
execute without further human back-and-forth:
- Goal statement.
- Assumptions.
- Ordered implementation steps.
- Validation steps.
- Risks or decisions that still require the human.

When the human is happy, WRITE the final plan to this file (path is also in
$SIDEKICK_PLAN_FILE):
%s

Do not edit any other files -- write only the plan file. Once the plan file is
saved, tell the human they can release the implementer WITHOUT quitting you:
run "sidekick release" (or "sidekick abort" to cancel) in any other pane. Quitting
you and answering the [y/N] prompt also works, but is not required.
`, state.ID, state.TaskFile, state.MemoryFile, state.PlanFile)
}

func ImplementerPrompt(state run.State, a config.AgentConfig) string {
	if strings.TrimSpace(a.Prompt) != "" {
		return ExpandPrompt(a.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick implementation task

You are the implementation agent for Sidekick run %s.

Task file:
%s

Plan file:
%s

Prior Sidekick runs recorded lessons for this repo at:
%s
If it exists, read it first for context and known pitfalls.

Work in this isolated worktree:
%s

Execute the plan with the smallest correct change. Keep the worktree reviewable:
- Inspect the repository before editing.
- Reuse local patterns.
- Run relevant validation.
- Do not commit unless the task explicitly requires it.
- Write a concise outcome summary to stdout, including validation results.
`, state.ID, state.TaskFile, state.PlanFile, state.MemoryFile, state.WorktreePath)
}

func ReviewerPrompt(state run.State, a config.AgentConfig) string {
	if strings.TrimSpace(a.Prompt) != "" {
		return ExpandPrompt(a.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick review task

You are %s reviewing Sidekick run %s.

Task file:
%s

Plan file:
%s

Review the git changes in this worktree:
%s

Prior Sidekick runs recorded lessons for this repo at:
%s
If it exists, read it first for context and known pitfalls.

Use a code-review stance. Prioritize bugs, regressions, security issues, missing tests, and mismatches with the task or plan.

Required output:
- Findings first, with file and line references when possible.
- Then residual risks or test gaps.
- Then a brief conclusion.

Do not edit files during review.

End with exactly one line and nothing after it:
SIDEKICK_VERDICT: approve
or
SIDEKICK_VERDICT: revise
`, a.Name, state.ID, state.TaskFile, state.PlanFile, state.WorktreePath, state.MemoryFile)
}

func LearnPrompt(state run.State, a config.AgentConfig) string {
	if strings.TrimSpace(a.Prompt) != "" {
		return ExpandPrompt(a.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick repo learning task

You are the learning agent for Sidekick run %s.

Update this repo memory file only:
%s

Read these run inputs and outputs:
- Task file: %s
- Plan file: %s
- Implementer log: %s
- Worktree status: git -C %s status --short
- Worktree diff: git -C %s diff

Create the memory file if it does not exist. Keep it as Markdown with these sections:

## Repo insights

Durable, generally useful conventions, skills, pitfalls, commands, architecture notes, and validation facts for future Sidekick runs in this repo. Merge new insights into this section, edit existing bullets in place when needed, keep it concise, and avoid duplicates.

## Runs

Append a dated entry for this run. Include:
- Run id: %s
- Goal from the task
- Files touched, based on git status/diff
- Validation outcome from the implementer log or diff context
- Any follow-up risk worth remembering

Do not edit any file except the memory file. If there is no durable insight, still append the run entry and leave Repo insights concise.
`, state.ID, state.MemoryFile, state.TaskFile, state.PlanFile, state.ImplementerLog(), state.WorktreePath, state.WorktreePath, state.ID)
}

func GatePrompt(state run.State) string {
	return fmt.Sprintf(`# Sidekick gate task

Run the configured no-mistakes gate for Sidekick run %s after implementation.

Worktree:
%s
`, state.ID, state.WorktreePath)
}
