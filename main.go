package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	configPath = ".sidekick/config.json"
	runRoot    = ".sidekick/runs"
)

type Config struct {
	Agents AgentsConfig `json:"agents"`
	Gate   GateConfig   `json:"gate"`
}

type AgentsConfig struct {
	Planner     AgentConfig   `json:"planner"`
	Implementer AgentConfig   `json:"implementer"`
	Reviewers   []AgentConfig `json:"reviewers"`
}

type AgentConfig struct {
	Name       string   `json:"name"`
	Command    []string `json:"command"`
	PromptMode string   `json:"promptMode"`
}

type GateConfig struct {
	Enabled bool     `json:"enabled"`
	Command []string `json:"command"`
}

type RunState struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	RepoRoot        string    `json:"repoRoot"`
	RunDir          string    `json:"runDir"`
	TaskFile        string    `json:"taskFile"`
	PlanFile        string    `json:"planFile"`
	PlannerDone     string    `json:"plannerDone"`
	ImplementDone   string    `json:"implementDone"`
	WorktreePath    string    `json:"worktreePath"`
	TmuxSession     string    `json:"tmuxSession"`
	GateEnabled     bool      `json:"gateEnabled"`
	PlannerName     string    `json:"plannerName"`
	ImplementerName string    `json:"implementerName"`
	ReviewerNames   []string  `json:"reviewerNames"`
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "sidekick: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return usage()
	}

	switch args[1] {
	case "init":
		return initConfig(args[2:])
	case "run":
		return startRun(args[2:])
	case "agent":
		return runAgent(args[2:])
	case "wait-file":
		return waitFile(args[2:])
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[1], usageText())
	}
}

func usage() error {
	fmt.Fprint(os.Stderr, usageText())
	return nil
}

func usageText() string {
	return `sidekick orchestrates planner, implementer, and reviewer agent harnesses.

Usage:
  sidekick init [--repo PATH]
  sidekick run --task TEXT [--repo PATH] [--planner NAME] [--implementer NAME] [--gate] [--attach]
  sidekick agent --repo PATH --run-dir PATH --role ROLE --prompt FILE --output FILE [--done FILE]
  sidekick wait-file FILE

Typical flow:
  sidekick init --repo /path/to/project
  sidekick run --repo /path/to/project --task "Add X and validate it" --attach
`
}

func initConfig(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}

	path := filepath.Join(root, configPath)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	cfg := defaultConfig()
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

func startRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	task := fs.String("task", "", "task for Sidekick to orchestrate")
	planner := fs.String("planner", "", "planner agent name from config")
	implementer := fs.String("implementer", "", "implementer agent name from config")
	gate := fs.Bool("gate", false, "run the configured no-mistakes gate after implementation")
	attach := fs.Bool("attach", false, "attach to the tmux session after creating it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*task) == "" {
		return errors.New("--task is required")
	}

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}

	plannerAgent, err := selectAgent(cfg.AllAgents(), cfg.Agents.Planner.Name, *planner)
	if err != nil {
		return fmt.Errorf("planner: %w", err)
	}
	implementerAgent, err := selectAgent(cfg.AllAgents(), cfg.Agents.Implementer.Name, *implementer)
	if err != nil {
		return fmt.Errorf("implementer: %w", err)
	}
	if len(cfg.Agents.Reviewers) == 0 {
		return errors.New("at least one reviewer agent is required")
	}

	if err := requireBinary("tmux"); err != nil {
		return err
	}
	if err := requireBinary("treehouse"); err != nil {
		return err
	}
	gateEnabled := *gate || cfg.Gate.Enabled
	if gateEnabled {
		if err := requireCommand(cfg.Gate.Command); err != nil {
			return fmt.Errorf("gate: %w", err)
		}
	}
	for _, agent := range append([]AgentConfig{plannerAgent, implementerAgent}, cfg.Agents.Reviewers...) {
		if err := requireAgent(agent); err != nil {
			return err
		}
	}

	runID := newRunID(*task)
	runDir := filepath.Join(root, runRoot, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}

	worktree, err := leaseWorktree(root, runID)
	if err != nil {
		return err
	}

	state := RunState{
		ID:              runID,
		CreatedAt:       time.Now(),
		RepoRoot:        root,
		RunDir:          runDir,
		TaskFile:        filepath.Join(runDir, "task.md"),
		PlanFile:        filepath.Join(runDir, "plan.md"),
		PlannerDone:     filepath.Join(runDir, "planner.done"),
		ImplementDone:   filepath.Join(runDir, "implement.done"),
		WorktreePath:    worktree,
		TmuxSession:     "sidekick-" + runID,
		GateEnabled:     gateEnabled,
		PlannerName:     plannerAgent.Name,
		ImplementerName: implementerAgent.Name,
	}
	for _, reviewer := range cfg.Agents.Reviewers {
		state.ReviewerNames = append(state.ReviewerNames, reviewer.Name)
	}

	if err := writeRunFiles(state, *task); err != nil {
		return err
	}
	if err := writeState(state); err != nil {
		return err
	}
	if err := createTmuxSession(root, cfg, state, plannerAgent, implementerAgent, gateEnabled); err != nil {
		return err
	}

	fmt.Printf("run: %s\n", state.ID)
	fmt.Printf("session: %s\n", state.TmuxSession)
	fmt.Printf("worktree: %s\n", state.WorktreePath)
	fmt.Printf("state: %s\n", filepath.Join(runDir, "state.json"))
	if *attach {
		return attachTmux(state.TmuxSession)
	}
	fmt.Printf("attach: tmux attach -t %s\n", state.TmuxSession)
	return nil
}

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

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	state, err := loadState(*runDir)
	if err != nil {
		return err
	}
	agent, err := agentForRole(cfg, *role)
	if err != nil {
		return err
	}

	prompt, err := os.ReadFile(*promptPath)
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

	cmd, err := commandForAgent(agent, strings.TrimSpace(string(prompt)), *promptPath)
	if err != nil {
		return err
	}
	cmd.Dir = workDirForRole(state, *role)
	cmd.Env = append(os.Environ(),
		"SIDEKICK_RUN_ID="+state.ID,
		"SIDEKICK_RUN_DIR="+state.RunDir,
		"SIDEKICK_TASK_FILE="+state.TaskFile,
		"SIDEKICK_PLAN_FILE="+state.PlanFile,
		"SIDEKICK_WORKTREE="+state.WorktreePath,
	)
	cmd.Stdin = agentStdin(agent, prompt)
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return err
	}
	if *donePath != "" {
		if err := os.WriteFile(*donePath, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func waitFile(args []string) error {
	if len(args) != 1 {
		return errors.New("wait-file requires exactly one path")
	}
	for {
		if _, err := os.Stat(args[0]); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(2 * time.Second)
	}
}

func defaultConfig() Config {
	return Config{
		Agents: AgentsConfig{
			Planner: AgentConfig{
				Name:       "claude-planner",
				Command:    []string{"claude"},
				PromptMode: "stdin",
			},
			Implementer: AgentConfig{
				Name:       "codex-implementer",
				Command:    []string{"codex"},
				PromptMode: "stdin",
			},
			Reviewers: []AgentConfig{
				{Name: "codex-reviewer", Command: []string{"codex"}, PromptMode: "stdin"},
				{Name: "claude-reviewer", Command: []string{"claude"}, PromptMode: "stdin"},
			},
		},
		Gate: GateConfig{
			Enabled: false,
			Command: []string{"no-mistakes", "-y"},
		},
	}
}

func loadConfig(root string) (Config, error) {
	path := filepath.Join(root, configPath)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := defaultConfig()
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg.withDefaults(), nil
}

func (cfg Config) withDefaults() Config {
	def := defaultConfig()
	if cfg.Agents.Planner.Name == "" {
		cfg.Agents.Planner = def.Agents.Planner
	}
	if cfg.Agents.Implementer.Name == "" {
		cfg.Agents.Implementer = def.Agents.Implementer
	}
	if len(cfg.Agents.Reviewers) == 0 {
		cfg.Agents.Reviewers = def.Agents.Reviewers
	}
	if len(cfg.Gate.Command) == 0 {
		cfg.Gate.Command = def.Gate.Command
	}
	return cfg
}

func (cfg Config) AllAgents() []AgentConfig {
	agents := []AgentConfig{cfg.Agents.Planner, cfg.Agents.Implementer}
	agents = append(agents, cfg.Agents.Reviewers...)
	return agents
}

func repoRoot(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s is not inside a git repository: %s", path, msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func selectAgent(agents []AgentConfig, fallback, name string) (AgentConfig, error) {
	if name == "" {
		name = fallback
	}
	for _, agent := range agents {
		if agent.Name == name {
			return normalizeAgent(agent), nil
		}
	}
	return AgentConfig{}, fmt.Errorf("agent %q not found in %s", name, configPath)
}

func agentForRole(cfg Config, role string) (AgentConfig, error) {
	role = strings.TrimSpace(role)
	if role == "planner" {
		return normalizeAgent(cfg.Agents.Planner), nil
	}
	if role == "implementer" {
		return normalizeAgent(cfg.Agents.Implementer), nil
	}
	for _, reviewer := range cfg.Agents.Reviewers {
		if role == "reviewer-"+slug(reviewer.Name) {
			return normalizeAgent(reviewer), nil
		}
	}
	return AgentConfig{}, fmt.Errorf("unknown role %q", role)
}

func normalizeAgent(agent AgentConfig) AgentConfig {
	if agent.PromptMode == "" {
		agent.PromptMode = "stdin"
	}
	return agent
}

func requireBinary(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is required in PATH", name)
	}
	return nil
}

func requireCommand(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.New("empty command")
	}
	return requireBinary(command[0])
}

