package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sidekick/internal/agent"
	"sidekick/internal/config"
	"sidekick/internal/run"
	"sidekick/internal/sh"
	"sidekick/internal/tmux"
	"sidekick/internal/worktree"
)

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

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}

	plannerAgent, err := config.SelectAgent(cfg.AllAgents(), cfg.Agents.Planner.Name, *planner)
	if err != nil {
		return fmt.Errorf("planner: %w", err)
	}
	implementerAgent, err := config.SelectAgent(cfg.AllAgents(), cfg.Agents.Implementer.Name, *implementer)
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

	runID := run.NewID(taskText)
	session := "sidekick-" + runID
	if err := tmux.NewSession(session, "bootstrap", root); err != nil {
		return err
	}

	state, err := spawnRun(session, "", root, cfg, taskText, gateEnabled, !*noLearn, !*noLand, false)
	if err != nil {
		return err
	}
	tmux.KillWindow(session + ":bootstrap")
	if err := tmux.SelectWindow(session + ":dashboard"); err != nil {
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
	return tmux.Attach(state.TmuxSession)
}

// validateAgents checks that tmux, the gate command (when enabled), and every
// configured agent needed by this run are runnable before any run is spawned.
func validateAgents(cfg config.Config, gateEnabled, learnEnabled bool) error {
	if err := config.RequireBinary("tmux"); err != nil {
		return err
	}
	if gateEnabled {
		if err := config.RequireCommand(cfg.Gate.Command); err != nil {
			return fmt.Errorf("gate: %w", err)
		}
	}
	agents := []config.AgentConfig{cfg.Agents.Planner, cfg.Agents.Implementer}
	agents = append(agents, cfg.Agents.Reviewers...)
	if learnEnabled {
		agents = append(agents, cfg.Agents.Learner)
	}
	for _, a := range agents {
		for _, candidate := range agentWithFallbacks(a) {
			if err := config.RequireAgent(candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentWithFallbacks(a config.AgentConfig) []config.AgentConfig {
	agents := []config.AgentConfig{a}
	for _, fallback := range a.Fallbacks {
		agents = append(agents, agentWithFallbacks(fallback)...)
	}
	return agents
}

// readTask sources the task when --task is absent: an interactive prompt on a
// TTY, otherwise all of piped stdin.
func readTask() (string, error) {
	// ponytail: ModeCharDevice tty check, no x/term dep
	if stdinIsTTY() {
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

// spawnRun leases a worktree, writes run files and state, and adds the run's
// windows to an existing tmux session under the given window-name prefix. It
// does not create the session and does not attach; the caller decides that.
func spawnRun(session, label, root string, cfg config.Config, task string, gate, learn, land, headless bool) (run.State, error) {
	runID, runDir, err := run.UniqueDir(root, task)
	if err != nil {
		return run.State{}, err
	}
	memoryFile := filepath.Join(root, ".sidekick", "memory.md")
	if err := os.MkdirAll(filepath.Dir(memoryFile), 0o755); err != nil {
		return run.State{}, err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return run.State{}, err
	}

	wt, backend, err := worktree.Lease(root, runID)
	if err != nil {
		return run.State{}, err
	}

	plannerAgent := config.Normalize(cfg.Agents.Planner)
	implementerAgent := config.Normalize(cfg.Agents.Implementer)
	learnerAgent := config.Normalize(cfg.Agents.Learner)

	state := run.State{
		ID:              runID,
		ConsoleLabel:    label,
		CreatedAt:       time.Now(),
		RepoRoot:        root,
		RunDir:          runDir,
		TaskFile:        filepath.Join(runDir, "task.md"),
		PlanFile:        filepath.Join(runDir, "plan.md"),
		MemoryFile:      memoryFile,
		PlannerDone:     filepath.Join(runDir, "planner.done"),
		ImplementDone:   filepath.Join(runDir, "implement.done"),
		LearnDone:       filepath.Join(runDir, "learn.done"),
		WorktreePath:    wt,
		WorktreeBackend: backend,
		TmuxSession:     session,
		GateEnabled:     gate,
		LearnEnabled:    learn,
		LandEnabled:     land,
		PlannerName:     plannerAgent.Name,
		ImplementerName: implementerAgent.Name,
		LearnerName:     learnerAgent.Name,
	}
	for _, reviewer := range cfg.Agents.Reviewers {
		state.ReviewerNames = append(state.ReviewerNames, reviewer.Name)
	}

	if err := writeRunFiles(state, task, cfg); err != nil {
		return run.State{}, err
	}
	if err := state.Save(); err != nil {
		return run.State{}, err
	}
	stages := stageCommands(state, gate, learn, land)
	if headless {
		if err := launchHeadless(session, stages, state); err != nil {
			return run.State{}, err
		}
	} else {
		if err := addRunWindows(session, label, stages, state); err != nil {
			return run.State{}, err
		}
	}
	return state, nil
}

func writeRunFiles(state run.State, task string, cfg config.Config) error {
	planner := config.ByName(cfg.AllAgents(), state.PlannerName, cfg.Agents.Planner)
	implementer := config.ByName(cfg.AllAgents(), state.ImplementerName, cfg.Agents.Implementer)
	files := map[string]string{
		state.TaskFile: task + "\n",
		filepath.Join(state.RunDir, "planner.prompt.md"):     agent.PlannerPrompt(state, planner),
		filepath.Join(state.RunDir, "implementer.prompt.md"): agent.ImplementerPrompt(state, implementer),
	}
	if state.LearnEnabled {
		learner := config.ByName(cfg.AllAgents(), state.LearnerName, cfg.Agents.Learner)
		files[filepath.Join(state.RunDir, "learn.prompt.md")] = agent.LearnPrompt(state, learner)
	}
	for _, reviewer := range state.ReviewerNames {
		a := config.ByName(cfg.Agents.Reviewers, reviewer, config.AgentConfig{Name: reviewer})
		files[filepath.Join(state.RunDir, "reviewer-"+run.Slug(reviewer)+".prompt.md")] = agent.ReviewerPrompt(state, a)
	}
	if state.GateEnabled {
		files[filepath.Join(state.RunDir, "gate.prompt.md")] = agent.GatePrompt(state)
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type runStage struct {
	Name        string
	Dir         string
	Cmd         string
	Log         string
	Interactive bool
}

func stageCommands(state run.State, gate, learn, land bool) []runStage {
	stages := []runStage{
		{
			Name:        "planner",
			Dir:         state.RepoRoot,
			Cmd:         sh.Join(self(), "agent", "--repo", state.RepoRoot, "--run-dir", state.RunDir, "--role", "planner", "--prompt", filepath.Join(state.RunDir, "planner.prompt.md"), "--output", state.PlanFile, "--done", state.PlannerDone),
			Log:         state.PlanFile,
			Interactive: true,
		},
		{
			// The cycle stage owns the implement -> review -> fix loop and writes
			// implement.done (or .failed) when it converges, so gate/learn/land
			// still key off the same marker.
			Name: "cycle",
			Dir:  state.WorktreePath,
			Cmd:  sh.Join(self(), "wait-file", state.PlannerDone) + " && " + sh.Join(self(), "cycle", "--repo", state.RepoRoot, "--run-dir", state.RunDir),
			Log:  state.ImplementerLog(),
		},
	}
	if gate {
		stages = append(stages, runStage{
			Name: "gate",
			Dir:  state.WorktreePath,
			Cmd:  sh.Join(self(), "wait-file", state.ImplementDone) + " && " + sh.Join(self(), "gate", "--repo", state.WorktreePath, "--run-dir", state.RunDir, "--output", state.GateLog(), "--done", state.GateDone()),
			Log:  state.GateLog(),
		})
	}
	if learn {
		cmd := sh.Join(self(), "wait-file", state.ImplementDone)
		if gate {
			cmd += " && " + sh.Join(self(), "wait-file", state.GateDone())
		}
		cmd += " && " + sh.Join(self(), "agent", "--repo", state.RepoRoot, "--run-dir", state.RunDir, "--role", "learn", "--prompt", filepath.Join(state.RunDir, "learn.prompt.md"), "--output", state.LearnLog(), "--done", state.LearnDone)
		stages = append(stages, runStage{Name: "learn", Dir: state.RepoRoot, Cmd: cmd, Log: state.LearnLog()})
	}
	if land {
		cmd := sh.Join(self(), "wait-file", state.ImplementDone)
		if gate {
			cmd += " && " + sh.Join(self(), "wait-file", state.GateDone())
		}
		if learn {
			cmd += " && " + sh.Join(self(), "wait-file", state.LearnDone)
		}
		cmd += " && " + sh.Join(self(), "land", "--repo", state.WorktreePath, "--run-dir", state.RunDir)
		stages = append(stages, runStage{Name: "land", Dir: state.WorktreePath, Cmd: cmd, Log: state.LandLog()})
	}
	return stages
}

// addRunWindows adds one run's windows to an existing tmux session. It is used
// by the scriptable `sidekick run` path; console runs use launchHeadless.
func addRunWindows(session, label string, stages []runStage, state run.State) error {
	for i, stage := range stages {
		if i == 1 {
			dashboardWin := stageWindowName(label, "dashboard")
			if err := tmux.NewWindow(session, dashboardWin, state.WorktreePath); err != nil {
				return err
			}
			dashboardPane, err := tmux.PaneID(session + ":" + dashboardWin)
			if err != nil {
				return err
			}
			if err := tmux.Send(dashboardPane, sh.Join(self(), "status", "--run-dir", state.RunDir, "--watch")); err != nil {
				return err
			}
		}
		win := stageWindowName(label, stage.Name)
		if err := tmux.NewWindow(session, win, stage.Dir); err != nil {
			return fmt.Errorf("create %s window: %w", stage.Name, err)
		}
		pane, err := tmux.PaneID(session + ":" + win)
		if err != nil {
			return err
		}
		if err := tmux.Send(pane, stage.Cmd); err != nil {
			return err
		}
	}
	return nil
}

func launchHeadless(session string, stages []runStage, state run.State) error {
	for _, stage := range stages {
		if stage.Interactive {
			win := stageWindowName(state.ConsoleLabel, stage.Name)
			if err := tmux.NewWindow(session, win, stage.Dir); err != nil {
				return fmt.Errorf("create %s window: %w", stage.Name, err)
			}
			pane, err := tmux.PaneID(session + ":" + win)
			if err != nil {
				return err
			}
			if err := tmux.Send(pane, stage.Cmd); err != nil {
				return err
			}
			continue
		}
		if err := launchDetached(stage); err != nil {
			return err
		}
	}
	return nil
}

func launchDetached(stage runStage) error {
	cmdText := stage.Cmd
	if stage.Name == "land" && stage.Log != "" {
		cmdText += " > " + sh.Quote(stage.Log) + " 2>&1"
	}
	args := []string{"sh", "-c", cmdText}
	if _, err := exec.LookPath("setsid"); err == nil {
		args = append([]string{"setsid"}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = stage.Dir
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	cmd.Stdin = devNull
	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		return err
	}
	_ = devNull.Close()
	return nil
}

func stageWindowName(label, name string) string {
	if label == "" {
		return name
	}
	return label + "-" + name
}
