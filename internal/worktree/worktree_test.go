package worktree_test

import (
	"os"
	"path/filepath"
	"testing"

	"sidekick/internal/testutil"
	"sidekick/internal/worktree"
)

func TestGitWorktree(t *testing.T) {
	root := testutil.GitInit(t)

	path, backend, err := worktree.Git(root, "test-run")
	if err != nil {
		t.Fatalf("worktree.Git() error = %v", err)
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
