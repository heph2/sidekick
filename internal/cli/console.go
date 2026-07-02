package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"sidekick/internal/config"
	"sidekick/internal/run"
	"sidekick/internal/sh"
	"sidekick/internal/tmux"
	"sidekick/internal/ui"
)

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

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
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
	consoleCmd := sh.Join(consoleArgs...)
	if err := tmux.NewSession(session, "console", root); err != nil {
		return err
	}
	consolePane, err := tmux.PaneID(session + ":console")
	if err != nil {
		return err
	}
	if err := tmux.Send(consolePane, consoleCmd); err != nil {
		return err
	}
	dashboardCmd := sh.Join(self(), "status", "--all", "--watch", "--repo", root)
	if err := tmux.NewWindow(session, "dashboard", root); err != nil {
		return fmt.Errorf("create dashboard window: %w", err)
	}
	dashboardPane, err := tmux.PaneID(session + ":dashboard")
	if err != nil {
		return err
	}
	if err := tmux.Send(dashboardPane, dashboardCmd); err != nil {
		return err
	}
	_ = tmux.SelectWindow(session + ":console")

	fmt.Printf("session: %s\n", session)
	if *noAttach {
		fmt.Printf("attach: tmux attach -t %s\n", session)
		return nil
	}
	return tmux.Attach(session)
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

	root, err := run.RepoRoot(*repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
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

	session, err := tmux.CurrentSession()
	if err != nil {
		return err
	}

	land := !*noLand
	learn := !*noLearn
	in := bufio.NewReader(os.Stdin)
	runs := map[string]run.State{}
	for idx := 1; ; idx++ {
		fmt.Print(ui.MascotColored())
		fmt.Println()
		fmt.Println("Give Sidekick a task. Slash commands: /help, /list, /release, /abort, /ship, /attach, /status.")

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
		parsed := parseConsoleInput(taskText)
		if parsed.Kind == "exit" {
			return nil
		}
		if parsed.Kind == "command" {
			if err := handleConsoleCommand(parsed, root, session, runs); err != nil {
				fmt.Fprintf(os.Stderr, "sidekick: %v\n", err)
			}
			idx--
			continue
		}

		label := fmt.Sprintf("t%d", idx)
		state, err := spawnRun(session, label, root, cfg, parsed.Task, gateEnabled, learn, land, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sidekick: %v\n", err)
			idx--
			continue
		}
		runs[label] = state
		fmt.Printf("run %s (%s) started - planner in %s-planner; watch the dashboard\n\n", label, state.ID, label)
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

type consoleInput struct {
	Kind    string
	Command string
	Args    []string
	Task    string
}

func parseConsoleInput(line string) consoleInput {
	line = strings.TrimSpace(line)
	if line == "" {
		return consoleInput{}
	}
	lower := strings.ToLower(line)
	if lower == "quit" || lower == "exit" || lower == "/quit" || lower == "/exit" {
		return consoleInput{Kind: "exit"}
	}
	if strings.HasPrefix(line, "/") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return consoleInput{}
		}
		return consoleInput{
			Kind:    "command",
			Command: strings.ToLower(strings.TrimPrefix(fields[0], "/")),
			Args:    append([]string(nil), fields[1:]...),
		}
	}
	return consoleInput{Kind: "task", Task: line}
}

