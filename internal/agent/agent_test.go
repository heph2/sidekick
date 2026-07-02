package agent_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sidekick/internal/agent"
	"sidekick/internal/config"
	"sidekick/internal/run"
	"sidekick/internal/testutil"
)

func TestCommandPromptModes(t *testing.T) {
	a := config.AgentConfig{Name: "planner", Command: []string{"agent", "--model", "plan"}}

	stdinCmd, err := agent.Command(a, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("stdin mode: %v", err)
	}
	if stdinCmd.Path != "agent" || !reflect.DeepEqual(stdinCmd.Args, []string{"agent", "--model", "plan"}) {
		t.Fatalf("stdin args = %#v", stdinCmd.Args)
	}

	a.PromptMode = "arg"
	argCmd, err := agent.Command(a, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("arg mode: %v", err)
	}
	if !reflect.DeepEqual(argCmd.Args, []string{"agent", "--model", "plan", "prompt text"}) {
		t.Fatalf("arg args = %#v", argCmd.Args)
	}

	a.PromptMode = "file"
	fileCmd, err := agent.Command(a, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("file mode: %v", err)
	}
	if !reflect.DeepEqual(fileCmd.Args, []string{"agent", "--model", "plan", "prompt.md"}) {
		t.Fatalf("file args = %#v", fileCmd.Args)
	}
}

func TestCommandModel(t *testing.T) {
	a := config.AgentConfig{Name: "planner", Command: []string{"agent", "exec"}, PromptMode: "arg", Model: "opus"}
	cmd, err := agent.Command(a, "prompt text", "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent", "exec", "--model", "opus", "prompt text"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %#v, want %#v", cmd.Args, want)
	}

	a.ModelFlag = "--model-id"
	cmd, err = agent.Command(a, "prompt text", "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"agent", "exec", "--model-id", "opus", "prompt text"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("custom flag args = %#v, want %#v", cmd.Args, want)
	}

	a.Model = ""
	cmd, err = agent.Command(a, "prompt text", "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"agent", "exec", "prompt text"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("empty model args = %#v, want %#v", cmd.Args, want)
	}
}

func TestExpandPrompt(t *testing.T) {
	state := run.State{
		ID:           "run-1",
		RunDir:       "/runs/run-1",
		TaskFile:     "/runs/run-1/task.md",
		PlanFile:     "/runs/run-1/plan.md",
		MemoryFile:   "/repo/.sidekick/memory.md",
		WorktreePath: "/worktrees/run-1",
	}
	got := agent.ExpandPrompt("$SIDEKICK_RUN_ID|$SIDEKICK_RUN_DIR|$SIDEKICK_TASK_FILE|$SIDEKICK_PLAN_FILE|$SIDEKICK_MEMORY_FILE|$SIDEKICK_WORKTREE|$UNKNOWN", state)
	want := "run-1|/runs/run-1|/runs/run-1/task.md|/runs/run-1/plan.md|/repo/.sidekick/memory.md|/worktrees/run-1|"
	if got != want {
		t.Fatalf("ExpandPrompt() = %q, want %q", got, want)
	}
}

func TestPromptOverrides(t *testing.T) {
	state := testutil.RunState(t, false)

	planner := agent.PlannerPrompt(state, config.AgentConfig{Prompt: "plan $SIDEKICK_RUN_ID from $SIDEKICK_TASK_FILE"})
	if planner != "plan test-run from "+state.TaskFile {
		t.Fatalf("planner override = %q", planner)
	}

	implementerDefault := agent.ImplementerPrompt(state, config.AgentConfig{})
	if !strings.Contains(implementerDefault, "Sidekick implementation task") {
		t.Fatalf("implementer default missing built-in prompt:\n%s", implementerDefault)
	}

	reviewer := agent.ReviewerPrompt(state, config.AgentConfig{Name: "custom-reviewer", Prompt: "review $SIDEKICK_WORKTREE"})
	if reviewer != "review "+state.WorktreePath {
		t.Fatalf("reviewer override = %q", reviewer)
	}

	learner := agent.LearnPrompt(state, config.AgentConfig{Prompt: "learn $SIDEKICK_MEMORY_FILE"})
	if learner != "learn "+state.MemoryFile {
		t.Fatalf("learner override = %q", learner)
	}
}

