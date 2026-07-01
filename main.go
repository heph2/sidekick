package main

import (
	"bufio"
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
	"sort"
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
	Notify NotifyConfig `json:"notify"`
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
	WorktreeBackend string    `json:"worktreeBackend"`
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
		return startRun(nil)
	}

	switch args[1] {
	case "init":
		return initConfig(args[2:])
	case "run":
		return startRun(args[2:])
	case "status":
		return showStatus(args[2:])
	case "agent":
		return runAgent(args[2:])
	case "gate":
		return runGate(args[2:])
	case "wait-file":
		return waitFile(args[2:])
	case "clean":
		return cleanRuns(args[2:])
	case "land":
		return landRun(args[2:])
	case "help", "-h", "--help":
		return usage()
	default:
		// ponytail: bare flags (e.g. `sidekick --no-attach`) mean the hero
		// path, not a subcommand. Anything else is a typo'd command.
		if strings.HasPrefix(args[1], "-") {
			return startRun(args[1:])
		}
		return fmt.Errorf("unknown command %q\n\n%s", args[1], usageText())
	}
}

func usage() error {
	fmt.Fprint(os.Stderr, usageText())
	return nil
}

func usageText() string {
	return mascot() + `

sidekick orchestrates planner, implementer, and reviewer agent harnesses.

Usage:
  sidekick [--task TEXT] [--repo PATH] [--planner NAME] [--implementer NAME] [--gate] [--no-land] [--no-attach]
  sidekick status --run-dir PATH [--watch] [--interval 2s]
  sidekick init [--repo PATH]
  sidekick agent --repo PATH --run-dir PATH --role ROLE --prompt FILE --output FILE [--done FILE]
  sidekick gate --repo PATH --run-dir PATH --output FILE [--done FILE]
  sidekick land --repo PATH --run-dir PATH
  sidekick wait-file FILE
  sidekick clean [--repo PATH] [--run ID]

Typical flow:
  cd /path/to/project
  sidekick          # prompts for the task, leases a worktree, attaches

No config or treehouse setup is required: sidekick uses built-in defaults and
falls back to a plain git worktree when treehouse is unavailable.
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
	noLand := fs.Bool("no-land", false, "do not add the land window that commits/pushes/opens a PR")
	noAttach := fs.Bool("no-attach", false, "do not attach to the tmux session after creating it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	taskText := strings.TrimSpace(*task)
	if taskText == "" {
		got, err := readTask()
		if err != nil {
			return err
		}
		taskText = got
	}
	if taskText == "" {
		return errors.New("task is required")
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

	runID := newRunID(taskText)
	runDir := filepath.Join(root, runRoot, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}

	worktree, backend, err := leaseWorktree(root, runID)
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
		WorktreeBackend: backend,
		TmuxSession:     "sidekick-" + runID,
		GateEnabled:     gateEnabled,
		PlannerName:     plannerAgent.Name,
		ImplementerName: implementerAgent.Name,
	}
	for _, reviewer := range cfg.Agents.Reviewers {
		state.ReviewerNames = append(state.ReviewerNames, reviewer.Name)
	}

	if err := writeRunFiles(state, taskText, cfg); err != nil {
		return err
	}
	if err := writeState(state); err != nil {
		return err
	}
	if err := createTmuxSession(root, cfg, state, gateEnabled, !*noLand); err != nil {
		return err
	}

	fmt.Printf("run: %s\n", state.ID)
	fmt.Printf("session: %s\n", state.TmuxSession)
	fmt.Printf("worktree: %s\n", state.WorktreePath)
	fmt.Printf("state: %s\n", filepath.Join(runDir, "state.json"))
	if *noAttach {
		fmt.Printf("attach: tmux attach -t %s\n", state.TmuxSession)
		return nil
	}
	return attachTmux(state.TmuxSession)
}

// readTask sources the task when --task is absent: an interactive prompt on a
// TTY, otherwise all of piped stdin.
func readTask() (string, error) {
	// ponytail: ModeCharDevice tty check, no x/term dep
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprint(os.Stderr, "Task> ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func showStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	runDir := fs.String("run-dir", "", "run directory")
	watch := fs.Bool("watch", false, "redraw status until interrupted")
	interval := fs.Duration("interval", 2*time.Second, "watch redraw interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return errors.New("--run-dir is required")
	}

	if *watch {
		state, err := loadState(*runDir)
		if err != nil {
			return err
		}
		cfg, err := loadConfig(state.RepoRoot)
		if err != nil {
			return err
		}
		previousPhase := ""
		for {
			fmt.Print("\033[H\033[2J")
			if err := renderStatus(os.Stdout, *runDir, terminalWidth()); err != nil {
				return err
			}
			view, err := buildStatusView(*runDir)
			if err != nil {
				return err
			}
			if previousPhase != "" && view.Phase != previousPhase {
				switch {
				case view.Phase == "complete":
					notify(cfg, "Sidekick: run complete")
				case strings.HasPrefix(view.Phase, "failed:"):
					notify(cfg, "Sidekick: "+view.Phase)
				}
			}
			previousPhase = view.Phase
			time.Sleep(*interval)
		}
	}
	return renderStatus(os.Stdout, *runDir, terminalWidth())
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

	env := append(os.Environ(),
		"SIDEKICK_RUN_ID="+state.ID,
		"SIDEKICK_RUN_DIR="+state.RunDir,
		"SIDEKICK_TASK_FILE="+state.TaskFile,
		"SIDEKICK_PLAN_FILE="+state.PlanFile,
		"SIDEKICK_WORKTREE="+state.WorktreePath,
	)

	if normalizeAgent(agent).Interactive {
		return runInteractiveAgent(cfg, agent, prompt, *promptPath, workDirForRole(state, *role), env, *donePath)
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
	cmd.Env = env
	cmd.Stdin = agentStdin(agent, prompt)
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	// tee stderr to the log too, else agent failures leave an empty log
	cmd.Stderr = io.MultiWriter(os.Stderr, out)

	if err := cmd.Run(); err != nil {
		if *donePath != "" {
			_ = markFile(*donePath + ".failed")
		}
		return err
	}
	if *donePath != "" {
		if err := markFile(*donePath); err != nil {
			return err
		}
	}
	return nil
}

// runInteractiveAgent attaches the harness to the pane's TTY (a live chat, no
// output capture) so its native UI renders, then gates the pipeline on human
// approval: only "y" releases downstream, anything else aborts it.
func runInteractiveAgent(cfg Config, agent AgentConfig, prompt []byte, promptPath, dir string, env []string, donePath string) error {
	cmd, err := commandForAgent(agent, strings.TrimSpace(string(prompt)), promptPath)
	if err != nil {
		return err
	}
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if donePath != "" {
			_ = markFile(donePath + ".failed")
		}
		return err
	}
	if donePath == "" {
		return nil
	}
	notify(cfg, "Sidekick: plan ready, release implementer?")
	if confirmRelease(os.Stdin, os.Stdout) {
		return markFile(donePath)
	}
	fmt.Fprintln(os.Stdout, "not released; downstream steps aborted")
	return markFile(donePath + ".failed")
}

// confirmRelease asks the human whether to release downstream steps. Only an
// explicit "y"/"yes" approves; EOF or anything else declines.
func confirmRelease(in io.Reader, out io.Writer) bool {
	return promptYesNo(in, out, "\nPlan ready? release implementer? [y/N] ")
}

// promptYesNo reads one line; only "y"/"yes" is true. EOF/anything else false.
func promptYesNo(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprint(out, question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
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

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if err := requireCommand(cfg.Gate.Command); err != nil {
		return err
	}
	state, err := loadState(*runDir)
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
	cmd.Env = append(os.Environ(),
		"SIDEKICK_RUN_ID="+state.ID,
		"SIDEKICK_RUN_DIR="+state.RunDir,
		"SIDEKICK_TASK_FILE="+state.TaskFile,
		"SIDEKICK_PLAN_FILE="+state.PlanFile,
		"SIDEKICK_WORKTREE="+state.WorktreePath,
	)
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	cmd.Stderr = io.MultiWriter(os.Stderr, out)
	if err := cmd.Run(); err != nil {
		if *donePath != "" {
			_ = markFile(*donePath + ".failed")
		}
		return err
	}
	if *donePath != "" {
		return markFile(*donePath)
	}
	return nil
}

func waitFile(args []string) error {
	if len(args) != 1 {
		return errors.New("wait-file requires exactly one path")
	}
	failed := args[0] + ".failed"
	for {
		if _, err := os.Stat(args[0]); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if fileExists(failed) {
			return fmt.Errorf("upstream step failed: %s", failed)
		}
		time.Sleep(2 * time.Second)
	}
}

func defaultConfig() Config {
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
			},
			Reviewers: []AgentConfig{
				{Name: "codex-reviewer", Command: []string{"codex", "exec"}, PromptMode: "stdin"},
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

// leaseWorktree gets an isolated worktree, preferring treehouse when it is
// available and falling back to a plain git worktree otherwise. It returns the
// worktree path and the backend used ("treehouse" or "git").
func leaseWorktree(root, runID string) (string, string, error) {
	if _, err := exec.LookPath("treehouse"); err == nil {
		cmd := exec.Command("treehouse", "get", "--lease", "--lease-holder", "sidekick:"+runID)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out)), "treehouse", nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr, "sidekick: treehouse lease failed (%s); using git worktree\n", msg)
	}
	return gitWorktree(root, runID)
}

func gitWorktree(root, runID string) (string, string, error) {
	// ponytail: branch off current HEAD, worktree lives in the ignored .sidekick tree
	path := filepath.Join(root, ".sidekick", "worktrees", runID)
	cmd := exec.Command("git", "-C", root, "worktree", "add", "-b", "sidekick/"+runID, path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", "", fmt.Errorf("git worktree add failed: %s", msg)
	}
	return path, "git", nil
}

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
	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}

	runsDir := filepath.Join(root, runRoot)
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
		if state, err := loadState(runDir); err == nil {
			if state.WorktreeBackend == "git" && state.WorktreePath != "" {
				runQuiet("git", "-C", root, "worktree", "remove", "--force", state.WorktreePath)
				runQuiet("git", "-C", root, "branch", "-D", "sidekick/"+state.ID)
			}
			// only touch the session sidekick created for this run
			if state.TmuxSession != "" {
				runQuiet("tmux", "kill-session", "-t", state.TmuxSession)
			}
		}
		if err := os.RemoveAll(runDir); err != nil {
			return err
		}
		fmt.Printf("cleaned %s\n", e.Name())
		cleaned++
	}
	runQuiet("git", "-C", root, "worktree", "prune")
	if cleaned == 0 {
		fmt.Println("no runs to clean")
	}
	return nil
}

// runQuiet runs a command and ignores its outcome; used for best-effort cleanup.
func runQuiet(name string, args ...string) {
	_ = exec.Command(name, args...).Run()
}

// notify signals that a run needs attention. It is best-effort: terminal bell by
// default, plus an optional user-configured notifier command.
func notify(cfg Config, msg string) {
	if !cfg.Notify.NoBell {
		fmt.Fprint(os.Stderr, "\a")
	}
	if len(cfg.Notify.Command) > 0 {
		args := append([]string{}, cfg.Notify.Command[1:]...)
		args = append(args, msg)
		runQuiet(cfg.Notify.Command[0], args...)
	}
}

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
	state, err := loadState(*runDir)
	if err != nil {
		return err
	}
	wt := state.WorktreePath
	if wt == "" {
		return errors.New("no worktree path in run state")
	}

	goal := firstLine(state.TaskFile)
	if goal == "" {
		goal = state.ID
	}
	branch, changed, err := landCommit(wt, goal)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println("nothing to land: worktree has no changes")
		return nil
	}
	fmt.Printf("committed on branch %s\n", branch)

	cfg, err := loadConfig(state.RepoRoot)
	if err != nil {
		return err
	}
	notify(cfg, "Sidekick: ready to push "+branch+" and open a PR")
	if !promptYesNo(os.Stdin, os.Stdout, fmt.Sprintf("Push %s and open a PR? [y/N] ", branch)) {
		fmt.Printf("left local; push later with: git -C %s push -u origin %s\n", wt, branch)
		return nil
	}

	push := exec.Command("git", "-C", wt, "push", "-u", "origin", branch)
	push.Stdout, push.Stderr = os.Stdout, os.Stderr
	if err := push.Run(); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	if _, err := exec.LookPath("gh"); err != nil {
		fmt.Printf("gh not found; open a PR for %s from your git host\n", branch)
		return nil
	}
	pr := exec.Command("gh", "pr", "create", "--fill", "--head", branch)
	pr.Dir = wt
	pr.Stdin, pr.Stdout, pr.Stderr = os.Stdin, os.Stdout, os.Stderr
	return pr.Run()
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

func writeRunFiles(state RunState, task string, cfg Config) error {
	planner := agentConfigByName(cfg.AllAgents(), state.PlannerName, cfg.Agents.Planner)
	implementer := agentConfigByName(cfg.AllAgents(), state.ImplementerName, cfg.Agents.Implementer)
	files := map[string]string{
		state.TaskFile: task + "\n",
		filepath.Join(state.RunDir, "planner.prompt.md"):     plannerPrompt(state, planner),
		filepath.Join(state.RunDir, "implementer.prompt.md"): implementerPrompt(state, implementer),
	}
	for _, reviewer := range state.ReviewerNames {
		agent := agentConfigByName(cfg.Agents.Reviewers, reviewer, AgentConfig{Name: reviewer})
		files[filepath.Join(state.RunDir, "reviewer-"+slug(reviewer)+".prompt.md")] = reviewerPrompt(state, agent)
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

func agentConfigByName(agents []AgentConfig, name string, fallback AgentConfig) AgentConfig {
	for _, agent := range agents {
		if agent.Name == name {
			return agent
		}
	}
	return fallback
}

type PipelineStep struct {
	Name   string
	Status string
	Log    string
}

type StatusView struct {
	State       RunState
	Goal        string
	Phase       string
	Elapsed     time.Duration
	Steps       []PipelineStep
	RecentTitle string
	RecentLines []string
}

func renderStatus(w io.Writer, runDir string, width int) error {
	view, err := buildStatusView(runDir)
	if err != nil {
		return err
	}
	if width < 60 {
		width = 60
	}
	if width > 120 {
		width = 120
	}

	fmt.Fprint(w, col("36", mascot()))
	fmt.Fprintf(w, "\n%s\n", strings.Repeat("=", width))
	fmt.Fprintf(w, "Sidekick run: %s\n", view.State.ID)
	fmt.Fprintf(w, "Phase: %s | Elapsed: %s\n", col(phaseColor(view.Phase), view.Phase), view.Elapsed.Round(time.Second))
	fmt.Fprintf(w, "Goal: %s\n", col("1", clip(view.Goal, width-6)))
	fmt.Fprintf(w, "%s\n\n", strings.Repeat("=", width))

	fmt.Fprintln(w, "Pipeline")
	for _, step := range view.Steps {
		mark := col(statusColor(step.Status), "["+statusMark(step.Status)+"]")
		fmt.Fprintf(w, "  %s %-18s %s\n", mark, step.Name, col(statusColor(step.Status), step.Status))
	}

	fmt.Fprintln(w, "\nArtifacts")
	fmt.Fprintf(w, "  worktree: %s\n", clip(view.State.WorktreePath, width-12))
	fmt.Fprintf(w, "  plan:     %s\n", clip(view.State.PlanFile, width-12))
	fmt.Fprintf(w, "  logs:     %s\n", clip(view.State.RunDir, width-12))
	fmt.Fprintf(w, "  attach:   tmux attach -t %s\n", view.State.TmuxSession)

	fmt.Fprintf(w, "\nRecent: %s\n", view.RecentTitle)
	if len(view.RecentLines) == 0 {
		fmt.Fprintln(w, "  waiting for agent output...")
		return nil
	}
	for _, line := range view.RecentLines {
		fmt.Fprintf(w, "  %s\n", clip(line, width-4))
	}
	return nil
}

func buildStatusView(runDir string) (StatusView, error) {
	state, err := loadState(runDir)
	if err != nil {
		return StatusView{}, err
	}
	view := StatusView{
		State:   state,
		Goal:    firstLine(state.TaskFile),
		Elapsed: time.Since(state.CreatedAt),
	}
	view.Steps = append(view.Steps, PipelineStep{Name: "planner", Status: stepStatus(state.PlannerDone, state.PlanFile), Log: state.PlanFile})
	view.Steps = append(view.Steps, PipelineStep{Name: "implementer", Status: stepStatus(state.ImplementDone, implementerLog(state)), Log: implementerLog(state)})
	for _, reviewer := range state.ReviewerNames {
		log := reviewerLog(state, reviewer)
		done := reviewerDone(state, reviewer)
		view.Steps = append(view.Steps, PipelineStep{Name: "review " + reviewer, Status: gatedStepStatus(state.ImplementDone, done, log), Log: log})
	}
	if state.GateEnabled {
		log := gateLog(state)
		view.Steps = append(view.Steps, PipelineStep{Name: "gate", Status: gatedStepStatus(state.ImplementDone, gateDone(state), log), Log: log})
	}
	view.Phase = currentPhase(view.Steps)
	view.RecentTitle, view.RecentLines = recentOutput(view.Steps)
	return view, nil
}

func stepStatus(donePath, logPath string) string {
	if fileExists(donePath + ".failed") {
		return "failed"
	}
	if fileExists(donePath) {
		return "done"
	}
	if fileExists(logPath) {
		return "running"
	}
	return "waiting"
}

func gatedStepStatus(prerequisiteDonePath, donePath, logPath string) string {
	if !fileExists(prerequisiteDonePath) {
		return "waiting"
	}
	return stepStatus(donePath, logPath)
}

func currentPhase(steps []PipelineStep) string {
	for _, step := range steps {
		if step.Status == "failed" {
			return "failed: " + step.Name
		}
		if step.Status != "done" {
			return step.Name
		}
	}
	return "complete"
}

func recentOutput(steps []PipelineStep) (string, []string) {
	active := append([]PipelineStep(nil), steps...)
	sort.SliceStable(active, func(i, j int) bool {
		return statusRank(active[i].Status) < statusRank(active[j].Status)
	})
	for _, step := range active {
		lines := lastLines(step.Log, 8)
		if len(lines) > 0 {
			return step.Name, lines
		}
	}
	return "none", nil
}

func statusRank(status string) int {
	switch status {
	case "failed":
		return 0
	case "running":
		return 1
	case "done":
		return 2
	default:
		return 3
	}
}

// col wraps s in an ANSI color unless NO_COLOR is set or stdout is not a TTY.
func col(code, s string) string {
	if code == "" || os.Getenv("NO_COLOR") != "" || !stdoutIsTTY() {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func phaseColor(phase string) string {
	switch {
	case strings.HasPrefix(phase, "failed"):
		return "31" // red
	case phase == "complete":
		return "32" // green
	default:
		return "33" // yellow
	}
}

func statusColor(status string) string {
	switch status {
	case "failed":
		return "31" // red
	case "done":
		return "32" // green
	case "running":
		return "33" // yellow
	default:
		return "2" // dim
	}
}

func statusMark(status string) string {
	switch status {
	case "failed":
		return "!"
	case "done":
		return "x"
	case "running":
		return ">"
	default:
		return " "
	}
}

func firstLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "empty task"
}

func lastLines(path string, count int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var lines []string
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) <= count {
		return lines
	}
	return lines[len(lines)-count:]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func implementerLog(state RunState) string {
	return filepath.Join(state.RunDir, "implementer.log")
}

func reviewerLog(state RunState, reviewer string) string {
	return filepath.Join(state.RunDir, "reviewer-"+slug(reviewer)+".log")
}

func reviewerDone(state RunState, reviewer string) string {
	return filepath.Join(state.RunDir, "reviewer-"+slug(reviewer)+".done")
}

func gateLog(state RunState) string {
	return filepath.Join(state.RunDir, "gate.log")
}

func gateDone(state RunState) string {
	return filepath.Join(state.RunDir, "gate.done")
}

func markFile(path string) error {
	return os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

func terminalWidth() int {
	if columns := strings.TrimSpace(os.Getenv("COLUMNS")); columns != "" {
		var width int
		if _, err := fmt.Sscanf(columns, "%d", &width); err == nil && width > 0 {
			return width
		}
	}
	return 100
}

func clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func mascot() string {
	return `       /\  /\        Sidekick
      /  \/  \       wood-hero support console
     / /\  /\ \
    | |  ||  | |
    | |__||__| |
    |  \____/  |
   /|  /||||\  |\
  /_|_/ |||| \_|_\
     /\_||||_/\
    /__/    \__\`
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

func createTmuxSession(root string, cfg Config, state RunState, gate, land bool) error {
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

	if err := exec.Command("tmux", "new-window", "-t", session, "-n", "dashboard", "-c", state.WorktreePath).Run(); err != nil {
		return err
	}
	dashboardPane, err := tmuxPaneID(session + ":dashboard")
	if err != nil {
		return err
	}
	dashboardCmd := shellJoin(self(), "status", "--run-dir", state.RunDir, "--watch")
	if err := tmuxSend(dashboardPane, dashboardCmd); err != nil {
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
		reviewerCmd := shellJoin(self(), "wait-file", state.ImplementDone) + " && " + shellJoin(self(), "agent", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--role", role, "--prompt", prompt, "--output", output, "--done", reviewerDone(state, reviewer.Name))
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
		gateCmd := shellJoin(self(), "wait-file", state.ImplementDone) + " && " + shellJoin(self(), "gate", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--output", gateLog(state), "--done", gateDone(state))
		if err := tmuxSend(gatePane, gateCmd); err != nil {
			return err
		}
	}

	if land {
		if err := exec.Command("tmux", "new-window", "-t", session, "-n", "land", "-c", state.WorktreePath).Run(); err != nil {
			return err
		}
		landPane, err := tmuxPaneID(session + ":land")
		if err != nil {
			return err
		}
		// wait for implementation (and the gate, when enabled) before landing
		landCmd := shellJoin(self(), "wait-file", state.ImplementDone)
		if gate {
			landCmd += " && " + shellJoin(self(), "wait-file", gateDone(state))
		}
		landCmd += " && " + shellJoin(self(), "land", "--repo", state.WorktreePath, "--run-dir", state.RunDir)
		if err := tmuxSend(landPane, landCmd); err != nil {
			return err
		}
	}

	return exec.Command("tmux", "select-window", "-t", session+":dashboard").Run()
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
	if model := strings.TrimSpace(agent.Model); model != "" {
		flag := strings.TrimSpace(agent.ModelFlag)
		if flag == "" {
			flag = "--model"
		}
		args = append(args, flag, model)
	}
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

func expandPrompt(tmpl string, state RunState) string {
	return os.Expand(tmpl, func(key string) string {
		switch key {
		case "SIDEKICK_RUN_ID":
			return state.ID
		case "SIDEKICK_RUN_DIR":
			return state.RunDir
		case "SIDEKICK_TASK_FILE":
			return state.TaskFile
		case "SIDEKICK_PLAN_FILE":
			return state.PlanFile
		case "SIDEKICK_WORKTREE":
			return state.WorktreePath
		default:
			return ""
		}
	})
}

func plannerPrompt(state RunState, agent AgentConfig) string {
	if strings.TrimSpace(agent.Prompt) != "" {
		return expandPrompt(agent.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick planning task (interactive)

You are the planning agent for Sidekick run %s. This is a back-and-forth chat
with the human. Discuss and refine the plan with them until they are satisfied.

Read the task in:
%s

Produce a concrete, reachable implementation plan an implementation agent can
execute without further human back-and-forth:
- Goal statement.
- Assumptions.
- Ordered implementation steps.
- Validation steps.
- Risks or decisions that still require the human.

When the human is happy, WRITE the final plan to this file (path is also in
$SIDEKICK_PLAN_FILE):
%s

Do not edit any other files -- write only the plan file. When the plan file is
saved and the human is done, end the session; Sidekick will ask them to release
the implementer.
`, state.ID, state.TaskFile, state.PlanFile)
}

func implementerPrompt(state RunState, agent AgentConfig) string {
	if strings.TrimSpace(agent.Prompt) != "" {
		return expandPrompt(agent.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick implementation task

You are the implementation agent for Sidekick run %s.

Task file:
%s

Plan file:
%s

Work in this isolated worktree:
%s

Execute the plan with the smallest correct change. Keep the worktree reviewable:
- Inspect the repository before editing.
- Reuse local patterns.
- Run relevant validation.
- Do not commit unless the task explicitly requires it.
- Write a concise outcome summary to stdout, including validation results.
`, state.ID, state.TaskFile, state.PlanFile, state.WorktreePath)
}

func reviewerPrompt(state RunState, agent AgentConfig) string {
	if strings.TrimSpace(agent.Prompt) != "" {
		return expandPrompt(agent.Prompt, state)
	}
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
`, agent.Name, state.ID, state.TaskFile, state.PlanFile, state.WorktreePath)
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
