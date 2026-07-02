// Package sh quotes strings for POSIX shells (tmux send-keys, sh -c).
package sh

import (
	"regexp"
	"strings"
)

var plainRE = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+-]+$`)

func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if plainRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func Join(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, Quote(arg))
	}
	return strings.Join(quoted, " ")
}
