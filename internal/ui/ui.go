// Package ui renders Sidekick's terminal output: colors, the mascot, status
// dashboards, and small terminal helpers.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// col wraps s in an ANSI color unless NO_COLOR is set or stdout is not a TTY.
func col(code, s string) string {
	if code == "" || os.Getenv("NO_COLOR") != "" || !stdoutIsTTY() {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// fg wraps s in a 24-bit truecolor ANSI escape, gated by the same rule as
// col: no-op when NO_COLOR is set or stdout is not a TTY.
//
// ponytail: assumes the terminal supports truecolor; the honest upgrade path
// is to gate on COLORTERM=truecolor/24bit and fall back to the existing
// 256-color col() when it isn't set. Not built - most terminals people use
// today (including tmux with default-terminal set right) support it, and a
// wrong guess just means a slightly off gradient, not broken output.
func fg(r, g, b int, s string) string {
	if os.Getenv("NO_COLOR") != "" || !stdoutIsTTY() {
		return s
	}
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", r, g, b, s)
}

// lerp linearly interpolates between from and to at t in [0,1].
func lerp(from, to int, t float64) int {
	return from + int(float64(to-from)*t)
}

func Mascot() string {
	return `      .        *          Sidekick
   *    \   .    /   *
      \  \  |  /  /       always-on companion
  - -- >   \|/   < -- -
      /  /  |  \  \
   *    /   '   \    *`
}

// MascotColored renders the sparkle mascot with an orange->pink gradient
// applied per line.
func MascotColored() string {
	from := [3]int{255, 140, 0}
	to := [3]int{255, 105, 180}
	lines := strings.Split(Mascot(), "\n")
	last := len(lines) - 1
	for i, line := range lines {
		t := 0.0
		if last > 0 {
			t = float64(i) / float64(last)
		}
		r := lerp(from[0], to[0], t)
		g := lerp(from[1], to[1], t)
		b := lerp(from[2], to[2], t)
		lines[i] = fg(r, g, b, line)
	}
	return strings.Join(lines, "\n")
}

func TerminalWidth() int {
	if columns := strings.TrimSpace(os.Getenv("COLUMNS")); columns != "" {
		var width int
		if _, err := fmt.Sscanf(columns, "%d", &width); err == nil && width > 0 {
			return width
		}
	}
	return 100
}

func Clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// PromptYesNo reads one line; only "y"/"yes" is true. EOF/anything else false.
func PromptYesNo(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprint(out, question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