func requireAgent(agent AgentConfig) error {
	if agent.Name == "" {
		return errors.New("agent name is required")
	}
	if err := requireCommand(agent.Command); err != nil {
		return fmt.Errorf("%s: %w", agent.Name, err)
	}
	switch normalizeAgent(agent).PromptMode {
	case "stdin", "arg", "file":
		return nil
	default:
		return fmt.Errorf("%s: unsupported promptMode %q", agent.Name, agent.PromptMode)
	}
}

func leaseWorktree(root, runID string) (string, error) {
	cmd := exec.Command("treehouse", "get", "--lease", "--lease-holder", "sidekick:"+runID)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("treehouse lease failed: %s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func writeRunFiles(state RunState, task string) error {
	files := map[string]string{
		state.TaskFile: task + "\n",
		filepath.Join(state.RunDir, "planner.prompt.md"):     plannerPrompt(state),
		filepath.Join(state.RunDir, "implementer.prompt.md"): implementerPrompt(state),
	}
	for _, reviewer := range state.ReviewerNames {
		files[filepath.Join(state.RunDir, "reviewer-"+slug(reviewer)+".prompt.md")] = reviewerPrompt(state, reviewer)
	}
	if state.GateEnabled {
		files[filepath.Join(state.RunDir, "gate.prompt.md")] = gatePrompt(state)
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeState(state RunState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(state.RunDir, "state.json"), data, 0o644)
}

func loadState(runDir string) (RunState, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		return RunState{}, err
	}
	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return RunState{}, err
	}
	return state, nil
}

