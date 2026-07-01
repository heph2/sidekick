# Sidekick Agent Notes

Sidekick is a local orchestration CLI for agentic development workflows. It coordinates planning, implementation, review, and optional validation while keeping the human in one tmux workspace.

## Current Architecture

- Language: Go, single-package CLI in `main.go`.
- Dev shell: `flake.nix`.
- Tests: Go unit tests in `main_test.go`.
- Runtime state: target repositories get an optional `.sidekick/config.json` and `.sidekick/runs/<id>/`.
- Entry: bare `sidekick` (no subcommand) opens a persistent console session (`sidekick console` running in a `console` window) that keeps prompting for tasks; `sidekick run` stays the scriptable one-shot with explicit flags (`--task`, `--no-attach`, etc.).
- Orchestration: leases a Treehouse worktree when available (otherwise a plain git worktree under `.sidekick/worktrees/<id>`), creates or reuses a tmux session, and starts planner, dashboard, implementer, reviewer, optional gate, and land windows for each run. Inside the console, every run's windows are prefixed (`t1-planner`, `t2-planner`, ...) so multiple tasks run side by side, each in its own worktree and branch.

## Workflow

- Planner is an interactive `claude` chat in its pane (agent `Interactive: true`): the human refines the plan, claude writes `plan.md`, and on exit Sidekick asks whether to release the implementer. Approval writes `planner.done`; declining writes `planner.done.failed` (aborts downstream).
- Implementer waits for `planner.done`, then works autonomously inside the isolated worktree.
- Reviewers wait for `implement.done`, then review the worktree diff.
- Optional gate runs the configured `no-mistakes` command after implementation.
- Land window waits for `implement.done` (and `gate.done` when gated), commits the worktree, then prompts before pushing the branch and opening a PR via `gh`. Skip with `--no-land`.
- Dashboard runs `sidekick status --watch` and renders the goal, phase, pipeline state, artifacts, recent logs, and the Sidekick sparkle mascot, with a truecolor gradient on a TTY (honors `NO_COLOR`).

## Conventions

- Keep changes small and compatible with configurable agent harnesses.
- Do not hard-code `claude`, `cc`, `codex`, Treehouse, or `no-mistakes` flags beyond defaults; use `.sidekick/config.json` for local harness details.
- Agent configs may set `model` as sugar for appending `--model <value>` before the prompt; use per-agent `modelFlag` when a harness needs a different flag, or keep complex cases in `command`.
- Agent configs may set `prompt` to override planner, implementer, or reviewer initial prompts; Sidekick expands `$SIDEKICK_*` run variables.
- `notify` config uses terminal bell by default and may run a user-supplied `notify.command`; desktop notification commands are not auto-detected.
- Do not commit generated binaries such as `bin/sidekick`.
- Preserve ASCII-only source files unless a change explicitly requires otherwise. The mascot is a sanctioned exception: it uses 24-bit truecolor escapes (`fg`/`mascotColored` in `main.go`) to render a warm orange-to-pink gradient, gated off exactly like `col()` (no-op under `NO_COLOR` or a non-TTY), so plain-text and piped output stay clean.
- Run `gofmt -w` on Go files, then `go test ./...` and `go build -o bin/sidekick .`.

## Product Direction

Sidekick should feel like a support-console companion: the human is the hero, and Sidekick keeps the agent loop visible and moving. The mascot is a small sparkle mark (star/dot glyphs with a warm gradient) paired with the "sidekick / always-on companion" wordmark - a lightweight visual signature rather than illustrative art.