func handleConsoleCommand(input consoleInput, root, session string, runs map[string]run.State) error {
	switch input.Command {
	case "help":
		fmt.Println("commands: /release [tN], /abort [tN], /ship [tN], /list, /status [tN], /attach <tN> <stage>, /kill <tN>, /quit")
		return nil
	case "list":
		printConsoleRuns(runs)
		return nil
	case "release":
		if len(input.Args) > 1 {
			return errors.New("usage: /release [tN]")
		}
		if len(input.Args) == 0 {
			return signalPlanner([]string{"--repo", root}, false)
		}
		state, err := resolveConsoleRun(input.Args[0], runs)
		if err != nil {
			return err
		}
		return signalPlanner([]string{"--run-dir", state.RunDir}, false)
	case "abort":
		if len(input.Args) > 1 {
			return errors.New("usage: /abort [tN]")
		}
		if len(input.Args) == 0 {
			return signalPlanner([]string{"--repo", root}, true)
		}
		state, err := resolveConsoleRun(input.Args[0], runs)
		if err != nil {
			return err
		}
		return signalPlanner([]string{"--run-dir", state.RunDir}, true)
	case "ship":
		if len(input.Args) > 1 {
			return errors.New("usage: /ship [tN]")
		}
		if len(input.Args) == 0 {
			return shipRun([]string{"--repo", root})
		}
		state, err := resolveConsoleRun(input.Args[0], runs)
		if err != nil {
			return err
		}
		return shipRun([]string{"--run-dir", state.RunDir})
	case "status":
		if len(input.Args) > 1 {
			return errors.New("usage: /status [tN]")
		}
		if len(input.Args) == 0 {
			return ui.RenderAllStatus(os.Stdout, root, ui.TerminalWidth())
		}
		state, err := resolveConsoleRun(input.Args[0], runs)
		if err != nil {
			return err
		}
		return ui.RenderStatus(os.Stdout, state.RunDir, ui.TerminalWidth(), 0)
	case "attach":
		if len(input.Args) != 2 {
			return errors.New("usage: /attach <tN> <stage>")
		}
		state, err := resolveConsoleRun(input.Args[0], runs)
		if err != nil {
			return err
		}
		return attachConsoleLog(session, state, input.Args[1])
	case "kill":
		if len(input.Args) != 1 {
			return errors.New("usage: /kill <tN>")
		}
		state, err := resolveConsoleRun(input.Args[0], runs)
		if err != nil {
			return err
		}
		if !run.Exists(state.PlannerDone) && !run.Exists(state.PlannerDone+".failed") {
			if err := run.Mark(state.PlannerDone + ".failed"); err != nil {
				return err
			}
			fmt.Printf("aborted %s; already-running headless stages are not killed\n", state.ConsoleLabel)
			return nil
		}
		fmt.Printf("%s is already past planning; already-running headless stages are not killed\n", state.ConsoleLabel)
		return nil
	default:
		return fmt.Errorf("unknown command /%s; try /help", input.Command)
	}
}

func printConsoleRuns(runs map[string]run.State) {
	if len(runs) == 0 {
		fmt.Println("no runs started in this console")
		return
	}
	labels := sortedRunLabels(runs)
	for _, label := range labels {
		state := runs[label]
		phase := "unknown"
		if view, err := ui.BuildStatusView(state.RunDir); err == nil {
			phase = view.Phase
		}
		fmt.Printf("%s -> %s  %s  %s\n", label, state.ID, phase, run.FirstLine(state.TaskFile))
	}
}

func sortedRunLabels(runs map[string]run.State) []string {
	labels := make([]string, 0, len(runs))
	for label := range runs {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		return labelNumber(labels[i]) < labelNumber(labels[j])
	})
	return labels
}

func labelNumber(label string) int {
	var n int
	if _, err := fmt.Sscanf(label, "t%d", &n); err == nil {
		return n
	}
	return 0
}

func resolveConsoleRun(target string, runs map[string]run.State) (run.State, error) {
	if state, ok := runs[target]; ok {
		return state, nil
	}
	for _, state := range runs {
		if state.ID == target || strings.HasPrefix(state.ID, target) {
			return state, nil
		}
	}
	return run.State{}, fmt.Errorf("unknown run %q", target)
}

func attachConsoleLog(session string, state run.State, stage string) error {
	logPath, err := logForStage(state, stage)
	if err != nil {
		return err
	}
	name := stageWindowName(state.ConsoleLabel, stage)
	cmd := "touch " + sh.Quote(logPath) + " && tail -f " + sh.Quote(logPath)
	return tmux.NewWindowCommand(session, name, state.RunDir, sh.Join("sh", "-c", cmd))
}

func logForStage(state run.State, stage string) (string, error) {
	stage = strings.TrimSpace(strings.ToLower(stage))
	switch stage {
	case "cycle", "implement", "implementer":
		return state.ImplementerLog(), nil
	case "gate":
		return state.GateLog(), nil
	case "learn", "learner":
		return state.LearnLog(), nil
	case "land", "ship":
		return state.LandLog(), nil
	}
	for _, reviewer := range state.ReviewerNames {
		if stage == "review-"+run.Slug(reviewer) || stage == run.Slug(reviewer) || stage == "reviewer-"+run.Slug(reviewer) {
			return state.ReviewerLog(reviewer), nil
		}
	}
	return "", fmt.Errorf("unknown stage %q", stage)
}
