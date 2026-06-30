# Sidekick

Sidekick is a local orchestration CLI for agentic development loops.

It keeps the human in one conversation while coordinating the noisy parts:

- planning with a dedicated AI harness
- implementation in an isolated Treehouse worktree
- review by multiple harnesses
- optional `no-mistakes` gating after implementation
- one tmux workspace for visibility without constant pane switching

## Current shape

This is the first executable slice. Sidekick does not replace `claude`, `cc`, `codex`, Treehouse, or `no-mistakes`; it coordinates them.

The default flow is:

1. planner runs in the target repo and writes `.sidekick/runs/<id>/plan.md`
2. implementer waits for the plan, then works in a leased Treehouse worktree
3. reviewers wait for implementation to finish, then review the git diff
4. optional gate window runs `no-mistakes -y`

## Requirements

- `git`
- `go`
- `tmux`
- `treehouse`
- the configured agent CLIs, for example `claude`, `cc`, or `codex`
- optional: `no-mistakes`

## Usage

Build:

```sh
go build -o bin/sidekick .
```

Initialize config in a target project:

```sh
bin/sidekick init --repo /path/to/project
```

Edit `/path/to/project/.sidekick/config.json` so each agent command matches your local harness setup.

Start a run:

```sh
bin/sidekick run --repo /path/to/project --task "Implement the requested feature and validate it" --attach
```

With the gate:

```sh
bin/sidekick run --repo /path/to/project --task "Implement the requested feature and validate it" --gate --attach
```

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

Sidekick creates durable Treehouse leases with holder names like `sidekick:<run-id>`.
Return a leased worktree when done:

```sh
treehouse return /path/to/leased/worktree
```

The run state and logs live under `.sidekick/runs/<id>/`.
