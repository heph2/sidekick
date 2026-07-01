# Sidekick Agent Notes

Sidekick is a local orchestration CLI for agentic development workflows. It coordinates planning, implementation, review, and optional validation while keeping the human in one tmux workspace.

## Current Architecture

- Language: Go, single-package CLI in `main.go`.
- Dev shell: `flake.nix`.
- Tests: Go unit tests in `main_test.go`.
- Runtime state: target repositories get an optional `.sidekick/config.json` and `.sidekick/runs/<id>/`.
- Entry: bare `sidekick` (no subcommand) runs the orchestration from the current repo, prompting for the task and auto-attaching. `sidekick run` is the same path with explicit flags.
- Orchestration: leases a Treehouse worktree when available (otherwise a plain git worktree under `.sidekick/worktrees/<id>`), creates a tmux session, and starts planner, dashboard, implementer, reviewer, optional gate, and land windows.

## Workflow

- Planner is an interactive `claude` chat in its pane (agent `Interactive: true`): the human refines the plan, claude writes `plan.md`, and on exit Sidekick asks whether to release the implementer. Approval writes `planner.done`; declining writes `planner.done.failed` (aborts downstream).
- Implementer waits for `planner.done`, then works autonomously inside the isolated worktree.
- Reviewers wait for `implement.done`, then review the worktree diff.
- Optional gate runs the configured `no-mistakes` command after implementation.
- Land window waits for `implement.done` (and `gate.done` when gated), commits the worktree, then prompts before pushing the branch and opening a PR via `gh`. Skip with `--no-land`.
- Dashboard runs `sidekick status --watch` and renders the goal, phase, pipeline state, artifacts, recent logs, and ASCII Sidekick mascot, with ANSI color on a TTY (honors `NO_COLOR`).

## Conventions

- Keep changes small and compatible with configurable agent harnesses.
- Do not hard-code `claude`, `cc`, `codex`, Treehouse, or `no-mistakes` flags beyond defaults; use `.sidekick/config.json` for local harness details.
- Do not commit generated binaries such as `bin/sidekick`.
- Preserve ASCII-only source files unless a change explicitly requires otherwise.
- Run `gofmt -w` on Go files, then `go test ./...` and `go build -o bin/sidekick .`.

## Product Direction

Sidekick should feel like a support-console companion: the human is the hero, and Sidekick keeps the agent loop visible and moving. The current mascot is an original wood-hero inspired ASCII mark based on the requested Kamui Woods direction, not copied artwork.
