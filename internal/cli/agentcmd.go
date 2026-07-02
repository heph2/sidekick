package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"sidekick/internal/agent"
	"sidekick/internal/config"
	"sidekick/internal/run"
)

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	repo := fs.String("repo", "", "target repository")
	runDir := fs.String("run-dir", "", "run directory")
	role := fs.String("role", "", "agent role")
	promptPath := fs.String("prompt", "", "prompt file")
	outputPath := fs.String("output", "", "output file")
	donePath := fs.String("done", "", "done marker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *runDir == "" || *role == "" || *promptPath == "" || *outputPath == "" {
		return errors.New("--repo, --run-dir, --role, --prompt, and --output are required")
	}

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	state, err := run.Load(*runDir)
	if err != nil {
		return err
	}
	return agent.Run(cfg, state, *role, *promptPath, *outputPath, *donePath)
}

func runGate(args []string) error {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	repo := fs.String("repo", "", "target repository")
	runDir := fs.String("run-dir", "", "run directory")
	outputPath := fs.String("output", "", "output file")
	donePath := fs.String("done", "", "done marker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *runDir == "" || *outputPath == "" {
		return errors.New("--repo, --run-dir, and --output are required")
	}

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if err := config.RequireCommand(cfg.Gate.Command); err != nil {
		return err
	}
	state, err := run.Load(*runDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(*outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	cmd := exec.Command(cfg.Gate.Command[0], cfg.Gate.Command[1:]...)
	cmd.Dir = state.WorktreePath
	cmd.Env = state.Env()
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	cmd.Stderr = io.MultiWriter(os.Stderr, out)
	if err := cmd.Run(); err != nil {
		if *donePath != "" {
			_ = run.Mark(*donePath + ".failed")
		}
		return err
	}
	if *donePath != "" {
		return run.Mark(*donePath)
	}
	return nil
}

func waitFileCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("wait-file requires exactly one path")
	}
	return run.WaitFile(args[0])
}

// signalPlanner releases (or, with fail=true, aborts) a run waiting on planner
// approval, by writing planner.done / planner.done.failed from any pane -- so
// the human never has to quit the interactive planner to unblock the pipeline.
// With no --run-dir it targets the newest run still waiting for approval.
func signalPlanner(args []string, fail bool) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository path")
	runDir := fs.String("run-dir", "", "run directory (default: newest run awaiting approval)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir := *runDir
	if dir == "" {
		root, err := run.RepoRoot(*repo)
		if err != nil {
			return err
		}
		dir, err = run.FindWaiting(root)
		if err != nil {
			return err
		}
	}

	done := filepath.Join(dir, "planner.done")
	if run.Exists(done) {
		return fmt.Errorf("already released: %s", done)
	}
	if run.Exists(done + ".failed") {
		return fmt.Errorf("already aborted: %s", done+".failed")
	}
	if fail {
		if err := run.Mark(done + ".failed"); err != nil {
			return err
		}
		fmt.Printf("aborted %s\n", filepath.Base(dir))
		return nil
	}
	if err := run.Mark(done); err != nil {
		return err
	}
	fmt.Printf("released %s; implementer starts within ~2s\n", filepath.Base(dir))
	return nil
}
