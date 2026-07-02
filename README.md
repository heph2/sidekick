# Sidekick

Sidekick is a local orchestration CLI for agentic development loops.

It keeps the human in one conversation while coordinating the noisy parts:

- planning with a dedicated AI harness
- implementation in an isolated worktree (Treehouse when available, otherwise a plain git worktree)
- review by multiple harnesses, looping back into implementation when changes are requested
- optional `no-mistakes` gating after implementation
- repo-local memory that records what happened and durable lessons for future runs
- one tmux workspace with a fixed live dashboard for visibility without constant pane switching

## Current shape

This is the first executable slice. Sidekick does not replace `claude`, `cc`, `codex`, Treehouse, or `no-mistakes`; it coordinates them.

The default console flow is:

1. planner runs as an interactive chat in the target repo; you refine the plan,
   it writes `.sidekick/runs/<id>/plan.md`, and Sidekick asks you to release the
   implementer before anything else starts
2. release or abort from the console with `/release [tN]` or `/abort [tN]`
3. `sidekick cycle` waits for your approval, then works headlessly in an isolated
   worktree: it runs the implementer, then runs reviewers against the result
4. if any reviewer requests changes, Sidekick writes reviewer feedback and
   reruns the implementer; the loop repeats until all reviewers approve or
   `maxReviewCycles` is reached
5. learner waits for implementation approval, then updates `.sidekick/memory.md` with a
   run entry and concise repo insights (skip with `--no-learn`)
6. the shared dashboard window tracks every console run, its phase, pipeline
   status, and recent output (with color on a TTY)
7. optional gate runs `no-mistakes -y` headlessly; when enabled, learner and land wait for it
8. land commits the worktree headlessly, then waits for `/ship [tN]` before
   pushing the branch and opening a PR via `gh`; when learning is enabled, land
   waits for it first so the learner can inspect the uncommitted diff (skip with
   `--no-land`)

`sidekick run` remains the windowed one-shot path for scripts and CI-style use.

## Requirements

- `git`
- `go`
- `tmux`
- the configured agent CLIs, for example `claude`, `cc`, or `codex`
- optional: `gh` (to open the PR in the land step; without it, push and open the PR by hand)
- optional: `treehouse` (falls back to a plain git worktree when absent)
- optional: `no-mistakes`

## Usage

Build:

```sh
go build -o bin/sidekick ./cmd/sidekick
```

Start a run from inside any git repo. With no arguments, Sidekick prompts for
tasks in a persistent console, leases a worktree for each task, and opens a
shared dashboard:

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
sidekick run --task "Implement the requested feature and validate it"
echo "Implement the requested feature and validate it" | sidekick run --no-attach
```

With the gate, and without auto-attaching:

```sh
sidekick run --task "Implement the requested feature and validate it" --gate --no-attach
```

Skip the post-run learner for a one-off run:

```sh
sidekick run --task "Try an experiment" --no-learn
```

Render a run dashboard without attaching to tmux:

```sh
bin/sidekick status --run-dir /path/to/project/.sidekick/runs/<id>
```

Watch it live:

```sh
bin/sidekick status --run-dir /path/to/project/.sidekick/runs/<id> --watch
```

Render the shared dashboard for every run in a repo:

```sh
bin/sidekick status --all --repo /path/to/project
```

Inside `sidekick console`, use `/help` to list commands. Common commands are
`/list`, `/release [tN]`, `/abort [tN]`, `/ship [tN]`, `/status [tN]`, and
`/attach <tN> <stage>` to tail a headless stage log in a temporary tmux window.

The dashboard includes the Sidekick ASCII mascot, the current phase, pipeline status, run artifacts, and recent agent output.

## Config

Default config:

```json
{
  "agents": {
    "planner": {
      "name": "claude-planner",
      "command": ["claude"],
      "promptMode": "arg",
      "interactive": true
    },
    "implementer": {
      "name": "codex-implementer",
      "command": ["codex", "exec", "--sandbox", "workspace-write"],
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
    ],
    "learner": {
      "name": "claude-learner",
      "command": ["claude"],
      "promptMode": "stdin"
    }
  },
  "gate": {
    "enabled": false,
    "command": ["no-mistakes", "-y"]
  },
  "notify": {
    "noBell": false,
    "command": []
  },
  "maxReviewCycles": 3
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

Or set `model` on an agent. Sidekick appends `--model <value>` before the
prompt by default; set `modelFlag` when a harness uses a different flag. If a
harness needs more complex model selection, keep using `command` directly.

```json
{
  "name": "codex-implementer",
  "command": ["codex", "exec", "--sandbox", "workspace-write"],
  "promptMode": "stdin",
  "model": "gpt-5-codex"
}
```

Each planner, implementer, reviewer, or learner can also set `prompt` to replace
the built-in initial prompt. Sidekick expands `$SIDEKICK_RUN_ID`,
`$SIDEKICK_RUN_DIR`, `$SIDEKICK_TASK_FILE`, `$SIDEKICK_PLAN_FILE`,
`$SIDEKICK_MEMORY_FILE`, and `$SIDEKICK_WORKTREE` in custom prompts.

The learner defaults to a non-interactive `claude` invocation. It runs from the
real repo root, reads the run files and worktree diff, and updates only
`.sidekick/memory.md`. Planner, implementer, and reviewer prompts tell agents to
read that file first when it exists, so durable repo conventions and pitfalls are
carried into future runs. Use `--no-learn` to skip this for a run.

`maxReviewCycles` caps the implement/review loop. When the cap is reached while
any reviewer still requests changes, Sidekick marks implementation failed so
gate, learner, and land do not proceed.

`notify` controls attention signals. The terminal bell is enabled by default;
set `"noBell": true` to silence it. `notify.command` is optional and receives
the Sidekick message as its final argument, for example:

```json
{
  "notify": {
    "command": ["notify-send", "Sidekick"]
  }
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

The run state and logs live under `.sidekick/runs/<id>/`. Persistent repo memory
lives at `.sidekick/memory.md` and is not removed by cleanup. Tear down finished
runs (git worktrees, their branches, the run's tmux session, and the run dir)
with:

```sh
sidekick clean            # all runs
sidekick clean --run <id> # one run
```

## Agent Notes

Project-local guidance for AI harnesses lives in `AGENTS.md`.
`CLAUDE.md` is a symlink to the same file so Claude-style harnesses read identical instructions.
