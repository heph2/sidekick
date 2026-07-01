# Sidekick Agent Notes

Sidekick is a local orchestration CLI for agentic development workflows. It coordinates planning, implementation, review, and optional validation while keeping the human in one tmux workspace.

## Current Architecture

- Language: Go, single-package CLI in `main.go`.
- Dev shell: `flake.nix`.
- Tests: Go unit tests in `main_test.go`.
- Runtime state: target repositories get an optional `.sidekick/config.json`, `.sidekick/runs/<id>/`, and persistent `.sidekick/memory.md`.
- Entry: bare `sidekick` (no subcommand) opens a persistent console session (`sidekick console` running in a `console` window) that keeps prompting for tasks; `sidekick run` stays the scriptable one-shot with explicit flags (`--task`, `--no-attach`, etc.).
- Orchestration: leases a Treehouse worktree when available (otherwise a plain git worktree under `.sidekick/worktrees/<id>`), creates or reuses a tmux session, and starts a fixed console workspace: `console` plus one aggregate `dashboard`. Inside the console, each run gets a transient planner window (`t1-planner`, `t2-planner`, ...); implementer, reviewer, learner, optional gate, and land stages run headlessly in the background and log under the run dir. `sidekick run` remains the windowed one-shot path.

## Workflow

- Planner is an interactive `claude` chat in its pane (agent `Interactive: true`): the human refines the plan, claude writes `plan.md`, and release is controlled from the console with `/release [tN]`; `/abort [tN]` writes `planner.done.failed` and aborts downstream.
- Implementer waits for `planner.done`, then works headlessly inside the isolated worktree.
- Reviewers wait for `implement.done`, then review the worktree diff headlessly.
- Optional gate runs the configured `no-mistakes` command headlessly after implementation.
- Learner runs after implementation (and after `gate.done` when gated), reads the run files and worktree diff, then updates `.sidekick/memory.md` in the real repo root with a run entry plus concise durable repo insights. Skip with `--no-learn`.
- Land waits for `implement.done`, `gate.done` when gated, and `learn.done` when learning is enabled, then commits the worktree and waits for `/ship [tN]` before pushing the branch and opening a PR via `gh`. Skip with `--no-land`.
- Dashboard runs `sidekick status --all --watch` and renders each run's goal, phase, pipeline state, and recent logs, with a truecolor gradient mascot on a TTY (honors `NO_COLOR`). Use `/attach <tN> <stage>` to open a temporary log-tail window for a headless stage.

## Conventions

- Keep changes small and compatible with configurable agent harnesses.
- Do not hard-code `claude`, `cc`, `codex`, Treehouse, or `no-mistakes` flags beyond defaults; use `.sidekick/config.json` for local harness details.
- Agent configs may set `model` as sugar for appending `--model <value>` before the prompt; use per-agent `modelFlag` when a harness needs a different flag, or keep complex cases in `command`. The learner agent is configured at `agents.learner`.
- Agent configs may set `prompt` to override planner, implementer, reviewer, or learner initial prompts; Sidekick expands `$SIDEKICK_*` run variables, including `$SIDEKICK_MEMORY_FILE`.
- Default planner, implementer, and reviewer prompts ask agents to read `.sidekick/memory.md` when present, so prior run lessons become recall context.
- `notify` config uses terminal bell by default and may run a user-supplied `notify.command`; desktop notification commands are not auto-detected.
- Do not commit generated binaries such as `bin/sidekick`.
- Preserve ASCII-only source files unless a change explicitly requires otherwise. The mascot is a sanctioned exception: it uses 24-bit truecolor escapes (`fg`/`mascotColored` in `main.go`) to render a warm orange-to-pink gradient, gated off exactly like `col()` (no-op under `NO_COLOR` or a non-TTY), so plain-text and piped output stay clean.
- Run `gofmt -w` on Go files, then `go test ./...` and `go build -o bin/sidekick .`.

## Product Direction

Sidekick should feel like a support-console companion: the human is the hero, and Sidekick keeps the agent loop visible and moving. The mascot is a small sparkle mark (star/dot glyphs with a warm gradient) paired with the "sidekick / always-on companion" wordmark - a lightweight visual signature rather than illustrative art.
