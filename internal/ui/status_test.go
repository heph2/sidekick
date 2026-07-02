package ui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sidekick/internal/run"
	"sidekick/internal/testutil"
)

func TestBuildStatusViewWaiting(t *testing.T) {
	state := testutil.RunState(t, false)
	view, err := BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if view.Goal != "Ship the dashboard" {
		t.Fatalf("goal = %q", view.Goal)
	}
	if view.Phase != "planner" {
		t.Fatalf("phase = %q, want planner", view.Phase)
	}
	wantStatuses := []string{"waiting", "waiting", "waiting"}
	if got := stepStatuses(view.Steps); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("statuses = %#v, want %#v", got, wantStatuses)
	}
}

func TestBuildStatusViewRunningAndComplete(t *testing.T) {
	state := testutil.RunState(t, true)
	testutil.MustWrite(t, state.PlanFile, "Plan\n")
	testutil.MustWrite(t, state.PlannerDone, "done\n")
	testutil.MustWrite(t, filepath.Join(state.RunDir, "cycle.status"), "cycle 1/3: implementing\n")
	testutil.MustWrite(t, state.ImplementerLog(), "working\n")

	view, err := BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if view.Cycle != "cycle 1/3: implementing" {
		t.Fatalf("cycle = %q, want cycle status", view.Cycle)
	}
	if view.Phase != "implementer" {
		t.Fatalf("phase = %q, want implementer", view.Phase)
	}
	wantStatuses := []string{"done", "running", "waiting", "waiting"}
	if got := stepStatuses(view.Steps); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("statuses = %#v, want %#v", got, wantStatuses)
	}
	if view.RecentTitle != "implementer" || !reflect.DeepEqual(view.RecentLines, []string{"working"}) {
		t.Fatalf("recent = %q %#v", view.RecentTitle, view.RecentLines)
	}

	testutil.MustWrite(t, state.ImplementDone, "done\n")
	testutil.MustWrite(t, state.ReviewerLog("codex-reviewer"), "reviewing\n")
	view, err = BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	wantStatuses = []string{"done", "done", "running", "waiting"}
	if got := stepStatuses(view.Steps); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("statuses = %#v, want %#v", got, wantStatuses)
	}

	testutil.MustWrite(t, state.ReviewerDone("codex-reviewer"), "done\n")
	testutil.MustWrite(t, state.GateLog(), "gate passed\n")
	testutil.MustWrite(t, state.GateDone(), "done\n")
	view, err = BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if view.Phase != "complete" {
		t.Fatalf("phase = %q, want complete", view.Phase)
	}
	wantStatuses = []string{"done", "done", "done", "done"}
	if got := stepStatuses(view.Steps); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("statuses = %#v, want %#v", got, wantStatuses)
	}
}

func TestBuildStatusViewReviewerCanRunBeforeImplementDone(t *testing.T) {
	state := testutil.RunState(t, false)
	testutil.MustWrite(t, state.PlanFile, "Plan\n")
	testutil.MustWrite(t, state.PlannerDone, "done\n")
	testutil.MustWrite(t, state.ReviewerLog("codex-reviewer"), "reviewing\n")

	view, err := BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "review codex-reviewer"); got != "running" {
		t.Fatalf("reviewer status = %q, want running", got)
	}
}

func TestBuildStatusViewLearnStep(t *testing.T) {
	state := testutil.RunState(t, false)
	state.ReviewerNames = nil
	state.LearnEnabled = true
	state.LearnDone = filepath.Join(state.RunDir, "learn.done")
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	view, err := BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "waiting" {
		t.Fatalf("learn status = %q, want waiting", got)
	}

	testutil.MustWrite(t, state.PlannerDone, "done\n")
	testutil.MustWrite(t, state.ImplementDone, "done\n")
	testutil.MustWrite(t, state.LearnLog(), "learning\n")
	view, err = BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "running" {
		t.Fatalf("learn status = %q, want running", got)
	}

	testutil.MustWrite(t, state.LearnDone, "done\n")
	view, err = BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "done" {
		t.Fatalf("learn status = %q, want done", got)
	}
}

