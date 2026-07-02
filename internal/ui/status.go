package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sidekick/internal/run"
)

type PipelineStep struct {
	Name   string
	Status string
	Log    string
}

type StatusView struct {
	State       run.State
	Goal        string
	Phase       string
	Cycle       string
	Elapsed     time.Duration
	Steps       []PipelineStep
	RecentTitle string
	RecentLines []string
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func RenderStatus(w io.Writer, runDir string, width int, frame int) error {
	view, err := BuildStatusView(runDir)
	if err != nil {
		return err
	}
	if width < 60 {
		width = 60
	}
	if width > 120 {
		width = 120
	}

	fmt.Fprintf(w, "%s %s  %s  %s\n", spinnerGlyph(view.Phase, frame), col("1", "Sidekick"), col(phaseColor(view.Phase), view.Phase), view.Elapsed.Round(time.Second))
	fmt.Fprintf(w, "%s\n", strings.Repeat("=", width))
	fmt.Fprintf(w, "Run:  %s\n", view.State.ID)
	if view.Cycle != "" {
		fmt.Fprintf(w, "Cycle: %s\n", view.Cycle)
	}
	fmt.Fprintf(w, "Goal: %s\n", col("1", Clip(view.Goal, width-6)))
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
	fmt.Fprintf(w, "  worktree: %s\n", Clip(view.State.WorktreePath, width-12))
	fmt.Fprintf(w, "  plan:     %s\n", Clip(view.State.PlanFile, width-12))
	fmt.Fprintf(w, "  logs:     %s\n", Clip(view.State.RunDir, width-12))
	fmt.Fprintf(w, "  attach:   tmux attach -t %s\n", view.State.TmuxSession)

	fmt.Fprintf(w, "\nRecent: %s\n", view.RecentTitle)
	if len(view.RecentLines) == 0 {
		fmt.Fprintln(w, "  waiting for agent output...")
		return nil
	}
	for _, line := range view.RecentLines {
		fmt.Fprintf(w, "  %s\n", Clip(line, width-4))
	}
	return nil
}

const allStatusLimit = 8

func RenderAllStatus(w io.Writer, root string, width int) error {
	views, err := allStatusViews(root)
	if err != nil {
		return err
	}
	if width < 60 {
		width = 60
	}
	if width > 120 {
		width = 120
	}

	fmt.Fprint(w, MascotColored())
	fmt.Fprintf(w, "\n%s\n", strings.Repeat("=", width))
	fmt.Fprintf(w, "Sidekick runs: %s\n", root)
	fmt.Fprintf(w, "%s\n\n", strings.Repeat("=", width))
	if len(views) == 0 {
		fmt.Fprintln(w, "No runs yet.")
		return nil
	}

	shown := len(views)
	if shown > allStatusLimit {
		shown = allStatusLimit
	}
	for i := 0; i < shown; i++ {
		view := views[i]
		label := view.State.ConsoleLabel
		if label == "" {
			label = Clip(view.State.ID, 14)
		}
		fmt.Fprintf(w, "%-14s %-18s %s\n", label, col(phaseColor(view.Phase), view.Phase), Clip(view.Goal, width-36))
		fmt.Fprintf(w, "  %s\n", compactPipeline(view.Steps, width-4))
		recent := "waiting for agent output..."
		if len(view.RecentLines) > 0 {
			recent = view.RecentTitle + ": " + view.RecentLines[len(view.RecentLines)-1]
		}
		fmt.Fprintf(w, "  %s\n\n", Clip(recent, width-4))
	}
	// ponytail: keep the shared dashboard compact; older runs remain on disk.
	if hidden := len(views) - shown; hidden > 0 {
		fmt.Fprintf(w, "+%d more\n", hidden)
	}
	return nil
}

func allStatusViews(root string) ([]StatusView, error) {
	runsDir := filepath.Join(root, run.Root)
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var views []StatusView
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		view, err := BuildStatusView(filepath.Join(runsDir, entry.Name()))
		if err != nil {
			continue
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		return views[i].State.CreatedAt.After(views[j].State.CreatedAt)
	})
	return views, nil
}

