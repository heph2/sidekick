package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sidekick/internal/run"
	"sidekick/internal/testutil"
	"sidekick/internal/worktree"
)

func TestPresetAgent(t *testing.T) {
	tests := []struct {
		harness     string
		role        string
		command     []string
		promptMode  string
		interactive bool
	}{
		{"claude", "planner", []string{"claude"}, "arg", true},
		{"claude", "implementer", []string{"claude"}, "stdin", false},
		{"claude", "reviewer", []string{"claude"}, "stdin", false},
		{"claude", "learner", []string{"claude"}, "stdin", false},
		{"codex", "planner", []string{"codex", "exec"}, "stdin", false},
		{"codex", "implementer", []string{"codex", "exec", "--sandbox", "workspace-write"}, "stdin", false},
		{"codex", "reviewer", []string{"codex", "exec"}, "stdin", false},
		{"codex", "learner", []string{"codex", "exec"}, "stdin", false},
	}

	for _, tc := range tests {
		got := presetAgent(tc.harness, tc.role)
		if !reflect.DeepEqual(got.Command, tc.command) {
			t.Fatalf("presetAgent(%q, %q).Command = %#v, want %#v", tc.harness, tc.role, got.Command, tc.command)
		}
		if got.PromptMode != tc.promptMode {
			t.Fatalf("presetAgent(%q, %q).PromptMode = %q, want %q", tc.harness, tc.role, got.PromptMode, tc.promptMode)
		}
		if got.Interactive != tc.interactive {
			t.Fatalf("presetAgent(%q, %q).Interactive = %v, want %v", tc.harness, tc.role, got.Interactive, tc.interactive)
		}
	}
}

func TestStageCommands(t *testing.T) {
	state := testutil.RunState(t, true)
	stages := stageCommands(state, true, true, true)
	var names []string
	interactive := map[string]bool{}
	for _, stage := range stages {
		names = append(names, stage.Name)
		interactive[stage.Name] = stage.Interactive
	}
	want := []string{"planner", "cycle", "gate", "learn", "land"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("stage names = %#v, want %#v", names, want)
	}
	if !interactive["planner"] {
		t.Fatal("planner stage should be interactive")
	}
	for _, name := range names[1:] {
		if interactive[name] {
			t.Fatalf("%s stage should be headless", name)
		}
	}

	stages = stageCommands(state, false, false, false)
	names = nil
	for _, stage := range stages {
		names = append(names, stage.Name)
	}
	want = []string{"planner", "cycle"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("minimal stage names = %#v, want %#v", names, want)
	}
}

func TestParseConsoleInput(t *testing.T) {
	cases := []struct {
		line string
		want consoleInput
	}{
		{"build the thing", consoleInput{Kind: "task", Task: "build the thing"}},
		{"/release t2", consoleInput{Kind: "command", Command: "release", Args: []string{"t2"}}},
		{"/ship", consoleInput{Kind: "command", Command: "ship"}},
		{"/help", consoleInput{Kind: "command", Command: "help"}},
		{"/foo", consoleInput{Kind: "command", Command: "foo"}},
		{"quit", consoleInput{Kind: "exit"}},
	}
	for _, tc := range cases {
		if got := parseConsoleInput(tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parseConsoleInput(%q) = %#v, want %#v", tc.line, got, tc.want)
		}
	}
}

func TestSignalPlannerReleasesOnce(t *testing.T) {
	root := t.TempDir()
	waiting := filepath.Join(root, run.Root, "20260202-000000-new")
	if err := os.MkdirAll(waiting, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.MustWrite(t, filepath.Join(waiting, "state.json"), "{}")

	if err := signalPlanner([]string{"--run-dir", waiting}, false); err != nil {
		t.Fatalf("release error = %v", err)
	}
	if !run.Exists(filepath.Join(waiting, "planner.done")) {
		t.Fatal("planner.done not written")
	}
	// releasing again must refuse to clobber
	if err := signalPlanner([]string{"--run-dir", waiting}, false); err == nil {
		t.Fatal("expected already-released error on second release")
	}
	// nothing left waiting
	if _, err := run.FindWaiting(root); err == nil {
		t.Fatal("expected no-waiting-run error")
	}
}

func TestLandCommit(t *testing.T) {
	root := testutil.GitInit(t)
	wt, _, err := worktree.Git(root, "land-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "NEW.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, changed, err := landCommit(wt, "add a file")
	if err != nil {
		t.Fatalf("landCommit() error = %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if branch != "sidekick/land-run" {
		t.Fatalf("branch = %q, want sidekick/land-run", branch)
	}
	// commit exists on the branch, nothing pushed (no remote configured)
	out, err := exec.Command("git", "-C", wt, "log", "--oneline", "-1").Output()
	if err != nil || !strings.Contains(string(out), "sidekick: add a file") {
		t.Fatalf("commit missing: %q err=%v", out, err)
	}
	// second call on a clean tree reports nothing to land
	if _, changed, err := landCommit(wt, "again"); err != nil || changed {
		t.Fatalf("clean landCommit: changed=%v err=%v, want false/nil", changed, err)
	}
}

func TestShipRunWritesApproval(t *testing.T) {
	state := testutil.RunState(t, false)
	state.LandEnabled = true
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	testutil.MustWrite(t, state.LandReady(), "ready\n")

	if err := shipRun([]string{"--run-dir", state.RunDir}); err != nil {
		t.Fatalf("shipRun() error = %v", err)
	}
	if !run.Exists(state.LandApprove()) {
		t.Fatal("land.approve not written")
	}
}

func TestPlanSummaryUsesGoalSection(t *testing.T) {
	state := testutil.RunState(t, false)
	testutil.MustWrite(t, state.PlanFile, "# Plan\n\n## Goal\n\nShip a fixed console dashboard. It should stay compact.\n\n## Steps\n\n- one\n")
	title, body := planSummary(state.PlanFile, state.TaskFile, state.ID)
	if title != "Ship a fixed console dashboard." {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(body, "Ship a fixed console dashboard") || !strings.Contains(body, "Plan: .sidekick/runs/test-run/plan.md") {
		t.Fatalf("body = %q", body)
	}
}

func TestCleanRunsRemovesGitWorktree(t *testing.T) {
	root := testutil.GitInit(t)

	path, backend, err := worktree.Git(root, "test-run")
	if err != nil {
		t.Fatalf("worktree.Git() error = %v", err)
	}
	runDir := filepath.Join(root, run.Root, "test-run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := run.State{ID: "test-run", RunDir: runDir, WorktreePath: path, WorktreeBackend: backend, TmuxSession: "sidekick-test-run"}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	if err := cleanRuns([]string{"--repo", root}); err != nil {
		t.Fatalf("cleanRuns() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("run dir still present: %v", err)
	}
	out, _ := exec.Command("git", "-C", root, "branch", "--list", "sidekick/test-run").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("branch not deleted: %q", out)
	}
}
