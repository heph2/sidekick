// Package tmux wraps the tmux commands Sidekick uses to build its workspace.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// NewSession creates a detached session with one named window in dir.
func NewSession(session, window, dir string) error {
	if err := exec.Command("tmux", "new-session", "-d", "-s", session, "-n", window, "-c", dir).Run(); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}
	return nil
}

func NewWindow(session, name, dir string) error {
	return exec.Command("tmux", "new-window", "-t", session, "-n", name, "-c", dir).Run()
}

// NewWindowCommand creates a window that runs command instead of a shell.
func NewWindowCommand(session, name, dir, command string) error {
	return exec.Command("tmux", "new-window", "-t", session, "-n", name, "-c", dir, command).Run()
}

// KillWindow is best-effort.
func KillWindow(target string) {
	_ = exec.Command("tmux", "kill-window", "-t", target).Run()
}

// KillSession is best-effort; only call it on sessions Sidekick created.
func KillSession(session string) {
	_ = exec.Command("tmux", "kill-session", "-t", session).Run()
}

func SelectWindow(target string) error {
	return exec.Command("tmux", "select-window", "-t", target).Run()
}

func Send(target, command string) error {
	return exec.Command("tmux", "send-keys", "-t", target, command, "Enter").Run()
}

func PaneID(target string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", target, "#{pane_id}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentSession returns the session name of the surrounding tmux client.
func CurrentSession() (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve tmux session: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func Attach(session string) error {
	cmd := exec.Command("tmux", "attach", "-t", session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
