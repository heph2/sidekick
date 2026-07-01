# Sidekick Agent Notes

Sidekick is a local orchestration CLI for agentic development workflows. It coordinates planning, implementation, review, and optional validation while keeping the human in one tmux workspace.

## Current Architecture

- Language: Go, single-package CLI in `main.go`.
- Dev shell: `flake.nix`.
- Tests: Go unit tests in `main_test.go`.
- Runtime state: target repositories get an optional `.sidekick/config.json`, `.sidekick/runs/<id>/`, and persistent `.sidekick/memory.md`.
- Entry: bare `sidekick` (no subcommand) opens a persistent console session (`sidekick console` running in a `console` window) that keeps prompting for tasks; `sidekick run` stays the scriptable one-shot with explicit flags (`--task`, `--no-attach`, etc.).
- Orchestration: leases a Treehouse worktree when available (otherwise a plain git worktree under `.sidekick/worktrees/<id>`), creates or reuses a tmux session, and starts planner, dashboard, cycle, learner, optional gate, and land windows for each run. The cycle window owns implement -> review -> fix loops. Inside the console, every run's windows are prefixed (`t1-planner`, `t2-planner`, ...) so multiple tasks run side by side, each in its own worktree and branch.

## Workflow

- Planner is an interactive `claude` chat in its pane (agent `Interactive: true`): the human refines the plan, claude writes `plan.md`, and on exit Sidekick asks whether to release the implementer. Approval writes `planner.done`; declining writes `planner.done.failed` (aborts downstream).
- `sidekick cycle` waits for `planner.done`, runs the implementer autonomously inside the isolated worktree, then runs reviewers concurrently.
- Reviewers end with `SIDEKICK_VERDICT: approve` or `SIDEKICK_VERDICT: revise`. If any reviewer requests changes or exits non-zero, Sidekick writes `review-feedback.md`, reruns the implementer with that feedback, and repeats until all reviewers approve or `maxReviewCycles` is reached. Hitting the cap with unresolved review feedback writes `implement.done.failed`, aborting downstream steps.
- Optional gate runs the configured `no-mistakes` command after implementation.
- Learner runs after implementation (and after `gate.done` when gated), reads the run files and worktree diff, then updates `.sidekick/memory.md` in the real repo root with a run entry plus concise durable repo insights. Skip with `--no-learn`.
- Land window waits for `implement.done`, `gate.done` when gated, and `learn.done` when learning is enabled, then commits the worktree and prompts before pushing the branch and opening a PR via `gh`. Skip with `--no-land`.
- Dashboard runs `sidekick status --watch` and renders the goal, phase, pipeline state, artifacts, recent logs, and the Sidekick sparkle mascot, with a truecolor gradient on a TTY (honors `NO_COLOR`).

## Conventions

- Keep changes small and compatible with configurable agent harnesses.
- Do not hard-code `claude`, `cc`, `codex`, Treehouse, or `no-mistakes` flags beyond defaults; use `.sidekick/config.json` for local harness details.
- Agent configs may set `model` as sugar for appending `--model <value>` before the prompt; use per-agent `modelFlag` when a harness needs a different flag, or keep complex cases in `command`. The learner agent is configured at `agents.learner`.
- Agent configs may set `prompt` to override planner, implementer, reviewer, or learner initial prompts; Sidekick expands `$SIDEKICK_*` run variables, including `$SIDEKICK_MEMORY_FILE`.
- Default planner, implementer, and reviewer prompts ask agents to read `.sidekick/memory.md` when present, so prior run lessons become recall context.
- `notify` config uses terminal bell by default and may run a user-supplied `notify.command`; desktop notification commands are not auto-detected.
- `maxReviewCycles` defaults to 3 and caps the implement/review loop.
- Do not commit generated binaries such as `bin/sidekick`.
- Preserve ASCII-only source files unless a change explicitly requires otherwise. The mascot is a sanctioned exception: it uses 24-bit truecolor escapes (`fg`/`mascotColored` in `main.go`) to render a warm orange-to-pink gradient, gated off exactly like `col()` (no-op under `NO_COLOR` or a non-TTY), so plain-text and piped output stay clean.
- Run `gofmt -w` on Go files, then `go test ./...` and `go build -o bin/sidekick .`.

## Product Direction

Sidekick should feel like a support-console companion: the human is the hero, and Sidekick keeps the agent loop visible and moving. The mascot is a small sparkle mark (star/dot glyphs with a warm gradient) paired with the "sidekick / always-on companion" wordmark - a lightweight visual signature rather than illustrative art.
