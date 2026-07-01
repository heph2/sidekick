package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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

func TestCommandForAgentModel(t *testing.T) {
	agent := AgentConfig{Name: "planner", Command: []string{"agent", "exec"}, PromptMode: "arg", Model: "opus"}
	cmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent", "exec", "--model", "opus", "prompt text"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %#v, want %#v", cmd.Args, want)
	}

	agent.ModelFlag = "--model-id"
	cmd, err = commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"agent", "exec", "--model-id", "opus", "prompt text"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("custom flag args = %#v, want %#v", cmd.Args, want)
	}

	agent.Model = ""
	cmd, err = commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"agent", "exec", "prompt text"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("empty model args = %#v, want %#v", cmd.Args, want)
	}
}

func TestExpandPrompt(t *testing.T) {
	state := RunState{
		ID:           "run-1",
		RunDir:       "/runs/run-1",
		TaskFile:     "/runs/run-1/task.md",
		PlanFile:     "/runs/run-1/plan.md",
		MemoryFile:   "/repo/.sidekick/memory.md",
		WorktreePath: "/worktrees/run-1",
	}
	got := expandPrompt("$SIDEKICK_RUN_ID|$SIDEKICK_RUN_DIR|$SIDEKICK_TASK_FILE|$SIDEKICK_PLAN_FILE|$SIDEKICK_MEMORY_FILE|$SIDEKICK_WORKTREE|$UNKNOWN", state)
	want := "run-1|/runs/run-1|/runs/run-1/task.md|/runs/run-1/plan.md|/repo/.sidekick/memory.md|/worktrees/run-1|"
	if got != want {
		t.Fatalf("expandPrompt() = %q, want %q", got, want)
	}
}

func TestPromptOverrides(t *testing.T) {
	state := testRunState(t, false)

	planner := plannerPrompt(state, AgentConfig{Prompt: "plan $SIDEKICK_RUN_ID from $SIDEKICK_TASK_FILE"})
	if planner != "plan test-run from "+state.TaskFile {
		t.Fatalf("planner override = %q", planner)
	}

	implementerDefault := implementerPrompt(state, AgentConfig{})
	if !strings.Contains(implementerDefault, "Sidekick implementation task") {
		t.Fatalf("implementer default missing built-in prompt:\n%s", implementerDefault)
	}

	reviewer := reviewerPrompt(state, AgentConfig{Name: "custom-reviewer", Prompt: "review $SIDEKICK_WORKTREE"})
	if reviewer != "review "+state.WorktreePath {
		t.Fatalf("reviewer override = %q", reviewer)
	}

	learner := learnPrompt(state, AgentConfig{Prompt: "learn $SIDEKICK_MEMORY_FILE"})
	if learner != "learn "+state.MemoryFile {
		t.Fatalf("learner override = %q", learner)
	}
}

func TestDefaultPromptsReferenceMemoryFile(t *testing.T) {
	state := testRunState(t, false)
	for name, prompt := range map[string]string{
		"planner":     plannerPrompt(state, AgentConfig{}),
		"implementer": implementerPrompt(state, AgentConfig{}),
		"reviewer":    reviewerPrompt(state, AgentConfig{Name: "reviewer"}),
		"learner":     learnPrompt(state, AgentConfig{}),
	} {
		if !strings.Contains(prompt, state.MemoryFile) {
			t.Fatalf("%s prompt missing memory file %q:\n%s", name, state.MemoryFile, prompt)
		}
	}
}

