package agent

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var verdictLineRE = regexp.MustCompile(`(?i)^\s*SIDEKICK_VERDICT:\s*([a-z-]+)`)

// ParseVerdict scans a reviewer log for the last SIDEKICK_VERDICT line.
// Missing or unknown verdicts approve, so a chatty reviewer never wedges the
// pipeline.
func ParseVerdict(logPath string) string {
	file, err := os.Open(logPath)
	if err != nil {
		return "approve"
	}
	defer file.Close()

	last := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		match := verdictLineRE.FindStringSubmatch(scanner.Text())
		if len(match) == 2 {
			last = strings.ToLower(match[1])
		}
	}
	switch last {
	case "approve", "approved", "lgtm":
		return "approve"
	case "revise", "request-changes", "changes":
		return "revise"
	default:
		return "approve"
	}
}

// ReviewVerdict folds a reviewer process error into the verdict: a crashed
// reviewer always requests changes.
func ReviewVerdict(logPath string, processErr error) string {
	if processErr != nil {
		return "revise"
	}
	return ParseVerdict(logPath)
}
