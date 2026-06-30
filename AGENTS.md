# Sidekick Agent Notes

Sidekick is a local orchestration CLI for agentic development workflows. It coordinates planning, implementation, review, and optional validation while keeping the human in one tmux workspace.

## Current Architecture

- Language: Go, single-package CLI in `main.go`.
- Dev shell: `flake.nix`.
- Tests: Go unit tests in `main_test.go`.
- Runtime state: target repositories get `.sidekick/config.json` and `.sidekick/runs/<id>/`.
- Orchestration: `sidekick run` leases a Treehouse worktree, creates a tmux session, and starts planner, dashboard, implementer, reviewer, and optional gate windows.

## Workflow

- Planner runs in the original repo and writes `plan.md`.
- Implementer waits for `planner.done`, then works inside the leased Treehouse worktree.
- Reviewers wait for `implement.done`, then review the worktree diff.
- Optional gate runs the configured `no-mistakes` command after implementation.
- Dashboard runs `sidekick status --watch` and renders the goal, phase, pipeline state, artifacts, recent logs, and ASCII Sidekick mascot.

## Conventions

- Keep changes small and compatible with configurable agent harnesses.
- Do not hard-code `claude`, `cc`, `codex`, Treehouse, or `no-mistakes` flags beyond defaults; use `.sidekick/config.json` for local harness details.
- Do not commit generated binaries such as `bin/sidekick`.
- Preserve ASCII-only source files unless a change explicitly requires otherwise.
- Run `gofmt -w` on Go files, then `go test ./...` and `go build -o bin/sidekick .`.

## Product Direction

Sidekick should feel like a support-console companion: the human is the hero, and Sidekick keeps the agent loop visible and moving. The current mascot is an original wood-hero inspired ASCII mark based on the requested Kamui Woods direction, not copied artwork.
