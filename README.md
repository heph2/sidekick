# Sidekick

Sidekick is a local orchestration CLI for agentic development loops.

It keeps the human in one conversation while coordinating the noisy parts:

- planning with a dedicated AI harness
- implementation in an isolated worktree (Treehouse when available, otherwise a plain git worktree)
- review by multiple harnesses
- optional `no-mistakes` gating after implementation
- one tmux workspace with a live dashboard for visibility without constant pane switching

## Current shape

This is the first executable slice. Sidekick does not replace `claude`, `cc`, `codex`, Treehouse, or `no-mistakes`; it coordinates them.

The default flow is:

1. planner runs in the target repo and writes `.sidekick/runs/<id>/plan.md`
2. implementer waits for the plan, then works in an isolated worktree
3. reviewers wait for implementation to finish, then review the git diff
4. the dashboard window tracks goal, phase, artifacts, and recent output
5. optional gate window runs `no-mistakes -y`

## Requirements

- `git`
- `go`
- `tmux`
- the configured agent CLIs, for example `claude`, `cc`, or `codex`
- optional: `treehouse` (falls back to a plain git worktree when absent)
- optional: `no-mistakes`

## Usage

Build:

```sh
go build -o bin/sidekick .
```

Start a run from inside any git repo. With no arguments, Sidekick prompts for
the task, leases a worktree, and attaches to the dashboard:

```sh
cd /path/to/project
sidekick
```

No `init` step is required: Sidekick uses built-in defaults when
`.sidekick/config.json` is absent, and falls back to a plain git worktree when
`treehouse` is not installed. Run `sidekick init` only if you want to customize
the agent commands (see Config below).

Pass the task inline, or pipe it in, instead of the prompt:

```sh
sidekick --task "Implement the requested feature and validate it"
echo "Implement the requested feature and validate it" | sidekick --no-attach
```

With the gate, and without auto-attaching:

```sh
sidekick --task "Implement the requested feature and validate it" --gate --no-attach
```

Render a run dashboard without attaching to tmux:

```sh
bin/sidekick status --run-dir /path/to/project/.sidekick/runs/<id>
```

Watch it live:

```sh
bin/sidekick status --run-dir /path/to/project/.sidekick/runs/<id> --watch
```

The dashboard includes the Sidekick wood-hero ASCII mascot, the current phase, pipeline status, run artifacts, and recent agent output.

## Config

Default config:

```json
{
  "agents": {
    "planner": {
      "name": "claude-planner",
      "command": ["claude"],
      "promptMode": "stdin"
    },
    "implementer": {
      "name": "codex-implementer",
      "command": ["codex"],
      "promptMode": "stdin"
    },
    "reviewers": [
      {
        "name": "codex-reviewer",
        "command": ["codex"],
        "promptMode": "stdin"
      },
      {
        "name": "claude-reviewer",
        "command": ["claude"],
        "promptMode": "stdin"
      }
    ]
  },
  "gate": {
    "enabled": false,
    "command": ["no-mistakes", "-y"]
  }
}
```

`promptMode` can be:

- `stdin`: write the prompt to process stdin
- `arg`: append the full prompt as the last argument
- `file`: append the prompt file path as the last argument

Use `command` to add local model and approval flags, for example:

```json
{
  "name": "cc-planner",
  "command": ["cc", "--model", "your-planning-model"],
  "promptMode": "stdin"
}
```

## Notes

When `treehouse` is available, Sidekick creates durable leases with holder names
like `sidekick:<run-id>`. Return a leased worktree when done:

```sh
treehouse return /path/to/leased/worktree
```

When `treehouse` is absent, Sidekick creates a git worktree under
`.sidekick/worktrees/<id>` on a `sidekick/<run-id>` branch.

The run state and logs live under `.sidekick/runs/<id>/`. Tear down finished
runs (git worktrees, their branches, the run's tmux session, and the run dir)
with:

```sh
sidekick clean            # all runs
sidekick clean --run <id> # one run
```

## Agent Notes

Project-local guidance for AI harnesses lives in `AGENTS.md`.
`CLAUDE.md` is a symlink to the same file so Claude-style harnesses read identical instructions.
