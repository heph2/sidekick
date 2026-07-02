// Package agent builds and executes agent harness commands: prompt
// construction, role resolution, process invocation, and reviewer verdicts.
package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sidekick/internal/config"
	"sidekick/internal/run"
	"sidekick/internal/ui"
)

// Command builds the exec.Cmd for an agent, applying the model flag and
// prompt mode ("stdin", "arg", or "file").
func Command(a config.AgentConfig, prompt, promptPath string) (*exec.Cmd, error) {
	if len(a.Command) == 0 {
		return nil, errors.New("empty agent command")
	}
	args := append([]string{}, a.Command[1:]...)
	if model := strings.TrimSpace(a.Model); model != "" {
		flag := strings.TrimSpace(a.ModelFlag)
		if flag == "" {
			flag = "--model"
		}
		args = append(args, flag, model)
	}
	switch config.Normalize(a).PromptMode {
	case "stdin":
	case "arg":
		args = append(args, prompt)
	case "file":
		args = append(args, promptPath)
	default:
		return nil, fmt.Errorf("unsupported promptMode %q", a.PromptMode)
	}
	return exec.Command(a.Command[0], args...), nil
}

// Stdin returns the prompt as stdin for stdin-mode agents, the real stdin
// otherwise.
func Stdin(a config.AgentConfig, prompt []byte) io.Reader {
	if config.Normalize(a).PromptMode == "stdin" {
		return bytes.NewReader(prompt)
	}
	return os.Stdin
}

// ForRole resolves the agent for a role from config defaults alone.
func ForRole(cfg config.Config, role string) (config.AgentConfig, error) {
	return ForRunRole(cfg, run.State{}, role)
}

// ForRunRole resolves the agent for a role, preferring the specific agent the
// run recorded (planner/implementer/learner/reviewer names in state) over the
// config default, so a run that selected a non-default agent keeps using it.
func ForRunRole(cfg config.Config, state run.State, role string) (config.AgentConfig, error) {
	role = strings.TrimSpace(role)
	if role == "planner" {
		if state.PlannerName != "" {
			return config.Normalize(config.ByName(cfg.AllAgents(), state.PlannerName, cfg.Agents.Planner)), nil
		}
		return config.Normalize(cfg.Agents.Planner), nil
	}
	if role == "implementer" {
		if state.ImplementerName != "" {
			return config.Normalize(config.ByName(cfg.AllAgents(), state.ImplementerName, cfg.Agents.Implementer)), nil
		}
		return config.Normalize(cfg.Agents.Implementer), nil
	}
	if role == "learn" {
		if state.LearnerName != "" {
			return config.Normalize(config.ByName(cfg.AllAgents(), state.LearnerName, cfg.Agents.Learner)), nil
		}
		return config.Normalize(cfg.Agents.Learner), nil
	}
	reviewerNames := state.ReviewerNames
	if len(reviewerNames) == 0 {
		for _, reviewer := range cfg.Agents.Reviewers {
			reviewerNames = append(reviewerNames, reviewer.Name)
		}
	}
	for _, reviewerName := range reviewerNames {
		if role == "reviewer-"+run.Slug(reviewerName) {
			return config.Normalize(config.ByName(cfg.Agents.Reviewers, reviewerName, config.AgentConfig{Name: reviewerName})), nil
		}
	}
	return config.AgentConfig{}, fmt.Errorf("unknown role %q", role)
}

// WorkDir picks the directory an agent runs in: the real repo for planner and
// learner, the isolated worktree for everything else.
func WorkDir(state run.State, role string) string {
	if role == "planner" || role == "learn" {
		return state.RepoRoot
	}
	return state.WorktreePath
}

// Run executes one agent stage: it resolves the role's agent, feeds it the
// prompt file, tees output to outputPath, and maintains the done marker.
func Run(cfg config.Config, state run.State, role, promptPath, outputPath, donePath string) error {
	a, err := ForRunRole(cfg, state, role)
	if err != nil {
		return err
	}

	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return err
	}

	env := state.Env()
	if config.Normalize(a).Interactive {
		return runInteractive(cfg, a, prompt, promptPath, WorkDir(state, role), env, donePath)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	cmd, err := Command(a, strings.TrimSpace(string(prompt)), promptPath)
	if err != nil {
		return err
	}
	cmd.Dir = WorkDir(state, role)
	cmd.Env = env
	cmd.Stdin = Stdin(a, prompt)
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	// tee stderr to the log too, else agent failures leave an empty log
	cmd.Stderr = io.MultiWriter(os.Stderr, out)

	if err := cmd.Run(); err != nil {
		if donePath != "" {
			_ = run.Mark(donePath + ".failed")
		}
		return err
	}
	if donePath != "" {
		if err := run.Mark(donePath); err != nil {
			return err
		}
	}
	return nil
}

// runInteractive attaches the harness to the pane's TTY (a live chat, no
// output capture) so its native UI renders, then gates the pipeline on human
// approval: only "y" releases downstream, anything else aborts it.
func runInteractive(cfg config.Config, a config.AgentConfig, prompt []byte, promptPath, dir string, env []string, donePath string) error {
	cmd, err := Command(a, strings.TrimSpace(string(prompt)), promptPath)
	if err != nil {
		return err
	}
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	if donePath == "" {
		return runErr
	}
	// A `sidekick release`/`abort` from another pane may have already resolved
	// this run; never clobber that decision (also means killing the planner
	// window after releasing elsewhere won't spuriously mark it failed).
	if run.Exists(donePath) || run.Exists(donePath+".failed") {
		return runErr
	}
	if runErr != nil {
		_ = run.Mark(donePath + ".failed")
		return runErr
	}
	cfg.Notify.Send("Sidekick: plan ready, release implementer?")
	fmt.Fprintln(os.Stdout, "(or run `sidekick release` in another pane; `sidekick abort` to cancel)")
	if ConfirmRelease(os.Stdin, os.Stdout) {
		return run.Mark(donePath)
	}
	fmt.Fprintln(os.Stdout, "not released; downstream steps aborted")
	return run.Mark(donePath + ".failed")
}

// ConfirmRelease asks the human whether to release downstream steps. Only an
// explicit "y"/"yes" approves; EOF or anything else declines.
func ConfirmRelease(in io.Reader, out io.Writer) bool {
	return ui.PromptYesNo(in, out, "\nPlan ready? release implementer? [y/N] ")
}