func TestDefaultPromptsReferenceMemoryFile(t *testing.T) {
	state := testutil.RunState(t, false)
	for name, prompt := range map[string]string{
		"planner":     agent.PlannerPrompt(state, config.AgentConfig{}),
		"implementer": agent.ImplementerPrompt(state, config.AgentConfig{}),
		"reviewer":    agent.ReviewerPrompt(state, config.AgentConfig{Name: "reviewer"}),
		"learner":     agent.LearnPrompt(state, config.AgentConfig{}),
	} {
		if !strings.Contains(prompt, state.MemoryFile) {
			t.Fatalf("%s prompt missing memory file %q:\n%s", name, state.MemoryFile, prompt)
		}
	}
}

func TestForRoleLearner(t *testing.T) {
	cfg := (config.Config{}).WithDefaults()
	a, err := agent.ForRole(cfg, "learn")
	if err != nil {
		t.Fatalf("ForRole(learn) error = %v", err)
	}
	if a.Name != cfg.Agents.Learner.Name {
		t.Fatalf("learner agent = %q, want %q", a.Name, cfg.Agents.Learner.Name)
	}
}

func TestForRunRoleUsesStateSelection(t *testing.T) {
	cfg := (config.Config{}).WithDefaults()
	custom := config.AgentConfig{Name: "custom-implementer", Command: []string{"custom"}, PromptMode: "stdin"}
	cfg.Agents.Reviewers = append(cfg.Agents.Reviewers, custom)
	state := run.State{ImplementerName: custom.Name}

	a, err := agent.ForRunRole(cfg, state, "implementer")
	if err != nil {
		t.Fatalf("ForRunRole() error = %v", err)
	}
	if a.Name != custom.Name {
		t.Fatalf("agent = %q, want %q", a.Name, custom.Name)
	}
}

func TestParseVerdict(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "approve", body: "looks good\nSIDEKICK_VERDICT: approve\n", want: "approve"},
		{name: "revise", body: "fix it\nSIDEKICK_VERDICT: revise\n", want: "revise"},
		{name: "last wins", body: "SIDEKICK_VERDICT: revise\nlater\nSIDEKICK_VERDICT: approve\n", want: "approve"},
		{name: "unknown approves", body: "SIDEKICK_VERDICT: maybe\n", want: "approve"},
		{name: "no verdict approves", body: "no machine line\n", want: "approve"},
		{name: "case insensitive", body: "  sidekick_verdict: REQUEST-CHANGES\n", want: "revise"},
		{name: "aliases", body: "SIDEKICK_VERDICT: lgtm\n", want: "approve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, run.Slug(tc.name)+".log")
			testutil.MustWrite(t, path, tc.body)
			if got := agent.ParseVerdict(path); got != tc.want {
				t.Fatalf("ParseVerdict() = %q, want %q", got, tc.want)
			}
		})
	}
	if got := agent.ParseVerdict(filepath.Join(dir, "missing.log")); got != "approve" {
		t.Fatalf("missing verdict = %q, want approve", got)
	}
}

func TestReviewVerdictProcessErrorForcesRevise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.log")
	testutil.MustWrite(t, path, "SIDEKICK_VERDICT: approve\n")
	if got := agent.ReviewVerdict(path, exec.ErrNotFound); got != "revise" {
		t.Fatalf("ReviewVerdict(process error) = %q, want revise", got)
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
		if got := agent.ConfirmRelease(strings.NewReader(tc.in), &bytes.Buffer{}); got != tc.want {
			t.Errorf("ConfirmRelease(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