func TestNotifyConfigJSON(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"notify":{"noBell":true,"command":["notify-send","Sidekick"]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Notify.NoBell {
		t.Fatal("NoBell = false, want true")
	}
	if !reflect.DeepEqual(cfg.Notify.Command, []string{"notify-send", "Sidekick"}) {
		t.Fatalf("notify command = %#v", cfg.Notify.Command)
	}

	cfg = (Config{}).withDefaults()
	if cfg.Notify.NoBell {
		t.Fatal("omitted notify disabled bell; want bell enabled by default")
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
	if cfg.Agents.Learner.Name == "" {
		t.Fatal("learner default missing")
	}
	if !reflect.DeepEqual(cfg.Gate.Command, []string{"no-mistakes", "-y"}) {
		t.Fatalf("gate command = %#v", cfg.Gate.Command)
	}
}

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

func TestAgentForRoleLearner(t *testing.T) {
	cfg := (Config{}).withDefaults()
	agent, err := agentForRole(cfg, "learn")
	if err != nil {
		t.Fatalf("agentForRole(learn) error = %v", err)
	}
	if agent.Name != cfg.Agents.Learner.Name {
		t.Fatalf("learner agent = %q, want %q", agent.Name, cfg.Agents.Learner.Name)
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

func TestBuildStatusViewLearnStep(t *testing.T) {
	state := testRunState(t, false)
	state.ReviewerNames = nil
	state.LearnEnabled = true
	state.LearnDone = filepath.Join(state.RunDir, "learn.done")
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}

	view, err := buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "waiting" {
		t.Fatalf("learn status = %q, want waiting", got)
	}

	mustWrite(t, state.PlannerDone, "done\n")
	mustWrite(t, state.ImplementDone, "done\n")
	mustWrite(t, learnLog(state), "learning\n")
	view, err = buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "running" {
		t.Fatalf("learn status = %q, want running", got)
	}

	mustWrite(t, state.LearnDone, "done\n")
	view, err = buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "done" {
		t.Fatalf("learn status = %q, want done", got)
	}
}

func TestBuildStatusViewLearnWaitsForGate(t *testing.T) {
	state := testRunState(t, true)
	state.ReviewerNames = nil
	state.LearnEnabled = true
	state.LearnDone = filepath.Join(state.RunDir, "learn.done")
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, state.PlannerDone, "done\n")
	mustWrite(t, state.ImplementDone, "done\n")
	mustWrite(t, learnLog(state), "learning\n")

	view, err := buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "waiting" {
		t.Fatalf("learn status before gate = %q, want waiting", got)
	}

	mustWrite(t, gateDone(state), "done\n")
	view, err = buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "running" {
		t.Fatalf("learn status after gate = %q, want running", got)
	}
}

func TestBuildStatusViewOmitsLearnWhenDisabled(t *testing.T) {
	state := testRunState(t, false)
	state.LearnEnabled = false
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}
	view, err := buildStatusView(state.RunDir)
	if err != nil {
		t.Fatalf("buildStatusView() error = %v", err)
	}
	if got := stepStatusByName(view.Steps, "learn"); got != "" {
		t.Fatalf("learn step present when disabled: %q", got)
	}
}

func TestRenderStatusIncludesMascotAndArtifacts(t *testing.T) {
	state := testRunState(t, false)
	var out bytes.Buffer
	if err := renderStatus(&out, state.RunDir, 80); err != nil {
		t.Fatalf("renderStatus() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"Sidekick", "always-on companion", "Ship the dashboard", "worktree:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered status missing %q:\n%s", want, text)
		}
	}
}

func TestGitWorktreeFallback(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	path, backend, err := gitWorktree(root, "test-run")
	if err != nil {
		t.Fatalf("gitWorktree() error = %v", err)
	}
	if backend != "git" {
		t.Fatalf("backend = %q, want git", backend)
	}
	want := filepath.Join(root, ".sidekick", "worktrees", "test-run")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("worktree dir missing: %v", err)
	}
}

