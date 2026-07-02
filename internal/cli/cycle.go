package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"sidekick/internal/agent"
	"sidekick/internal/config"
	"sidekick/internal/run"
)

// runCycle owns the implement -> review -> fix loop for one run. It runs the
// implementer, then all reviewers concurrently, and repeats with reviewer
// feedback until every reviewer approves or maxReviewCycles is reached. It
// writes implement.done on success and implement.done.failed on cap, so the
// downstream gate/learn/land stages gate on the same marker they always have.
func runCycle(args []string) error {
	fs := flag.NewFlagSet("cycle", flag.ContinueOnError)
	repo := fs.String("repo", "", "real repository root, used for config")
	runDir := fs.String("run-dir", "", "run directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *runDir == "" {
		return errors.New("--repo and --run-dir are required")
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
	if len(state.ReviewerNames) == 0 {
		return errors.New("at least one reviewer is required")
	}
	if state.RepoRoot == "" {
		state.RepoRoot = root
	}

	implementer := config.ByName(cfg.AllAgents(), state.ImplementerName, cfg.Agents.Implementer)
	max := cfg.MaxReviewCycles
	feedbackPath := filepath.Join(state.RunDir, "review-feedback.md")

	for iter := 1; iter <= max; iter++ {
		writeCycleStatus(state, fmt.Sprintf("cycle %d/%d: implementing", iter, max))
		prompt := agent.ImplementerPrompt(state, implementer)
		if iter > 1 {
			prompt += fmt.Sprintf(`

## Reviewer feedback (previous round)

Reviewers requested changes in the previous round. Read:
%s

Address every concrete point before summarizing your validation.
`, feedbackPath)
		}
		implementPromptPath := filepath.Join(state.RunDir, "implementer.prompt.md")
		if err := os.WriteFile(implementPromptPath, []byte(prompt), 0o644); err != nil {
			_ = run.Mark(state.ImplementDone + ".failed")
			return err
		}
		if err := runSidekickAgent(state, root, "implementer", implementPromptPath, state.ImplementerLog()); err != nil {
			_ = run.Mark(state.ImplementDone + ".failed")
			return fmt.Errorf("implementer cycle %d/%d: %w", iter, max, err)
		}

		writeCycleStatus(state, fmt.Sprintf("cycle %d/%d: reviewing", iter, max))
		results := runReviewers(state, root)
		needsRevision := false
		for _, result := range results {
			if result.Verdict == "revise" {
				needsRevision = true
				break
			}
		}
		if !needsRevision {
			for _, reviewer := range state.ReviewerNames {
				if err := run.Mark(state.ReviewerDone(reviewer)); err != nil {
					_ = run.Mark(state.ImplementDone + ".failed")
					return err
				}
			}
			_ = os.Remove(feedbackPath)
			writeCycleStatus(state, fmt.Sprintf("cycle %d/%d: approved", iter, max))
			return run.Mark(state.ImplementDone)
		}
		if err := writeReviewFeedback(feedbackPath, results); err != nil {
			_ = run.Mark(state.ImplementDone + ".failed")
			return err
		}
	}

	writeCycleStatus(state, fmt.Sprintf("cycle cap %d reached; review still requests changes", max))
	_ = run.Mark(state.ImplementDone + ".failed")
	return fmt.Errorf("review cycle cap %d reached with unresolved reviewer feedback", max)
}

type reviewResult struct {
	Name    string
	LogPath string
	Verdict string
	Err     error
}

// runSidekickAgent runs one agent stage synchronously without a done marker;
// the cycle loop owns done markers so a per-round agent run never releases the
// pipeline on its own.
func runSidekickAgent(state run.State, repo, role, promptPath, outputPath string) error {
	cmd := exec.Command(self(), "agent", "--repo", repo, "--run-dir", state.RunDir, "--role", role, "--prompt", promptPath, "--output", outputPath)
	cmd.Dir = state.WorktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runReviewers(state run.State, repo string) []reviewResult {
	results := make([]reviewResult, len(state.ReviewerNames))
	var wg sync.WaitGroup
	for i, reviewer := range state.ReviewerNames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			role := "reviewer-" + run.Slug(reviewer)
			promptPath := filepath.Join(state.RunDir, role+".prompt.md")
			logPath := state.ReviewerLog(reviewer)
			err := runSidekickAgent(state, repo, role, promptPath, logPath)
			results[i] = reviewResult{
				Name:    reviewer,
				LogPath: logPath,
				Verdict: agent.ReviewVerdict(logPath, err),
				Err:     err,
			}
		}()
	}
	wg.Wait()
	return results
}

func writeReviewFeedback(path string, results []reviewResult) error {
	var b strings.Builder
	b.WriteString("# Reviewer feedback\n\n")
	for _, result := range results {
		if result.Verdict != "revise" {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", result.Name)
		if result.Err != nil {
			fmt.Fprintf(&b, "Reviewer process failed: %v\n\n", result.Err)
		}
		data, err := os.ReadFile(result.LogPath)
		if err != nil {
			fmt.Fprintf(&b, "Could not read reviewer log %s: %v\n\n", result.LogPath, err)
			continue
		}
		b.Write(bytes.TrimSpace(data))
		b.WriteString("\n\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeCycleStatus(state run.State, msg string) {
	fmt.Println(msg)
	_ = os.WriteFile(filepath.Join(state.RunDir, "cycle.status"), []byte(msg+"\n"), 0o644)
}