func compactPipeline(steps []PipelineStep, width int) string {
	var parts []string
	for _, step := range steps {
		parts = append(parts, col(statusColor(step.Status), "["+statusMark(step.Status)+"]")+" "+step.Name)
	}
	return Clip(strings.Join(parts, "  "), width)
}

func BuildStatusView(runDir string) (StatusView, error) {
	state, err := run.Load(runDir)
	if err != nil {
		return StatusView{}, err
	}
	view := StatusView{
		State:   state,
		Goal:    run.FirstLine(state.TaskFile),
		Cycle:   run.ReadLine(filepath.Join(state.RunDir, "cycle.status")),
		Elapsed: time.Since(state.CreatedAt),
	}
	view.Steps = append(view.Steps, PipelineStep{Name: "planner", Status: stepStatus(state.PlannerDone, state.PlanFile), Log: state.PlanFile})
	view.Steps = append(view.Steps, PipelineStep{Name: "implementer", Status: stepStatus(state.ImplementDone, state.ImplementerLog()), Log: state.ImplementerLog()})
	// The cycle stage runs implement and review together, so reviewer logs
	// appear before implement.done exists. Gate the review rows on planner.done
	// (the cycle's real prerequisite) rather than implement.done.
	for _, reviewer := range state.ReviewerNames {
		log := state.ReviewerLog(reviewer)
		done := state.ReviewerDone(reviewer)
		view.Steps = append(view.Steps, PipelineStep{Name: "review " + reviewer, Status: gatedStepStatus(state.PlannerDone, done, log), Log: log})
	}
	if state.GateEnabled {
		log := state.GateLog()
		view.Steps = append(view.Steps, PipelineStep{Name: "gate", Status: gatedStepStatus(state.ImplementDone, state.GateDone(), log), Log: log})
	}
	if state.LearnEnabled {
		log := state.LearnLog()
		prerequisite := state.ImplementDone
		if state.GateEnabled {
			prerequisite = state.GateDone()
		}
		view.Steps = append(view.Steps, PipelineStep{Name: "learn", Status: gatedStepStatus(prerequisite, state.LearnDone, log), Log: log})
	}
	if state.LandEnabled {
		prerequisite := state.ImplementDone
		if state.GateEnabled {
			prerequisite = state.GateDone()
		}
		if state.LearnEnabled {
			prerequisite = state.LearnDone
		}
		view.Steps = append(view.Steps, PipelineStep{Name: "land", Status: landStepStatus(prerequisite, state), Log: state.LandLog()})
	}
	view.Phase = currentPhase(view.Steps)
	view.RecentTitle, view.RecentLines = recentOutput(view.Steps)
	return view, nil
}

func stepStatus(donePath, logPath string) string {
	if run.Exists(donePath + ".failed") {
		return "failed"
	}
	if run.Exists(donePath) {
		return "done"
	}
	if run.Exists(logPath) {
		return "running"
	}
	return "waiting"
}

func gatedStepStatus(prerequisiteDonePath, donePath, logPath string) string {
	if !run.Exists(prerequisiteDonePath) {
		return "waiting"
	}
	return stepStatus(donePath, logPath)
}

func landStepStatus(prerequisiteDonePath string, state run.State) string {
	if !run.Exists(prerequisiteDonePath) {
		return "waiting"
	}
	done := state.LandDone()
	if run.Exists(done + ".failed") {
		return "failed"
	}
	if run.Exists(done) {
		return "done"
	}
	if run.Exists(state.LandReady()) || run.Exists(state.LandLog()) {
		return "running"
	}
	return "waiting"
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
		lines := run.LastLines(step.Log, 8)
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

func spinnerGlyph(phase string, frame int) string {
	switch {
	case phase == "complete":
		return fg(80, 200, 120, "✓")
	case strings.HasPrefix(phase, "failed"):
		return fg(255, 95, 95, "✗")
	default:
		i := frame % len(spinnerFrames)
		if i < 0 {
			i = 0
		}
		t := float64(i) / float64(len(spinnerFrames)-1)
		r := lerp(255, 255, t)
		g := lerp(140, 105, t)
		b := lerp(0, 180, t)
		return fg(r, g, b, spinnerFrames[i])
	}
}
