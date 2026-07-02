package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sidekick/internal/config"
	"sidekick/internal/run"
	"sidekick/internal/ui"
)

// landRun commits the worktree, then (after a prompt) pushes its branch and
// opens a PR. Commit is local and reversible; the push/PR is the outward action
// and is gated behind an explicit yes.
func landRun(args []string) error {
	fs := flag.NewFlagSet("land", flag.ContinueOnError)
	_ = fs.String("repo", ".", "repository path") // accepted for symmetry; land uses the worktree from state
	runDir := fs.String("run-dir", "", "run directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return errors.New("--run-dir is required")
	}
	state, err := run.Load(*runDir)
	if err != nil {
		return err
	}
	wt := state.WorktreePath
	if wt == "" {
		return errors.New("no worktree path in run state")
	}

	title, body := planSummary(state.PlanFile, state.TaskFile, state.ID)
	if title == "" {
		title = state.ID
	}
	branch, changed, err := landCommit(wt, title)
	if err != nil {
		_ = run.Mark(state.LandDone() + ".failed")
		return err
	}
	if !changed {
		fmt.Println("nothing to land: worktree has no changes")
		return run.Mark(state.LandDone())
	}
	fmt.Printf("committed on branch %s\n", branch)

	cfg, err := config.Load(state.RepoRoot)
	if err != nil {
		return err
	}
	cfg.Notify.Send("Sidekick: ready to push " + branch + " and open a PR")
	if stdinIsTTY() {
		if !ui.PromptYesNo(os.Stdin, os.Stdout, fmt.Sprintf("Push %s and open a PR? [y/N] ", branch)) {
			fmt.Printf("left local; push later with: git -C %s push -u origin %s\n", wt, branch)
			return nil
		}
	} else {
		if err := run.Mark(state.LandReady()); err != nil {
			_ = run.Mark(state.LandDone() + ".failed")
			return err
		}
		fmt.Printf("ready to ship %s; run: sidekick ship --run-dir %s\n", branch, state.RunDir)
		if err := run.WaitFile(state.LandApprove()); err != nil {
			_ = run.Mark(state.LandDone() + ".failed")
			return err
		}
	}

	push := exec.Command("git", "-C", wt, "push", "-u", "origin", branch)
	push.Stdout, push.Stderr = os.Stdout, os.Stderr
	if err := push.Run(); err != nil {
		_ = run.Mark(state.LandDone() + ".failed")
		return fmt.Errorf("git push: %w", err)
	}
	if _, err := exec.LookPath("gh"); err != nil {
		fmt.Printf("gh not found; open a PR for %s from your git host\n", branch)
		return run.Mark(state.LandDone())
	}
	prArgs := []string{"pr", "create", "--head", branch}
	if title != "" || body != "" {
		prArgs = append(prArgs, "--title", title, "--body", body)
	} else {
		prArgs = append(prArgs, "--fill")
	}
	pr := exec.Command("gh", prArgs...)
	pr.Dir = wt
	pr.Stdout, pr.Stderr = os.Stdout, os.Stderr
	if err := pr.Run(); err != nil {
		_ = run.Mark(state.LandDone() + ".failed")
		return err
	}
	return run.Mark(state.LandDone())
}

// landCommit stages and commits the worktree. Returns the current branch and
// whether anything was committed (false when the worktree was clean).
func landCommit(wt, goal string) (string, bool, error) {
	if err := exec.Command("git", "-C", wt, "add", "-A").Run(); err != nil {
		return "", false, fmt.Errorf("git add: %w", err)
	}
	dirty, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		return "", false, fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(string(dirty)) == "" {
		return "", false, nil
	}
	commit := exec.Command("git", "-C", wt, "commit", "-m", "sidekick: "+goal)
	commit.Stdout, commit.Stderr = os.Stdout, os.Stderr
	if err := commit.Run(); err != nil {
		return "", false, fmt.Errorf("git commit: %w", err)
	}
	branchOut, err := exec.Command("git", "-C", wt, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", false, fmt.Errorf("resolve branch: %w", err)
	}
	return strings.TrimSpace(string(branchOut)), true, nil
}

func shipRun(args []string) error {
	fs := flag.NewFlagSet("ship", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository path")
	runDir := fs.String("run-dir", "", "run directory (default: newest run ready to land)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := *runDir
	if dir == "" {
		root, err := run.RepoRoot(*repo)
		if err != nil {
			return err
		}
		dir, err = run.FindReadyLand(root)
		if err != nil {
			return err
		}
	}
	state, err := run.Load(dir)
	if err != nil {
		return err
	}
	if run.Exists(state.LandDone()) {
		return fmt.Errorf("already landed: %s", filepath.Base(dir))
	}
	if !run.Exists(state.LandReady()) {
		return fmt.Errorf("run is not ready to ship: %s", filepath.Base(dir))
	}
	if run.Exists(state.LandApprove()) {
		return fmt.Errorf("already approved: %s", filepath.Base(dir))
	}
	if err := run.Mark(state.LandApprove()); err != nil {
		return err
	}
	fmt.Printf("shipping %s; land continues within ~2s\n", filepath.Base(dir))
	return nil
}

func planSummary(planFile, taskFile, runID string) (string, string) {
	goal := goalSection(planFile)
	fallback := run.FirstLine(taskFile)
	if fallback == "unavailable" || fallback == "empty task" {
		fallback = runID
	}
	if strings.TrimSpace(goal) == "" {
		title := ui.Clip(strings.TrimSpace(fallback), 70)
		return title, fmt.Sprintf("%s\n\nRun: %s", title, runID)
	}
	title := ui.Clip(firstSentence(goal), 70)
	if strings.TrimSpace(title) == "" {
		title = ui.Clip(strings.TrimSpace(fallback), 70)
	}
	body := strings.TrimSpace(goal) + "\n\nPlan: " + filepath.ToSlash(filepath.Join(".sidekick", "runs", runID, "plan.md")) + "\nRun: " + runID
	return title, body
}

func goalSection(planFile string) string {
	data, err := os.ReadFile(planFile)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	inGoal := false
	var goal []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			if inGoal {
				break
			}
			if strings.EqualFold(trimmed, "## Goal") {
				inGoal = true
			}
			continue
		}
		if inGoal {
			goal = append(goal, line)
		}
	}
	return strings.TrimSpace(strings.Join(goal, "\n"))
}

func firstSentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' {
			return strings.TrimSpace(text[:i+1])
		}
	}
	return strings.TrimSpace(text)
}
