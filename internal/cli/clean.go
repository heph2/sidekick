package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"sidekick/internal/run"
	"sidekick/internal/tmux"
	"sidekick/internal/worktree"
)

// cleanRuns tears down finished runs: removes git-backed worktrees and their
// branches, kills the run's own tmux session, and deletes the run dir. With
// --run it targets one run; otherwise every run under .sidekick/runs.
func cleanRuns(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository path")
	only := fs.String("run", "", "clean a single run by ID (default: all runs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}

	runsDir := filepath.Join(root, run.Root)
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("no runs to clean")
			return nil
		}
		return err
	}

	cleaned := 0
	for _, e := range entries {
		if !e.IsDir() || (*only != "" && e.Name() != *only) {
			continue
		}
		runDir := filepath.Join(runsDir, e.Name())
		// ponytail: best-effort teardown; a missing state.json still gets its dir removed
		if state, err := run.Load(runDir); err == nil {
			if state.WorktreeBackend == "git" && state.WorktreePath != "" {
				worktree.RemoveGit(root, state.WorktreePath, state.ID)
			}
			// only touch the session sidekick created for this run
			if state.TmuxSession != "" {
				tmux.KillSession(state.TmuxSession)
			}
		}
		if err := os.RemoveAll(runDir); err != nil {
			return err
		}
		fmt.Printf("cleaned %s\n", e.Name())
		cleaned++
	}
	worktree.Prune(root)
	if cleaned == 0 {
		fmt.Println("no runs to clean")
	}
	return nil
}
