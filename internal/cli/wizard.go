package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sidekick/internal/config"
	"sidekick/internal/run"
)

func initConfig(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}

	path := filepath.Join(root, config.Path)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return writeConfig(path, config.Default())
}

func runWizard(args []string) error {
	fs := flag.NewFlagSet("wizard", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}
	path := filepath.Join(root, config.Path)

	cfg := config.Default()
	exists := false
	if _, err := os.Stat(path); err == nil {
		exists = true
		cfg, err = config.Load(root)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	sc := bufio.NewScanner(os.Stdin)
	fmt.Printf("Configuring Sidekick agents for %s\n", root)
	cfg.Agents.Planner = promptAgent(sc, "planner", cfg.Agents.Planner)
	cfg.Agents.Implementer = promptAgent(sc, "implementer", cfg.Agents.Implementer)
	cfg.Agents.Reviewers = promptReviewers(sc, cfg.Agents.Reviewers)
	cfg.Agents.Learner = promptAgent(sc, "learner", cfg.Agents.Learner)
	if err := sc.Err(); err != nil {
		return err
	}

	if exists {
		overwrite := askYesNo(sc, fmt.Sprintf("Overwrite %s? [y/N] ", path))
		if err := sc.Err(); err != nil {
			return err
		}
		if !overwrite {
			fmt.Println("aborted")
			return nil
		}
	}

	return writeConfig(path, cfg)
}

func writeConfig(path string, cfg config.Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func promptAgent(sc *bufio.Scanner, role string, cur config.AgentConfig) config.AgentConfig {
	defHarness := inferHarness(cur)
	harness := strings.ToLower(ask(sc, fmt.Sprintf("%s harness (claude/codex/custom)", role), defHarness))
	return agentFromHarness(sc, role, cur, harness)
}

func promptReviewers(sc *bufio.Scanner, current []config.AgentConfig) []config.AgentConfig {
	var reviewers []config.AgentConfig
	stopped := false
	if len(current) > 0 {
		names := make([]string, 0, len(current))
		for _, reviewer := range current {
			names = append(names, reviewer.Name)
		}
		fmt.Printf("Existing reviewers: %s\n", strings.Join(names, ", "))
	}
	for i, cur := range current {
		defHarness := inferHarness(cur)
		harness := strings.ToLower(ask(sc, fmt.Sprintf("reviewer %d harness (claude/codex/custom, blank keeps, stop ends)", i+1), defHarness))
		if isStopAnswer(harness) {
			stopped = true
			break
		}
		reviewers = append(reviewers, agentFromHarness(sc, "reviewer", cur, harness))
	}
	if !stopped {
		for i := len(reviewers) + 1; ; i++ {
			harness := strings.ToLower(ask(sc, fmt.Sprintf("reviewer %d harness (claude/codex/custom, blank to stop)", i), ""))
			if harness == "" || isStopAnswer(harness) {
				break
			}
			reviewers = append(reviewers, agentFromHarness(sc, "reviewer", config.AgentConfig{}, harness))
		}
	}
	return uniquifyReviewerNames(reviewers)
}

func agentFromHarness(sc *bufio.Scanner, role string, cur config.AgentConfig, harness string) config.AgentConfig {
	switch harness {
	case "claude", "codex":
		agent := presetAgent(harness, role)
		agent.Name = defaultAgentName(harness, role)
		agent.Model = ask(sc, role+" model", cur.Model)
		agent.Prompt = cur.Prompt
		agent.Fallbacks = cur.Fallbacks
		if harness == inferHarness(cur) && cur.Name != "" {
			agent.Interactive = cur.Interactive
		}
		return agent
	default:
		command := ask(sc, role+" command", strings.Join(cur.Command, " "))
		agent := config.AgentConfig{
			Name:        cur.Name,
			Command:     strings.Fields(command),
			PromptMode:  ask(sc, role+" prompt mode", defaultPromptMode(cur)),
			Model:       ask(sc, role+" model", cur.Model),
			Prompt:      cur.Prompt,
			Interactive: cur.Interactive,
			Fallbacks:   cur.Fallbacks,
		}
		if agent.Name == "" {
			agent.Name = "custom-" + role
		}
		return agent
	}
}

func presetAgent(harness, role string) config.AgentConfig {
	switch harness {
	case "claude":
		if role == "planner" {
			return config.AgentConfig{Command: []string{"claude"}, PromptMode: "arg", Interactive: true}
		}
		return config.AgentConfig{Command: []string{"claude"}, PromptMode: "stdin"}
	case "codex":
		if role == "implementer" {
			return config.AgentConfig{Command: []string{"codex", "exec", "--sandbox", "workspace-write"}, PromptMode: "stdin"}
		}
		return config.AgentConfig{Command: []string{"codex", "exec"}, PromptMode: "stdin"}
	default:
		return config.AgentConfig{PromptMode: "stdin"}
	}
}

func inferHarness(agent config.AgentConfig) string {
	if len(agent.Command) == 0 {
		return "claude"
	}
	switch strings.ToLower(agent.Command[0]) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	default:
		return "custom"
	}
}

func defaultPromptMode(agent config.AgentConfig) string {
	if agent.PromptMode != "" {
		return agent.PromptMode
	}
	return "stdin"
}

func defaultAgentName(harness, role string) string {
	return harness + "-" + role
}

func uniquifyReviewerNames(reviewers []config.AgentConfig) []config.AgentConfig {
	seen := map[string]int{}
	for i := range reviewers {
		base := reviewers[i].Name
		if base == "" {
			base = "reviewer"
		}
		seen[base]++
		if seen[base] == 1 {
			reviewers[i].Name = base
			continue
		}
		reviewers[i].Name = fmt.Sprintf("%s-%d", base, seen[base])
	}
	return reviewers
}

func isStopAnswer(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "stop", "done", "none":
		return true
	default:
		return false
	}
}

func ask(sc *bufio.Scanner, label, def string) string {
	if def == "" {
		fmt.Printf("%s: ", label)
	} else {
		fmt.Printf("%s [%s]: ", label, def)
	}
	if !sc.Scan() {
		return def
	}
	answer := strings.TrimSpace(sc.Text())
	if answer == "" {
		return def
	}
	return answer
}

func askYesNo(sc *bufio.Scanner, question string) bool {
	fmt.Print(question)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