func TestBuildStatusViewLearnWaitsForGate(t *testing.T) {
	state := testutil.RunState(t, true)
	state.ReviewerNames = nil
	state.LearnEnabled = true
	state.LearnDone = filepath.Join(state.RunDir, "learn.done")
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	testutil.MustWrite(t, state.PlannerDone, "done\n")
	testutil.MustWrite(t, state.ImplementDone, "done\n")
	testutil.MustWrite(t, state.LearnLog(), "learning\n")

	view, err := BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "waiting" {
		t.Fatalf("learn status before gate = %q, want waiting", got)
	}

	testutil.MustWrite(t, state.GateDone(), "done\n")
	view, err = BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "running" {
		t.Fatalf("learn status after gate = %q, want running", got)
	}
}

func TestBuildStatusViewOmitsLearnWhenDisabled(t *testing.T) {
	state := testutil.RunState(t, false)
	state.LearnEnabled = false
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	view, err := BuildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("BuildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "" {
		t.Fatalf("learn step present when disabled: %q", got)
	}
}

func TestRenderStatusIncludesHeaderAndArtifacts(t *testing.T) {
	state := testutil.RunState(t, false)
	var out bytes.Buffer
	if err := RenderStatus(&out, state.RunDir, 80, 0); err != nil {
		t.Fatalf("RenderStatus() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"Sidekick", "Ship the dashboard", "worktree:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered status missing %q:\n%s", want, text)
		}
	}
}

func TestRenderStatusNoColorWhenNotTTY(t *testing.T) {
	// go test stdout is not a TTY, so col() and the spinner's fg() must emit no
	// escape codes.
	state := testutil.RunState(t, false)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.TaskFile, []byte("do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderStatus(&buf, state.RunDir, 100, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\033[") {
		t.Fatalf("dashboard leaked ANSI escapes to non-tty output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Sidekick") {
		t.Fatalf("dashboard missing plain wordmark text:\n%s", buf.String())
	}
}

func TestLerp(t *testing.T) {
	if got := lerp(10, 20, 0); got != 10 {
		t.Fatalf("lerp(10,20,0) = %d, want 10", got)
	}
	if got := lerp(10, 20, 1); got != 20 {
		t.Fatalf("lerp(10,20,1) = %d, want 20", got)
	}
	got := lerp(0, 10, 0.5)
	if got <= 0 || got >= 10 {
		t.Fatalf("lerp(0,10,0.5) = %d, want strictly between 0 and 10", got)
	}
}

func TestFgAndMascotColoredNoColorWhenNotTTY(t *testing.T) {
	// go test stdout is not a TTY, so fg() must be a no-op like col().
	if got := fg(255, 0, 0, "x"); got != "x" {
		t.Fatalf("fg() = %q, want %q (no escapes on non-tty)", got, "x")
	}
	colored := MascotColored()
	if strings.Contains(colored, "\033[") {
		t.Fatalf("MascotColored() leaked ANSI escapes to non-tty output:\n%s", colored)
	}
	if colored != Mascot() {
		t.Fatalf("MascotColored() on non-tty should equal plain Mascot()")
	}
}

func TestRenderAllStatusCapsRuns(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 10; i++ {
		runDir := filepath.Join(root, run.Root, fmt.Sprintf("run-%02d", i))
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		state := run.State{
			ID:            fmt.Sprintf("run-%02d", i),
			ConsoleLabel:  fmt.Sprintf("t%d", i),
			CreatedAt:     time.Now().Add(time.Duration(i) * time.Minute),
			RepoRoot:      root,
			RunDir:        runDir,
			TaskFile:      filepath.Join(runDir, "task.md"),
			PlanFile:      filepath.Join(runDir, "plan.md"),
			PlannerDone:   filepath.Join(runDir, "planner.done"),
			ImplementDone: filepath.Join(runDir, "implement.done"),
			WorktreePath:  filepath.Join(runDir, "worktree"),
			TmuxSession:   "sidekick-test",
		}
		testutil.MustWrite(t, state.TaskFile, fmt.Sprintf("Task %02d\n", i))
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := RenderAllStatus(&out, root, 100); err != nil {
		t.Fatalf("RenderAllStatus() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"t10", "Task 10", "+2 more"} {
		if !strings.Contains(text, want) {
			t.Fatalf("aggregate status missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "t1 ") {
		t.Fatalf("aggregate status did not cap oldest run:\n%s", text)
	}
}

func stepStatuses(steps []PipelineStep) []string {
	statuses := make([]string, 0, len(steps))
	for _, step := range steps {
		statuses = append(statuses, step.Status)
	}
	return statuses
}

func stepStatusByName(steps []PipelineStep, name string) string {
	for _, step := range steps {
		if step.Name == name {
			return step.Status
		}
	}
	return ""
}
