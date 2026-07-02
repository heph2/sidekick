// Package testutil provides shared fixtures for Sidekick's package tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sidekick/internal/run"
)

// MustWrite writes a file or fails the test.
func MustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// RunState builds a saved run state in a temp dir, mirroring what spawnRun
// records for a real run.
func RunState(t *testing.T, gate bool) run.State {
	t.Helper()
	runDir := t.TempDir()
	state := run.State{
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
	MustWrite(t, state.TaskFile, "Ship the dashboard\n\nExtra detail\n")
	if err := state.Save(); err != nil {
		t.Fatalf("state.Save() error = %v", err)
	}
	return state
}

// GitInit creates a git repo with one empty commit and returns its root.
func GitInit(t *testing.T) string {
	t.Helper()
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
	return root
}
