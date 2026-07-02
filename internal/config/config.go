// Package config loads and validates .sidekick/config.json, the per-repo
// description of agent harnesses, the gate command, and notifications.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Path is the config file location relative to the repo root.
const Path = ".sidekick/config.json"

type Config struct {
	Agents          AgentsConfig `json:"agents"`
	Gate            GateConfig   `json:"gate"`
	Notify          NotifyConfig `json:"notify"`
	MaxReviewCycles int          `json:"maxReviewCycles,omitempty"`
}

type NotifyConfig struct {
	// NoBell silences the terminal bell. By default the bell is enabled.
	NoBell bool `json:"noBell,omitempty"`
	// Command is an optional notifier command. The message is appended as the
	// final argument, for example ["notify-send", "Sidekick"].
	Command []string `json:"command,omitempty"`
}

type AgentsConfig struct {
	Planner     AgentConfig   `json:"planner"`
	Implementer AgentConfig   `json:"implementer"`
	Reviewers   []AgentConfig `json:"reviewers"`
	Learner     AgentConfig   `json:"learner"`
}

type AgentConfig struct {
	Name       string   `json:"name"`
	Command    []string `json:"command"`
	PromptMode string   `json:"promptMode"`
	// Model is appended as <ModelFlag> <value> when set. Empty uses the
	// harness default or any model already supplied in Command.
	Model string `json:"model,omitempty"`
	// ModelFlag is the flag used to pass Model. Defaults to "--model".
	ModelFlag string `json:"modelFlag,omitempty"`
	// Prompt overrides the built-in initial prompt. $SIDEKICK_* run variables
	// are expanded when run files are written.
	Prompt string `json:"prompt,omitempty"`
	// Interactive runs the harness attached to the pane's TTY (a live chat)
	// and gates the pipeline on human approval instead of capturing output.
	Interactive bool `json:"interactive,omitempty"`
	// Fallbacks are tried, in order, when this harness fails with output that
	// looks like a usage, quota, or rate-limit failure.
	Fallbacks []AgentConfig `json:"fallbacks,omitempty"`
}

type GateConfig struct {
	Enabled bool     `json:"enabled"`
	Command []string `json:"command"`
}

func Default() Config {
	return Config{
		Agents: AgentsConfig{
			Planner: AgentConfig{
				// Interactive claude chat: prompt seeds the session as an arg
				// (`claude "<prompt>"` stays interactive; -p would be one-shot).
				Name:        "claude-planner",
				Command:     []string{"claude"},
				PromptMode:  "arg",
				Interactive: true,
			},
			Implementer: AgentConfig{
				// codex exec = non-interactive (bare `codex` is a TUI that rejects
				// piped stdin); workspace-write lets it edit the worktree.
				Name:       "codex-implementer",
				Command:    []string{"codex", "exec", "--sandbox", "workspace-write"},
				PromptMode: "stdin",
				Fallbacks: []AgentConfig{
					{Name: "claude-implementer", Command: []string{"claude"}, PromptMode: "stdin"},
				},
			},
			Reviewers: []AgentConfig{
				{Name: "codex-reviewer", Command: []string{"codex", "exec"}, PromptMode: "stdin"},
				{Name: "claude-reviewer", Command: []string{"claude"}, PromptMode: "stdin"},
			},
			Learner: AgentConfig{
				Name:       "claude-learner",
				Command:    []string{"claude"},
				PromptMode: "stdin",
				Fallbacks: []AgentConfig{
					{Name: "codex-learner", Command: []string{"codex", "exec"}, PromptMode: "stdin"},
				},
			},
		},
		Gate: GateConfig{
			Enabled: false,
			Command: []string{"no-mistakes", "-y"},
		},
	}
}

func Load(root string) (Config, error) {
	path := filepath.Join(root, Path)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg.WithDefaults(), nil
}

func (cfg Config) WithDefaults() Config {
	def := Default()
	if cfg.MaxReviewCycles <= 0 {
		cfg.MaxReviewCycles = 3
	}
	if cfg.Agents.Planner.Name == "" {
		cfg.Agents.Planner = def.Agents.Planner
	}
	if cfg.Agents.Implementer.Name == "" {
		cfg.Agents.Implementer = def.Agents.Implementer
	}
	if len(cfg.Agents.Reviewers) == 0 {
		cfg.Agents.Reviewers = def.Agents.Reviewers
	}
	if cfg.Agents.Learner.Name == "" {
		cfg.Agents.Learner = def.Agents.Learner
	}
	if len(cfg.Gate.Command) == 0 {
		cfg.Gate.Command = def.Gate.Command
	}
	return cfg
}

func (cfg Config) AllAgents() []AgentConfig {
	var agents []AgentConfig
	agents = appendAgentAndFallbacks(agents, cfg.Agents.Planner)
	agents = appendAgentAndFallbacks(agents, cfg.Agents.Implementer)
	for _, reviewer := range cfg.Agents.Reviewers {
		agents = appendAgentAndFallbacks(agents, reviewer)
	}
	agents = appendAgentAndFallbacks(agents, cfg.Agents.Learner)
	return agents
}

func appendAgentAndFallbacks(agents []AgentConfig, agent AgentConfig) []AgentConfig {
	agents = append(agents, agent)
	for _, fallback := range agent.Fallbacks {
		agents = appendAgentAndFallbacks(agents, fallback)
	}
	return agents
}

// Send signals that a run needs attention. It is best-effort: terminal bell by
// default, plus an optional user-configured notifier command.
func (n NotifyConfig) Send(msg string) {
	if !n.NoBell {
		fmt.Fprint(os.Stderr, "\a")
	}
	if len(n.Command) > 0 {
		args := append([]string{}, n.Command[1:]...)
		args = append(args, msg)
		_ = exec.Command(n.Command[0], args...).Run()
	}
}

func Normalize(agent AgentConfig) AgentConfig {
	if agent.PromptMode == "" {
		agent.PromptMode = "stdin"
	}
	return agent
}

func SelectAgent(agents []AgentConfig, fallback, name string) (AgentConfig, error) {
	if name == "" {
		name = fallback
	}
	for _, agent := range agents {
		if agent.Name == name {
			return Normalize(agent), nil
		}
	}
	return AgentConfig{}, fmt.Errorf("agent %q not found in %s", name, Path)
}

func ByName(agents []AgentConfig, name string, fallback AgentConfig) AgentConfig {
	for _, agent := range agents {
		if agent.Name == name {
			return agent
		}
	}
	return fallback
}

func RequireBinary(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is required in PATH", name)
	}
	return nil
}

func RequireCommand(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.New("empty command")
	}
	return RequireBinary(command[0])
}

func RequireAgent(agent AgentConfig) error {
	if agent.Name == "" {
		return errors.New("agent name is required")
	}
	if err := RequireCommand(agent.Command); err != nil {
		return fmt.Errorf("%s: %w", agent.Name, err)
	}
	switch Normalize(agent).PromptMode {
	case "stdin", "arg", "file":
		return nil
	default:
		return fmt.Errorf("%s: unsupported promptMode %q", agent.Name, agent.PromptMode)
	}
}
