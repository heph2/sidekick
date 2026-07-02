// Package worktree provides isolated worktrees for runs: a Treehouse lease
// when the CLI is installed, otherwise a plain git worktree under .sidekick.
package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Lease gets an isolated worktree, preferring treehouse when it is available
// and falling back to a plain git worktree otherwise. It returns the worktree
// path and the backend used ("treehouse" or "git").
func Lease(root, runID string) (string, string, error) {
	if _, err := exec.LookPath("treehouse"); err == nil {
		cmd := exec.Command("treehouse", "get", "--lease", "--lease-holder", "sidekick:"+runID)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out)), "treehouse", nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr, "sidekick: treehouse lease failed (%s); using git worktree\n", msg)
	}
	return Git(root, runID)
}

// Git creates a plain git worktree on a new sidekick/<runID> branch.
func Git(root, runID string) (string, string, error) {
	// ponytail: branch off current HEAD, worktree lives in the ignored .sidekick tree
	path := filepath.Join(root, ".sidekick", "worktrees", runID)
	cmd := exec.Command("git", "-C", root, "worktree", "add", "-b", "sidekick/"+runID, path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", "", fmt.Errorf("git worktree add failed: %s", msg)
	}
	return path, "git", nil
}

// RemoveGit tears down a git-backed worktree and its branch, best-effort.
func RemoveGit(root, path, runID string) {
	_ = exec.Command("git", "-C", root, "worktree", "remove", "--force", path).Run()
	_ = exec.Command("git", "-C", root, "branch", "-D", "sidekick/"+runID).Run()
}

// Prune runs git worktree prune, best-effort.
func Prune(root string) {
	_ = exec.Command("git", "-C", root, "worktree", "prune").Run()
}