func createTmuxSession(root string, cfg Config, state RunState, planner, implementer AgentConfig, gate bool) error {
	session := state.TmuxSession
	if err := exec.Command("tmux", "new-session", "-d", "-s", session, "-n", "planner", "-c", root).Run(); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}

	plannerPane, err := tmuxPaneID(session + ":planner")
	if err != nil {
		return err
	}
	plannerCmd := shellJoin(self(), "agent", "--repo", root, "--run-dir", state.RunDir, "--role", "planner", "--prompt", filepath.Join(state.RunDir, "planner.prompt.md"), "--output", state.PlanFile, "--done", state.PlannerDone)
	if err := tmuxSend(plannerPane, plannerCmd); err != nil {
		return err
	}

	if err := exec.Command("tmux", "new-window", "-t", session, "-n", "implement", "-c", state.WorktreePath).Run(); err != nil {
		return err
	}
	implementPane, err := tmuxPaneID(session + ":implement")
	if err != nil {
		return err
	}
	implementCmd := shellJoin(self(), "wait-file", state.PlannerDone) + " && " + shellJoin(self(), "agent", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--role", "implementer", "--prompt", filepath.Join(state.RunDir, "implementer.prompt.md"), "--output", filepath.Join(state.RunDir, "implementer.log"), "--done", state.ImplementDone)
	if err := tmuxSend(implementPane, implementCmd); err != nil {
		return err
	}

	if err := exec.Command("tmux", "new-window", "-t", session, "-n", "review", "-c", state.WorktreePath).Run(); err != nil {
		return err
	}
	reviewPane, err := tmuxPaneID(session + ":review")
	if err != nil {
		return err
	}
	for i, reviewer := range cfg.Agents.Reviewers {
		if i > 0 {
			reviewPane, err = tmuxSplitPane(session+":review", state.WorktreePath)
			if err != nil {
				return err
			}
		}
		role := "reviewer-" + slug(reviewer.Name)
		prompt := filepath.Join(state.RunDir, role+".prompt.md")
		output := filepath.Join(state.RunDir, role+".log")
		reviewerCmd := shellJoin(self(), "wait-file", state.ImplementDone) + " && " + shellJoin(self(), "agent", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--role", role, "--prompt", prompt, "--output", output)
		if err := tmuxSend(reviewPane, reviewerCmd); err != nil {
			return err
		}
	}
	if len(cfg.Agents.Reviewers) > 1 {
		_ = exec.Command("tmux", "select-layout", "-t", session+":review", "tiled").Run()
	}

	if gate {
		if err := exec.Command("tmux", "new-window", "-t", session, "-n", "gate", "-c", state.WorktreePath).Run(); err != nil {
			return err
		}
		gatePane, err := tmuxPaneID(session + ":gate")
		if err != nil {
			return err
		}
		gateCmd := shellJoin(self(), "wait-file", state.ImplementDone) + " && " + shellJoin(cfg.Gate.Command...)
		if err := tmuxSend(gatePane, gateCmd); err != nil {
			return err
		}
	}

	return exec.Command("tmux", "select-window", "-t", session+":planner").Run()
}

func attachTmux(session string) error {
	cmd := exec.Command("tmux", "attach", "-t", session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func tmuxSend(target, command string) error {
	return exec.Command("tmux", "send-keys", "-t", target, command, "Enter").Run()
}

func tmuxPaneID(target string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", target, "#{pane_id}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func tmuxSplitPane(target, dir string) (string, error) {
	out, err := exec.Command("tmux", "split-window", "-P", "-F", "#{pane_id}", "-t", target, "-c", dir).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func commandForAgent(agent AgentConfig, prompt, promptPath string) (*exec.Cmd, error) {
	if len(agent.Command) == 0 {
		return nil, errors.New("empty agent command")
	}
	args := append([]string{}, agent.Command[1:]...)
	switch normalizeAgent(agent).PromptMode {
	case "stdin":
	case "arg":
		args = append(args, prompt)
	case "file":
		args = append(args, promptPath)
	default:
		return nil, fmt.Errorf("unsupported promptMode %q", agent.PromptMode)
	}
	return exec.Command(agent.Command[0], args...), nil
}

func agentStdin(agent AgentConfig, prompt []byte) io.Reader {
	if normalizeAgent(agent).PromptMode == "stdin" {
		return bytes.NewReader(prompt)
	}
	return os.Stdin
}

func workDirForRole(state RunState, role string) string {
	if role == "planner" {
		return state.RepoRoot
	}
	return state.WorktreePath
}

func plannerPrompt(state RunState) string {
	return fmt.Sprintf(`# Sidekick planning task

You are the planning agent for Sidekick run %s.

Read the task in:
%s

Create a concrete, reachable implementation plan. Keep it focused enough for an implementation agent to execute without more user back-and-forth.

Required output:
- Goal statement.
- Assumptions.
- Ordered implementation steps.
- Validation steps.
- Risks or decisions that still require the human.

Do not edit files. Write only the plan.
`, state.ID, state.TaskFile)
}

func implementerPrompt(state RunState) string {
	return fmt.Sprintf(`# Sidekick implementation task

You are the implementation agent for Sidekick run %s.

Task file:
%s

Plan file:
%s

Work in this isolated treehouse worktree:
%s

Execute the plan with the smallest correct change. Keep the worktree reviewable:
- Inspect the repository before editing.
- Reuse local patterns.
- Run relevant validation.
- Do not commit unless the task explicitly requires it.
- Write a concise outcome summary to stdout, including validation results.
`, state.ID, state.TaskFile, state.PlanFile, state.WorktreePath)
}

func reviewerPrompt(state RunState, reviewer string) string {
	return fmt.Sprintf(`# Sidekick review task

You are %s reviewing Sidekick run %s.

Task file:
%s

Plan file:
%s

Review the git changes in this worktree:
%s

Use a code-review stance. Prioritize bugs, regressions, security issues, missing tests, and mismatches with the task or plan.

Required output:
- Findings first, with file and line references when possible.
- Then residual risks or test gaps.
- Then a brief conclusion.

Do not edit files during review.
`, reviewer, state.ID, state.TaskFile, state.PlanFile, state.WorktreePath)
}

func gatePrompt(state RunState) string {
	return fmt.Sprintf(`# Sidekick gate task

Run the configured no-mistakes gate for Sidekick run %s after implementation.

Worktree:
%s
`, state.ID, state.WorktreePath)
}

func newRunID(task string) string {
	stamp := time.Now().Format("20060102-150405")
	s := slug(task)
	if len(s) > 32 {
		s = s[:32]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "task"
	}
	return stamp + "-" + s
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func shellJoin(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if regexp.MustCompile(`^[A-Za-z0-9_./:=@%+-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

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
