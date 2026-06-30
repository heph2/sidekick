package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	got := slug("Implement Sidekick: plan -> code + review!")
	want := "implement-sidekick-plan-code-review"
	if got != want {
		t.Fatalf("slug() = %q, want %q", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain/path-1": "plain/path-1",
		"":             "''",
		"has space":    "'has space'",
		"it's fine":    "'it'\\''s fine'",
	}
	for input, want := range cases {
		if got := shellQuote(input); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCommandForAgentPromptModes(t *testing.T) {
	agent := AgentConfig{Name: "planner", Command: []string{"agent", "--model", "plan"}}

	stdinCmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("stdin mode: %v", err)
	}
	if stdinCmd.Path != "agent" || !reflect.DeepEqual(stdinCmd.Args, []string{"agent", "--model", "plan"}) {
		t.Fatalf("stdin args = %#v", stdinCmd.Args)
	}

	agent.PromptMode = "arg"
	argCmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("arg mode: %v", err)
	}
	if !reflect.DeepEqual(argCmd.Args, []string{"agent", "--model", "plan", "prompt text"}) {
		t.Fatalf("arg args = %#v", argCmd.Args)
	}

	agent.PromptMode = "file"
	fileCmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("file mode: %v", err)
	}
	if !reflect.DeepEqual(fileCmd.Args, []string{"agent", "--model", "plan", "prompt.md"}) {
		t.Fatalf("file args = %#v", fileCmd.Args)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := (Config{}).withDefaults()
	if cfg.Agents.Planner.Name == "" {
		t.Fatal("planner default missing")
	}
	if cfg.Agents.Implementer.Name == "" {
		t.Fatal("implementer default missing")
	}
	if len(cfg.Agents.Reviewers) != 2 {
		t.Fatalf("reviewer defaults = %d, want 2", len(cfg.Agents.Reviewers))
	}
	if !reflect.DeepEqual(cfg.Gate.Command, []string{"no-mistakes", "-y"}) {
		t.Fatalf("gate command = %#v", cfg.Gate.Command)
	}
}

func TestBuildStatusViewWaiting(t *testing.T) {
	state := testRunState(t, false)
	view, err := buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
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
	state := testRunState(t, true)
	mustWrite(t, state.PlanFile, "Plan\n")
	mustWrite(t, state.PlannerDone, "done\n")
	mustWrite(t, implementerLog(state), "working\n")

	view, err := buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
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

	mustWrite(t, state.ImplementDone, "done\n")
	mustWrite(t, reviewerLog(state, "codex-reviewer"), "reviewing\n")
	view, err = buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	wantStatuses = []string{"done", "done", "running", "waiting"}
	if got := stepStatuses(view.Steps); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("statuses = %#v, want %#v", got, wantStatuses)
	}

	mustWrite(t, reviewerDone(state, "codex-reviewer"), "done\n")
	mustWrite(t, gateLog(state), "gate passed\n")
	mustWrite(t, gateDone(state), "done\n")
	view, err = buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if view.Phase != "complete" {
		t.Fatalf("phase = %q, want complete", view.Phase)
	}
	wantStatuses = []string{"done", "done", "done", "done"}
	if got := stepStatuses(view.Steps); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("statuses = %#v, want %#v", got, wantStatuses)
	}
}

func TestRenderStatusIncludesMascotAndArtifacts(t *testing.T) {
	state := testRunState(t, false)
	var out bytes.Buffer
	if err := renderStatus(&out, state.RunDir, 80); err != nil {
		t.Fatalf("renderStatus() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"Sidekick", "wood-hero support console", "Ship the dashboard", "worktree:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered status missing %q:\n%s", want, text)
		}
	}
}

func testRunState(t *testing.T, gate bool) RunState {
	t.Helper()
	runDir := t.TempDir()
	state := RunState{
		ID:              "test-run",
		CreatedAt:       time.Now().Add(-time.Minute),
		RepoRoot:        filepath.Join(runDir, "repo"),
		RunDir:          runDir,
		TaskFile:        filepath.Join(runDir, "task.md"),
		PlanFile:        filepath.Join(runDir, "plan.md"),
		PlannerDone:     filepath.Join(runDir, "planner.done"),
		ImplementDone:   filepath.Join(runDir, "implement.done"),
		WorktreePath:    filepath.Join(runDir, "worktree"),
		TmuxSession:     "sidekick-test-run",
		GateEnabled:     gate,
		PlannerName:     "claude-planner",
		ImplementerName: "codex-implementer",
		ReviewerNames:   []string{"codex-reviewer"},
	}
	mustWrite(t, state.TaskFile, "Ship the dashboard\n\nExtra detail\n")
	if err := writeState(state); err != nil {
		t.Fatalf("writeState() error = %v", err)
	}
	return state
}

func stepStatuses(steps []PipelineStep) []string {
	statuses := make([]string, 0, len(steps))
	for _, step := range steps {
		statuses = append(statuses, step.Status)
	}
	return statuses
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
