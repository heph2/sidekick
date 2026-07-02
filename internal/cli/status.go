package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sidekick/internal/config"
	"sidekick/internal/run"
	"sidekick/internal/ui"
)

func showStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	runDir := fs.String("run-dir", "", "run directory")
	repo := fs.String("repo", ".", "target repository")
	all := fs.Bool("all", false, "show all runs for the repository")
	watch := fs.Bool("watch", false, "redraw status until interrupted")
	interval := fs.Duration("interval", 120*time.Millisecond, "watch redraw interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *all {
		root, err := run.RepoRoot(*repo)
		if err != nil {
			return err
		}
		if *watch {
			for {
				fmt.Print("\033[H\033[2J")
				if err := ui.RenderAllStatus(os.Stdout, root, ui.TerminalWidth()); err != nil {
					return err
				}
				time.Sleep(*interval)
			}
		}
		return ui.RenderAllStatus(os.Stdout, root, ui.TerminalWidth())
	}
	if *runDir == "" {
		return errors.New("--run-dir is required")
	}

	if *watch {
		state, err := run.Load(*runDir)
		if err != nil {
			return err
		}
		cfg, err := config.Load(state.RepoRoot)
		if err != nil {
			return err
		}
		previousPhase := ""
		frame := 0
		// ponytail: full redraw at 120ms rereads small run files each frame;
		// fine at this size, throttle or diff-render if files grow.
		for {
			fmt.Print("\033[H")
			if err := ui.RenderStatus(os.Stdout, *runDir, ui.TerminalWidth(), frame); err != nil {
				return err
			}
			fmt.Print("\033[J")
			frame++
			view, err := ui.BuildStatusView(*runDir)
			if err != nil {
				return err
			}
			if previousPhase != "" && view.Phase != previousPhase {
				switch {
				case view.Phase == "complete":
					cfg.Notify.Send("Sidekick: run complete")
				case strings.HasPrefix(view.Phase, "failed:"):
					cfg.Notify.Send("Sidekick: " + view.Phase)
				}
			}
			previousPhase = view.Phase
			time.Sleep(*interval)
		}
	}
	return ui.RenderStatus(os.Stdout, *runDir, ui.TerminalWidth(), 0)
}