func TestConfirmRelease(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false}, // EOF
	} {
		if got := confirmRelease(strings.NewReader(tc.in), &bytes.Buffer{}); got != tc.want {
			t.Errorf("confirmRelease(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFindWaitingRunAndRelease(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, runRoot)
	mk := func(name string, done bool) string {
		dir := filepath.Join(runs, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "state.json"), "{}")
		if done {
			mustWrite(t, filepath.Join(dir, "planner.done"), "x")
		}
		return dir
	}
	mk("20260101-000000-old", true) // already released
	waiting := mk("20260202-000000-new", false)

	got, err := findWaitingRun(root)
	if err != nil {
		t.Fatalf("findWaitingRun() error = %v", err)
	}
	if got != waiting {
		t.Fatalf("findWaitingRun() = %q, want %q", got, waiting)
	}

	if err := signalPlanner([]string{"--run-dir", waiting}, false); err != nil {
		t.Fatalf("release error = %v", err)
	}
	if !fileExists(filepath.Join(waiting, "planner.done")) {
		t.Fatal("planner.done not written")
	}
	// releasing again must refuse to clobber
	if err := signalPlanner([]string{"--run-dir", waiting}, false); err == nil {
		t.Fatal("expected already-released error on second release")
	}
	// nothing left waiting
	if _, err := findWaitingRun(root); err == nil {
		t.Fatal("expected no-waiting-run error")
	}
}

func TestLandCommit(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "root"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	wt, _, err := gitWorktree(root, "land-run")
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

func TestRenderStatusNoColorWhenNotTTY(t *testing.T) {
	// go test stdout is not a TTY, so col() and the mascot's fg() must emit no
	// escape codes.
	state := testRunState(t, false)
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.TaskFile, []byte("do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := renderStatus(&buf, state.RunDir, 100); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\033[") {
		t.Fatalf("dashboard leaked ANSI escapes to non-tty output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Sidekick") {
		t.Fatalf("dashboard missing plain mascot text:\n%s", buf.String())
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
	colored := mascotColored()
	if strings.Contains(colored, "\033[") {
		t.Fatalf("mascotColored() leaked ANSI escapes to non-tty output:\n%s", colored)
	}
	if colored != mascot() {
		t.Fatalf("mascotColored() on non-tty should equal plain mascot()")
	}
}

func TestUniqueRunDirDistinctOnCollision(t *testing.T) {
	root := t.TempDir()
	id1, dir1, err := uniqueRunDir(root, "same task")
	if err != nil {
		t.Fatalf("uniqueRunDir() error = %v", err)
	}
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatal(err)
	}
	id2, dir2, err := uniqueRunDir(root, "same task")
	if err != nil {
		t.Fatalf("uniqueRunDir() error = %v", err)
	}
	if id1 == id2 || dir1 == dir2 {
		t.Fatalf("uniqueRunDir() collided: %q == %q", dir1, dir2)
	}
	if !strings.HasSuffix(id2, "-2") {
		t.Fatalf("id2 = %q, want suffix -2", id2)
	}
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	id3, dir3, err := uniqueRunDir(root, "same task")
	if err != nil {
		t.Fatalf("uniqueRunDir() error = %v", err)
	}
	if dir3 == dir1 || dir3 == dir2 {
		t.Fatalf("uniqueRunDir() collided on third call: %q", dir3)
	}
	if !strings.HasSuffix(id3, "-3") {
		t.Fatalf("id3 = %q, want suffix -3", id3)
	}
}

func TestCleanRunsRemovesGitWorktree(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	path, backend, err := gitWorktree(root, "test-run")
	if err != nil {
		t.Fatalf("gitWorktree() error = %v", err)
	}
	runDir := filepath.Join(root, runRoot, "test-run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := RunState{ID: "test-run", RunDir: runDir, WorktreePath: path, WorktreeBackend: backend, TmuxSession: "sidekick-test-run"}
	if err := writeState(state); err != nil {
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
		MemoryFile:      filepath.Join(runDir, "repo", ".sidekick", "memory.md"),
		PlannerDone:     filepath.Join(runDir, "planner.done"),
		ImplementDone:   filepath.Join(runDir, "implement.done"),
		LearnDone:       filepath.Join(runDir, "learn.done"),
		WorktreePath:    filepath.Join(runDir, "worktree"),
		TmuxSession:     "sidekick-test-run",
		GateEnabled:     gate,
		LearnEnabled:    false,
		PlannerName:     "claude-planner",
		ImplementerName: "codex-implementer",
		LearnerName:     "claude-learner",
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

func stepStatusByName(steps []PipelineStep, name string) string {
	for _, step := range steps {
		if step.Name == name {
			return step.Status
		}
	}
	return ""
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
