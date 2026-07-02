// Package cli parses arguments, dispatches subcommands, and orchestrates
// runs: spawning worktrees, tmux windows, and the headless stage pipeline.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sidekick/internal/ui"
)

// Main is the sidekick entry point, receiving os.Args.
func Main(args []string) error {
	if len(args) < 2 {
		return startConsole(nil)
	}

	switch args[1] {
	case "init":
		return initConfig(args[2:])
	case "wizard":
		return runWizard(args[2:])
	case "run":
		return startRun(args[2:])
	case "console":
		return consoleLoop(args[2:])
	case "status":
		return showStatus(args[2:])
	case "agent":
		return runAgent(args[2:])
	case "cycle":
		return runCycle(args[2:])
	case "gate":
		return runGate(args[2:])
	case "wait-file":
		return waitFileCmd(args[2:])
	case "clean":
		return cleanRuns(args[2:])
	case "land":
		return landRun(args[2:])
	case "ship":
		return shipRun(args[2:])
	case "release":
		return signalPlanner(args[2:], false)
	case "abort":
		return signalPlanner(args[2:], true)
	case "help", "-h", "--help":
		return usage()
	default:
		// ponytail: bare flags (e.g. `sidekick --no-attach`) mean the hero
		// path, not a subcommand. Anything else is a typo'd command.
		if strings.HasPrefix(args[1], "-") {
			return startConsole(args[1:])
		}
		return fmt.Errorf("unknown command %q\n\n%s", args[1], usageText())
	}
}

func usage() error {
	fmt.Fprint(os.Stderr, usageText())
	return nil
}

func usageText() string {
	return ui.Mascot() + `

sidekick orchestrates planner, implementer, and reviewer agent harnesses.

Usage:
  sidekick [--repo PATH] [--gate] [--no-learn] [--no-land] [--no-attach]
  sidekick run [--task TEXT] [--repo PATH] [--planner NAME] [--implementer NAME] [--gate] [--no-learn] [--no-land] [--no-attach]
  sidekick console [--repo PATH] [--gate] [--no-learn] [--no-land]
  sidekick status --run-dir PATH [--watch] [--interval 2s]
  sidekick status --all [--repo PATH] [--watch] [--interval 2s]
  sidekick init [--repo PATH]
  sidekick wizard [--repo PATH]
  sidekick agent --repo PATH --run-dir PATH --role ROLE --prompt FILE --output FILE [--done FILE]
  sidekick cycle --repo PATH --run-dir PATH
  sidekick gate --repo PATH --run-dir PATH --output FILE [--done FILE]
  sidekick land --repo PATH --run-dir PATH
  sidekick ship [--repo PATH] [--run-dir PATH]      # approve landing
  sidekick release [--repo PATH] [--run-dir PATH]   # approve the newest waiting plan
  sidekick abort   [--repo PATH] [--run-dir PATH]   # cancel it instead
  sidekick wait-file FILE
  sidekick clean [--repo PATH] [--run ID]

Typical flow:
  cd /path/to/project
  sidekick          # opens a persistent console; keep giving it tasks
  sidekick run --task "..." --no-attach   # scriptable one-shot run

No config or treehouse setup is required: sidekick uses built-in defaults and
falls back to a plain git worktree when treehouse is unavailable.
Set maxReviewCycles in .sidekick/config.json to cap implement/review loops
(default: 3).
`
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// self returns the path of the running sidekick binary, for re-invoking
// subcommands inside tmux panes and detached stages.
func self() string {
	path, err := os.Executable()
	if err == nil {
		return path
	}
	if len(os.Args) == 0 {
		return "sidekick"
	}
	path, err = filepath.Abs(os.Args[0])
	if err == nil {
		return path
	}
	return os.Args[0]
}
