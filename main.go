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
	MemoryFile      string    `json:"memoryFile"`
	PlannerDone     string    `json:"plannerDone"`
	ImplementDone   string    `json:"implementDone"`
	LearnDone       string    `json:"learnDone"`
	WorktreePath    string    `json:"worktreePath"`
	WorktreeBackend string    `json:"worktreeBackend"`
	TmuxSession     string    `json:"tmuxSession"`
	GateEnabled     bool      `json:"gateEnabled"`
	LearnEnabled    bool      `json:"learnEnabled"`
	PlannerName     string    `json:"plannerName"`
	ImplementerName string    `json:"implementerName"`
	LearnerName     string    `json:"learnerName"`
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
		return startConsole(nil)
	}

	switch args[1] {
	case "init":
		return initConfig(args[2:])
	case "wizard":
		return runWizard(args[2:])
	case "run":
		return startRun(args[2:])
	case "console":
		return consoleLoop(args[2:])
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
	case "release":
		return signalPlanner(args[2:], false)
	case "abort":
		return signalPlanner(args[2:], true)
	case "help", "-h", "--help":
		return usage()
	default:
		// ponytail: bare flags (e.g. `sidekick --no-attach`) mean the hero
		// path, not a subcommand. Anything else is a typo'd command.
		if strings.HasPrefix(args[1], "-") {
			return startConsole(args[1:])
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
  sidekick [--repo PATH] [--gate] [--no-learn] [--no-land] [--no-attach]
  sidekick run [--task TEXT] [--repo PATH] [--planner NAME] [--implementer NAME] [--gate] [--no-learn] [--no-land] [--no-attach]
  sidekick console [--repo PATH] [--gate] [--no-learn] [--no-land]
  sidekick status --run-dir PATH [--watch] [--interval 2s]
  sidekick init [--repo PATH]
  sidekick wizard [--repo PATH]
  sidekick agent --repo PATH --run-dir PATH --role ROLE --prompt FILE --output FILE [--done FILE]
  sidekick gate --repo PATH --run-dir PATH --output FILE [--done FILE]
  sidekick land --repo PATH --run-dir PATH
  sidekick release [--repo PATH] [--run-dir PATH]   # approve the newest waiting plan
  sidekick abort   [--repo PATH] [--run-dir PATH]   # cancel it instead
  sidekick wait-file FILE
  sidekick clean [--repo PATH] [--run ID]

Typical flow:
  cd /path/to/project
  sidekick          # opens a persistent console; keep giving it tasks
  sidekick run --task "..." --no-attach   # scriptable one-shot run

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

func runWizard(args []string) error {
	fs := flag.NewFlagSet("wizard", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}
	path := filepath.Join(root, configPath)

	cfg := defaultConfig()
	exists := false
	if _, err := os.Stat(path); err == nil {
		exists = true
		cfg, err = loadConfig(root)
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

func promptAgent(sc *bufio.Scanner, role string, cur AgentConfig) AgentConfig {
	defHarness := inferHarness(cur)
	harness := strings.ToLower(ask(sc, fmt.Sprintf("%s harness (claude/codex/custom)", role), defHarness))
	return agentFromHarness(sc, role, cur, harness)
}

func promptReviewers(sc *bufio.Scanner, current []AgentConfig) []AgentConfig {
	var reviewers []AgentConfig
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
			reviewers = append(reviewers, agentFromHarness(sc, "reviewer", AgentConfig{}, harness))
		}
	}
	return uniquifyReviewerNames(reviewers)
}

func agentFromHarness(sc *bufio.Scanner, role string, cur AgentConfig, harness string) AgentConfig {
	switch harness {
	case "claude", "codex":
		agent := presetAgent(harness, role)
		agent.Name = defaultAgentName(harness, role)
		agent.Model = ask(sc, role+" model", cur.Model)
		agent.Prompt = cur.Prompt
		if harness == inferHarness(cur) && cur.Name != "" {
			agent.Interactive = cur.Interactive
		}
		return agent
	default:
		command := ask(sc, role+" command", strings.Join(cur.Command, " "))
		agent := AgentConfig{
			Name:        cur.Name,
			Command:     strings.Fields(command),
			PromptMode:  ask(sc, role+" prompt mode", defaultPromptMode(cur)),
			Model:       ask(sc, role+" model", cur.Model),
			Prompt:      cur.Prompt,
			Interactive: cur.Interactive,
		}
		if agent.Name == "" {
			agent.Name = "custom-" + role
		}
		return agent
	}
}

func presetAgent(harness, role string) AgentConfig {
	switch harness {
	case "claude":
		if role == "planner" {
			return AgentConfig{Command: []string{"claude"}, PromptMode: "arg", Interactive: true}
		}
		return AgentConfig{Command: []string{"claude"}, PromptMode: "stdin"}
	case "codex":
		if role == "implementer" {
			return AgentConfig{Command: []string{"codex", "exec", "--sandbox", "workspace-write"}, PromptMode: "stdin"}
		}
		return AgentConfig{Command: []string{"codex", "exec"}, PromptMode: "stdin"}
	default:
		return AgentConfig{PromptMode: "stdin"}
	}
}

func inferHarness(agent AgentConfig) string {
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

func defaultPromptMode(agent AgentConfig) string {
	if agent.PromptMode != "" {
		return agent.PromptMode
	}
	return "stdin"
}

func defaultAgentName(harness, role string) string {
	return harness + "-" + role
}

func uniquifyReviewerNames(reviewers []AgentConfig) []AgentConfig {
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

func startRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	task := fs.String("task", "", "task for Sidekick to orchestrate")
	planner := fs.String("planner", "", "planner agent name from config")
	implementer := fs.String("implementer", "", "implementer agent name from config")
	gate := fs.Bool("gate", false, "run the configured no-mistakes gate after implementation")
	noLearn := fs.Bool("no-learn", false, "do not run the post-implementation learner that updates .sidekick/memory.md")
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
	cfg.Agents.Planner = plannerAgent
	cfg.Agents.Implementer = implementerAgent
	if len(cfg.Agents.Reviewers) == 0 {
		return errors.New("at least one reviewer agent is required")
	}

	gateEnabled := *gate || cfg.Gate.Enabled
	if err := validateAgents(cfg, gateEnabled, !*noLearn); err != nil {
		return err
	}

	runID := newRunID(taskText)
	session := "sidekick-" + runID
	bootstrap, err := newBootstrapSession(session, root)
	if err != nil {
		return err
	}

	state, err := spawnRun(session, "", root, cfg, taskText, gateEnabled, !*noLearn, !*noLand)
	if err != nil {
		return err
	}
	_ = exec.Command("tmux", "kill-window", "-t", session+":"+bootstrap).Run()
	if err := exec.Command("tmux", "select-window", "-t", session+":dashboard").Run(); err != nil {
		return err
	}

	fmt.Printf("run: %s\n", state.ID)
	fmt.Printf("session: %s\n", state.TmuxSession)
	fmt.Printf("worktree: %s\n", state.WorktreePath)
	fmt.Printf("state: %s\n", filepath.Join(state.RunDir, "state.json"))
	if *noAttach {
		fmt.Printf("attach: tmux attach -t %s\n", state.TmuxSession)
		return nil
	}
	return attachTmux(state.TmuxSession)
}

// validateAgents checks that tmux, the gate command (when enabled), and every
// configured agent needed by this run are runnable before any run is spawned.
func validateAgents(cfg Config, gateEnabled, learnEnabled bool) error {
	if err := requireBinary("tmux"); err != nil {
		return err
	}
	if gateEnabled {
		if err := requireCommand(cfg.Gate.Command); err != nil {
			return fmt.Errorf("gate: %w", err)
		}
	}
	agents := []AgentConfig{cfg.Agents.Planner, cfg.Agents.Implementer}
	agents = append(agents, cfg.Agents.Reviewers...)
	if learnEnabled {
		agents = append(agents, cfg.Agents.Learner)
	}
	for _, agent := range agents {
		if err := requireAgent(agent); err != nil {
			return err
		}
	}
	return nil
}

// startConsole is the bare `sidekick` entry point: it opens a persistent
// session with a single "console" window running `sidekick console`, which
// prompts for tasks in a loop and spawns each one into its own windows within
// the same session.
func startConsole(args []string) error {
	fs := flag.NewFlagSet("console-launcher", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	gate := fs.Bool("gate", false, "run the configured no-mistakes gate after implementation")
	noLearn := fs.Bool("no-learn", false, "do not run the post-implementation learner that updates .sidekick/memory.md")
	noLand := fs.Bool("no-land", false, "do not add the land window that commits/pushes/opens a PR")
	noAttach := fs.Bool("no-attach", false, "do not attach to the tmux session after creating it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if len(cfg.Agents.Reviewers) == 0 {
		return errors.New("at least one reviewer agent is required")
	}
	gateEnabled := *gate || cfg.Gate.Enabled
	if err := validateAgents(cfg, gateEnabled, !*noLearn); err != nil {
		return err
	}

	stamp := time.Now().Format("20060102-150405")
	session := "sidekick-" + stamp
	consoleArgs := []string{self(), "console", "--repo", root}
	if gateEnabled {
		consoleArgs = append(consoleArgs, "--gate")
	}
	if *noLearn {
		consoleArgs = append(consoleArgs, "--no-learn")
	}
	if *noLand {
		consoleArgs = append(consoleArgs, "--no-land")
	}
	consoleCmd := shellJoin(consoleArgs...)
	if err := exec.Command("tmux", "new-session", "-d", "-s", session, "-n", "console", "-c", root).Run(); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}
	consolePane, err := tmuxPaneID(session + ":console")
	if err != nil {
		return err
	}
	if err := tmuxSend(consolePane, consoleCmd); err != nil {
		return err
	}

	fmt.Printf("session: %s\n", session)
	if *noAttach {
		fmt.Printf("attach: tmux attach -t %s\n", session)
		return nil
	}
	return attachTmux(session)
}

// consoleLoop runs inside the console window: it repeatedly prompts for a
// task and spawns it into new windows in the current session, without ever
// blocking on a run's completion, so the console is free for the next task
// while prior runs proceed asynchronously.
func consoleLoop(args []string) error {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	repo := fs.String("repo", ".", "target repository")
	gate := fs.Bool("gate", false, "run the configured no-mistakes gate after implementation")
	noLearn := fs.Bool("no-learn", false, "do not run the post-implementation learner that updates .sidekick/memory.md")
	noLand := fs.Bool("no-land", false, "do not add the land window that commits/pushes/opens a PR")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := repoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if len(cfg.Agents.Reviewers) == 0 {
		return errors.New("at least one reviewer agent is required")
	}
	gateEnabled := *gate || cfg.Gate.Enabled
	if err := validateAgents(cfg, gateEnabled, !*noLearn); err != nil {
		return err
	}

	sessionOut, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return fmt.Errorf("resolve tmux session: %w", err)
	}
	session := strings.TrimSpace(string(sessionOut))

	land := !*noLand
	learn := !*noLearn
	in := bufio.NewReader(os.Stdin)
	for idx := 1; ; idx++ {
		fmt.Print(mascotColored())
		fmt.Println()
		fmt.Println("Give Sidekick a task; each one gets its own windows. Type quit/exit to leave the console.")

		taskText, eof, err := consoleReadLine(in)
		if err != nil {
			return err
		}
		if eof {
			return nil
		}
		if taskText == "" {
			idx--
			continue
		}
		switch strings.ToLower(taskText) {
		case "quit", "exit":
			return nil
		}

		prefix := fmt.Sprintf("t%d-", idx)
		state, err := spawnRun(session, prefix, root, cfg, taskText, gateEnabled, learn, land)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sidekick: %v\n", err)
			idx--
			continue
		}
		fmt.Printf("run %s started; windows %s*\n\n", state.ID, prefix)
	}
}

// consoleReadLine reads one line for the console loop, reporting EOF (e.g.
// Ctrl-D or a closed pipe) distinctly from a blank line so the loop can exit
// on EOF but reprompt on blank input.
func consoleReadLine(in *bufio.Reader) (line string, eof bool, err error) {
	fmt.Fprint(os.Stderr, "Task> ")
	text, err := in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(text), true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(text), false, nil
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
		"SIDEKICK_MEMORY_FILE="+state.MemoryFile,
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
	runErr := cmd.Run()
	if donePath == "" {
		return runErr
	}
	// A `sidekick release`/`abort` from another pane may have already resolved
	// this run; never clobber that decision (also means killing the planner
	// window after releasing elsewhere won't spuriously mark it failed).
	if fileExists(donePath) || fileExists(donePath+".failed") {
		return runErr
	}
	if runErr != nil {
		_ = markFile(donePath + ".failed")
		return runErr
	}
	notify(cfg, "Sidekick: plan ready, release implementer?")
	fmt.Fprintln(os.Stdout, "(or run `sidekick release` in another pane; `sidekick abort` to cancel)")
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
		"SIDEKICK_MEMORY_FILE="+state.MemoryFile,
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
			Learner: AgentConfig{
				Name:       "claude-learner",
				Command:    []string{"claude"},
				PromptMode: "stdin",
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
	if cfg.Agents.Learner.Name == "" {
		cfg.Agents.Learner = def.Agents.Learner
	}
	if len(cfg.Gate.Command) == 0 {
		cfg.Gate.Command = def.Gate.Command
	}
	return cfg
}

func (cfg Config) AllAgents() []AgentConfig {
	agents := []AgentConfig{cfg.Agents.Planner, cfg.Agents.Implementer}
	agents = append(agents, cfg.Agents.Reviewers...)
	agents = append(agents, cfg.Agents.Learner)
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
	if role == "learn" {
		return normalizeAgent(cfg.Agents.Learner), nil
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
	if state.LearnEnabled {
		learner := agentConfigByName(cfg.AllAgents(), state.LearnerName, cfg.Agents.Learner)
		files[filepath.Join(state.RunDir, "learn.prompt.md")] = learnPrompt(state, learner)
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

	fmt.Fprint(w, mascotColored())
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

	// While the planner is still open, remind the human how to release the
	// implementer from any pane without quitting the interactive planner.
	if view.Phase == "planner" {
		fmt.Fprintf(w, "\n%s\n", col("33", "Awaiting approval - release the implementer from any pane:"))
		fmt.Fprintf(w, "  sidekick release --run-dir %s\n", view.State.RunDir)
		fmt.Fprintf(w, "  sidekick abort   --run-dir %s\n", view.State.RunDir)
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
	if state.LearnEnabled {
		log := learnLog(state)
		prerequisite := state.ImplementDone
		if state.GateEnabled {
			prerequisite = gateDone(state)
		}
		view.Steps = append(view.Steps, PipelineStep{Name: "learn", Status: gatedStepStatus(prerequisite, state.LearnDone, log), Log: log})
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

func learnLog(state RunState) string {
	return filepath.Join(state.RunDir, "learn.log")
}

func markFile(path string) error {
	return os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
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
		root, err := repoRoot(*repo)
		if err != nil {
			return err
		}
		dir, err = findWaitingRun(root)
		if err != nil {
			return err
		}
	}

	done := filepath.Join(dir, "planner.done")
	if fileExists(done) {
		return fmt.Errorf("already released: %s", done)
	}
	if fileExists(done + ".failed") {
		return fmt.Errorf("already aborted: %s", done+".failed")
	}
	if fail {
		if err := markFile(done + ".failed"); err != nil {
			return err
		}
		fmt.Printf("aborted %s\n", filepath.Base(dir))
		return nil
	}
	if err := markFile(done); err != nil {
		return err
	}
	fmt.Printf("released %s; implementer starts within ~2s\n", filepath.Base(dir))
	return nil
}

// findWaitingRun returns the newest run dir under root that has a plan but no
// planner.done/.failed yet (i.e. awaiting human approval). Run IDs are
// timestamp-prefixed, so the lexically largest name is the newest.
func findWaitingRun(root string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, runRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("no runs found")
		}
		return "", err
	}
	best := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, runRoot, e.Name())
		done := filepath.Join(dir, "planner.done")
		if fileExists(done) || fileExists(done+".failed") {
			continue
		}
		if !fileExists(filepath.Join(dir, "state.json")) {
			continue
		}
		if best == "" || e.Name() > filepath.Base(best) {
			best = dir
		}
	}
	if best == "" {
		return "", errors.New("no run is awaiting approval")
	}
	return best, nil
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
	return `      .        *          Sidekick
   *    \   .    /   *
      \  \  |  /  /       always-on companion
  - -- >   \|/   < -- -
      /  /  |  \  \
   *    /   '   \    *`
}

// fg wraps s in a 24-bit truecolor ANSI escape, gated by the same rule as
// col: no-op when NO_COLOR is set or stdout is not a TTY.
//
// ponytail: assumes the terminal supports truecolor; the honest upgrade path
// is to gate on COLORTERM=truecolor/24bit and fall back to the existing
// 256-color col() when it isn't set. Not built - most terminals people use
// today (including tmux with default-terminal set right) support it, and a
// wrong guess just means a slightly off gradient, not broken output.
func fg(r, g, b int, s string) string {
	if os.Getenv("NO_COLOR") != "" || !stdoutIsTTY() {
		return s
	}
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", r, g, b, s)
}

// lerp linearly interpolates between from and to at t in [0,1].
func lerp(from, to int, t float64) int {
	return from + int(float64(to-from)*t)
}

// mascotColored renders the sparkle mascot with an orange->pink gradient
// applied per line.
func mascotColored() string {
	from := [3]int{255, 140, 0}
	to := [3]int{255, 105, 180}
	lines := strings.Split(mascot(), "\n")
	last := len(lines) - 1
	for i, line := range lines {
		t := 0.0
		if last > 0 {
			t = float64(i) / float64(last)
		}
		r := lerp(from[0], to[0], t)
		g := lerp(from[1], to[1], t)
		b := lerp(from[2], to[2], t)
		lines[i] = fg(r, g, b, line)
	}
	return strings.Join(lines, "\n")
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

// addRunWindows adds one run's windows (planner, dashboard, implement, review,
// optional gate, optional learn, optional land) to an existing tmux session, with every window
// name prefixed so multiple runs can coexist in the same session. It expects
// the session (or an empty placeholder window) to already exist and creates
// its own prefix+"planner" window rather than reusing whatever window is
// current, so it is safe to call into a session that already hosts other
// runs. It does not create the session and does not select a window -
// callers that own the session decide that.
func addRunWindows(session, prefix string, cfg Config, state RunState, gate, learn, land bool) error {
	plannerWin := prefix + "planner"
	if err := exec.Command("tmux", "new-window", "-t", session, "-n", plannerWin, "-c", state.RepoRoot).Run(); err != nil {
		return fmt.Errorf("create planner window: %w", err)
	}
	plannerPane, err := tmuxPaneID(session + ":" + plannerWin)
	if err != nil {
		return err
	}
	plannerCmd := shellJoin(self(), "agent", "--repo", state.RepoRoot, "--run-dir", state.RunDir, "--role", "planner", "--prompt", filepath.Join(state.RunDir, "planner.prompt.md"), "--output", state.PlanFile, "--done", state.PlannerDone)
	if err := tmuxSend(plannerPane, plannerCmd); err != nil {
		return err
	}

	dashboardWin := prefix + "dashboard"
	if err := exec.Command("tmux", "new-window", "-t", session, "-n", dashboardWin, "-c", state.WorktreePath).Run(); err != nil {
		return err
	}
	dashboardPane, err := tmuxPaneID(session + ":" + dashboardWin)
	if err != nil {
		return err
	}
	dashboardCmd := shellJoin(self(), "status", "--run-dir", state.RunDir, "--watch")
	if err := tmuxSend(dashboardPane, dashboardCmd); err != nil {
		return err
	}

	implementWin := prefix + "implement"
	if err := exec.Command("tmux", "new-window", "-t", session, "-n", implementWin, "-c", state.WorktreePath).Run(); err != nil {
		return err
	}
	implementPane, err := tmuxPaneID(session + ":" + implementWin)
	if err != nil {
		return err
	}
	implementCmd := shellJoin(self(), "wait-file", state.PlannerDone) + " && " + shellJoin(self(), "agent", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--role", "implementer", "--prompt", filepath.Join(state.RunDir, "implementer.prompt.md"), "--output", filepath.Join(state.RunDir, "implementer.log"), "--done", state.ImplementDone)
	if err := tmuxSend(implementPane, implementCmd); err != nil {
		return err
	}

	reviewWin := prefix + "review"
	if err := exec.Command("tmux", "new-window", "-t", session, "-n", reviewWin, "-c", state.WorktreePath).Run(); err != nil {
		return err
	}
	reviewPane, err := tmuxPaneID(session + ":" + reviewWin)
	if err != nil {
		return err
	}
	for i, reviewer := range cfg.Agents.Reviewers {
		if i > 0 {
			reviewPane, err = tmuxSplitPane(session+":"+reviewWin, state.WorktreePath)
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
		_ = exec.Command("tmux", "select-layout", "-t", session+":"+reviewWin, "tiled").Run()
	}

	if gate {
		gateWin := prefix + "gate"
		if err := exec.Command("tmux", "new-window", "-t", session, "-n", gateWin, "-c", state.WorktreePath).Run(); err != nil {
			return err
		}
		gatePane, err := tmuxPaneID(session + ":" + gateWin)
		if err != nil {
			return err
		}
		gateCmd := shellJoin(self(), "wait-file", state.ImplementDone) + " && " + shellJoin(self(), "gate", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--output", gateLog(state), "--done", gateDone(state))
		if err := tmuxSend(gatePane, gateCmd); err != nil {
			return err
		}
	}

	if learn {
		learnWin := prefix + "learn"
		if err := exec.Command("tmux", "new-window", "-t", session, "-n", learnWin, "-c", state.RepoRoot).Run(); err != nil {
			return err
		}
		learnPane, err := tmuxPaneID(session + ":" + learnWin)
		if err != nil {
			return err
		}
		learnCmd := shellJoin(self(), "wait-file", state.ImplementDone)
		if gate {
			learnCmd += " && " + shellJoin(self(), "wait-file", gateDone(state))
		}
		learnCmd += " && " + shellJoin(self(), "agent", "--repo", state.RepoRoot, "--run-dir", state.RunDir, "--role", "learn", "--prompt", filepath.Join(state.RunDir, "learn.prompt.md"), "--output", learnLog(state), "--done", state.LearnDone)
		if err := tmuxSend(learnPane, learnCmd); err != nil {
			return err
		}
	}

	if land {
		landWin := prefix + "land"
		if err := exec.Command("tmux", "new-window", "-t", session, "-n", landWin, "-c", state.WorktreePath).Run(); err != nil {
			return err
		}
		landPane, err := tmuxPaneID(session + ":" + landWin)
		if err != nil {
			return err
		}
		// wait for implementation, the gate, and repo learning before landing
		landCmd := shellJoin(self(), "wait-file", state.ImplementDone)
		if gate {
			landCmd += " && " + shellJoin(self(), "wait-file", gateDone(state))
		}
		if learn {
			landCmd += " && " + shellJoin(self(), "wait-file", state.LearnDone)
		}
		landCmd += " && " + shellJoin(self(), "land", "--repo", state.WorktreePath, "--run-dir", state.RunDir)
		if err := tmuxSend(landPane, landCmd); err != nil {
			return err
		}
	}

	return nil
}

// newBootstrapSession creates a new detached tmux session with a throwaway
// window (tmux requires -n on new-session) in root's directory, returning the
// bootstrap window's name so the caller can kill it once real windows exist.
func newBootstrapSession(session, root string) (string, error) {
	bootstrap := "bootstrap"
	if err := exec.Command("tmux", "new-session", "-d", "-s", session, "-n", bootstrap, "-c", root).Run(); err != nil {
		return "", fmt.Errorf("create tmux session: %w", err)
	}
	return bootstrap, nil
}

// spawnRun leases a worktree, writes run files and state, and adds the run's
// windows to an existing tmux session under the given window-name prefix. It
// does not create the session and does not attach; the caller decides that.
func spawnRun(session, prefix, root string, cfg Config, task string, gate, learn, land bool) (RunState, error) {
	runID, runDir, err := uniqueRunDir(root, task)
	if err != nil {
		return RunState{}, err
	}
	memoryFile := filepath.Join(root, ".sidekick", "memory.md")
	if err := os.MkdirAll(filepath.Dir(memoryFile), 0o755); err != nil {
		return RunState{}, err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return RunState{}, err
	}

	worktree, backend, err := leaseWorktree(root, runID)
	if err != nil {
		return RunState{}, err
	}

	plannerAgent := normalizeAgent(cfg.Agents.Planner)
	implementerAgent := normalizeAgent(cfg.Agents.Implementer)
	learnerAgent := normalizeAgent(cfg.Agents.Learner)

	state := RunState{
		ID:              runID,
		CreatedAt:       time.Now(),
		RepoRoot:        root,
		RunDir:          runDir,
		TaskFile:        filepath.Join(runDir, "task.md"),
		PlanFile:        filepath.Join(runDir, "plan.md"),
		MemoryFile:      memoryFile,
		PlannerDone:     filepath.Join(runDir, "planner.done"),
		ImplementDone:   filepath.Join(runDir, "implement.done"),
		LearnDone:       filepath.Join(runDir, "learn.done"),
		WorktreePath:    worktree,
		WorktreeBackend: backend,
		TmuxSession:     session,
		GateEnabled:     gate,
		LearnEnabled:    learn,
		PlannerName:     plannerAgent.Name,
		ImplementerName: implementerAgent.Name,
		LearnerName:     learnerAgent.Name,
	}
	for _, reviewer := range cfg.Agents.Reviewers {
		state.ReviewerNames = append(state.ReviewerNames, reviewer.Name)
	}

	if err := writeRunFiles(state, task, cfg); err != nil {
		return RunState{}, err
	}
	if err := writeState(state); err != nil {
		return RunState{}, err
	}
	if err := addRunWindows(session, prefix, cfg, state, gate, learn, land); err != nil {
		return RunState{}, err
	}
	return state, nil
}

// uniqueRunDir picks a run ID/dir for task under root, guarding against
// newRunID's 1-second granularity: if the directory already exists (two
// spawns within the same second), it suffixes -2, -3, ... until free.
func uniqueRunDir(root, task string) (string, string, error) {
	base := newRunID(task)
	id := base
	for i := 2; ; i++ {
		dir := filepath.Join(root, runRoot, id)
		if !fileExists(dir) {
			return id, dir, nil
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
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
	if role == "planner" || role == "learn" {
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
		case "SIDEKICK_MEMORY_FILE":
			return state.MemoryFile
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

Prior Sidekick runs recorded lessons for this repo at:
%s
If it exists, read it first for context and known pitfalls.

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

Do not edit any other files -- write only the plan file. Once the plan file is
saved, tell the human they can release the implementer WITHOUT quitting you:
run "sidekick release" (or "sidekick abort" to cancel) in any other pane. Quitting
you and answering the [y/N] prompt also works, but is not required.
`, state.ID, state.TaskFile, state.MemoryFile, state.PlanFile)
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

Prior Sidekick runs recorded lessons for this repo at:
%s
If it exists, read it first for context and known pitfalls.

Work in this isolated worktree:
%s

Execute the plan with the smallest correct change. Keep the worktree reviewable:
- Inspect the repository before editing.
- Reuse local patterns.
- Run relevant validation.
- Do not commit unless the task explicitly requires it.
- Write a concise outcome summary to stdout, including validation results.
`, state.ID, state.TaskFile, state.PlanFile, state.MemoryFile, state.WorktreePath)
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

Prior Sidekick runs recorded lessons for this repo at:
%s
If it exists, read it first for context and known pitfalls.

Use a code-review stance. Prioritize bugs, regressions, security issues, missing tests, and mismatches with the task or plan.

Required output:
- Findings first, with file and line references when possible.
- Then residual risks or test gaps.
- Then a brief conclusion.

Do not edit files during review.
`, agent.Name, state.ID, state.TaskFile, state.PlanFile, state.WorktreePath, state.MemoryFile)
}

func learnPrompt(state RunState, agent AgentConfig) string {
	if strings.TrimSpace(agent.Prompt) != "" {
		return expandPrompt(agent.Prompt, state)
	}
	return fmt.Sprintf(`# Sidekick repo learning task

You are the learning agent for Sidekick run %s.

Update this repo memory file only:
%s

Read these run inputs and outputs:
- Task file: %s
- Plan file: %s
- Implementer log: %s
- Worktree status: git -C %s status --short
- Worktree diff: git -C %s diff

Create the memory file if it does not exist. Keep it as Markdown with these sections:

## Repo insights

Durable, generally useful conventions, skills, pitfalls, commands, architecture notes, and validation facts for future Sidekick runs in this repo. Merge new insights into this section, edit existing bullets in place when needed, keep it concise, and avoid duplicates.

## Runs

Append a dated entry for this run. Include:
- Run id: %s
- Goal from the task
- Files touched, based on git status/diff
- Validation outcome from the implementer log or diff context
- Any follow-up risk worth remembering

Do not edit any file except the memory file. If there is no durable insight, still append the run entry and leave Repo insights concise.
`, state.ID, state.MemoryFile, state.TaskFile, state.PlanFile, implementerLog(state), state.WorktreePath, state.WorktreePath, state.ID)
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
