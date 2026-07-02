// Package run owns per-run state: the .sidekick/runs/<id> directory, the
// state.json file, log and done-marker paths, and run discovery helpers.
package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Root is the runs directory relative to the repo root.
const Root = ".sidekick/runs"

// State is the persistent record of one run, stored as state.json in the
// run directory.
type State struct {
	ID              string    `json:"id"`
	ConsoleLabel    string    `json:"consoleLabel,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	RepoRoot        string    `json:"repoRoot"`
	RunDir          string    `json:"runDir"`
	TaskFile        string    `json:"taskFile"`
	PlanFile        string    `json:"planFile"`
	MemoryFile      string    `json:"memoryFile"`
	PlannerDone     string    `json:"plannerDone"`
	ImplementDone   string    `json:"implementDone"`
	LearnDone       string    `json:"learnDone"`
	WorktreePath    string    `json:"worktreePath"`
	WorktreeBackend string    `json:"worktreeBackend"`
	TmuxSession     string    `json:"tmuxSession"`
	GateEnabled     bool      `json:"gateEnabled"`
	LearnEnabled    bool      `json:"learnEnabled"`
	LandEnabled     bool      `json:"landEnabled"`
	PlannerName     string    `json:"plannerName"`
	ImplementerName string    `json:"implementerName"`
	LearnerName     string    `json:"learnerName"`
	ReviewerNames   []string  `json:"reviewerNames"`
}

func (s State) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(s.RunDir, "state.json"), data, 0o644)
}

func Load(runDir string) (State, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s State) ImplementerLog() string { return filepath.Join(s.RunDir, "implementer.log") }
func (s State) GateLog() string        { return filepath.Join(s.RunDir, "gate.log") }
func (s State) GateDone() string       { return filepath.Join(s.RunDir, "gate.done") }
func (s State) LearnLog() string       { return filepath.Join(s.RunDir, "learn.log") }
func (s State) LandLog() string        { return filepath.Join(s.RunDir, "land.log") }
func (s State) LandReady() string      { return filepath.Join(s.RunDir, "land.ready") }
func (s State) LandApprove() string    { return filepath.Join(s.RunDir, "land.approve") }
func (s State) LandDone() string       { return filepath.Join(s.RunDir, "land.done") }

func (s State) ReviewerLog(reviewer string) string {
	return filepath.Join(s.RunDir, "reviewer-"+Slug(reviewer)+".log")
}

func (s State) ReviewerDone(reviewer string) string {
	return filepath.Join(s.RunDir, "reviewer-"+Slug(reviewer)+".done")
}

// Env returns the process environment extended with the SIDEKICK_* variables
// describing this run, for agent and gate subprocesses.
func (s State) Env() []string {
	return append(os.Environ(),
		"SIDEKICK_RUN_ID="+s.ID,
		"SIDEKICK_RUN_DIR="+s.RunDir,
		"SIDEKICK_TASK_FILE="+s.TaskFile,
		"SIDEKICK_PLAN_FILE="+s.PlanFile,
		"SIDEKICK_MEMORY_FILE="+s.MemoryFile,
		"SIDEKICK_WORKTREE="+s.WorktreePath,
	)
}

// Mark writes a timestamped done-marker file.
func Mark(path string) error {
	return os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// WaitFile blocks until path exists, or fails when path+".failed" appears
// first (an upstream stage aborted).
func WaitFile(path string) error {
	failed := path + ".failed"
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if Exists(failed) {
			return fmt.Errorf("upstream step failed: %s", failed)
		}
		time.Sleep(2 * time.Second)
	}
}

func NewID(task string) string {
	stamp := time.Now().Format("20060102-150405")
	s := Slug(task)
	if len(s) > 32 {
		s = s[:32]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "task"
	}
	return stamp + "-" + s
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// UniqueDir picks a run ID/dir for task under root, guarding against NewID's
// 1-second granularity: if the directory already exists (two spawns within
// the same second), it suffixes -2, -3, ... until free.
func UniqueDir(root, task string) (string, string, error) {
	base := NewID(task)
	id := base
	for i := 2; ; i++ {
		dir := filepath.Join(root, Root, id)
		if !Exists(dir) {
			return id, dir, nil
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// FindWaiting returns the newest run dir under root that has a plan but no
// planner.done/.failed yet (i.e. awaiting human approval). Run IDs are
// timestamp-prefixed, so the lexically largest name is the newest.
func FindWaiting(root string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, Root))
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("no runs found")
		}
		return "", err
	}
	best := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, Root, e.Name())
		done := filepath.Join(dir, "planner.done")
		if Exists(done) || Exists(done+".failed") {
			continue
		}
		if !Exists(filepath.Join(dir, "state.json")) {
			continue
		}
		if best == "" || e.Name() > filepath.Base(best) {
			best = dir
		}
	}
	if best == "" {
		return "", errors.New("no run is awaiting approval")
	}
	return best, nil
}

// FindReadyLand returns the newest run dir that is ready to ship (land.ready
// exists) and not yet approved or landed.
func FindReadyLand(root string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, Root))
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("no runs found")
		}
		return "", err
	}
	best := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, Root, e.Name())
		state, err := Load(dir)
		if err != nil {
			continue
		}
		if !Exists(state.LandReady()) || Exists(state.LandApprove()) || Exists(state.LandDone()) {
			continue
		}
		if best == "" || e.Name() > filepath.Base(best) {
			best = dir
		}
	}
	if best == "" {
		return "", errors.New("no run is ready to ship")
	}
	return best, nil
}

// RepoRoot resolves path to its git toplevel directory.
func RepoRoot(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s is not inside a git repository: %s", path, msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// FirstLine returns the first non-blank line of a file, with placeholders for
// unreadable or empty files (used for run goals on dashboards).
func FirstLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "empty task"
}

// ReadLine is FirstLine without placeholders: empty string when missing/blank.
func ReadLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// LastLines returns up to count trailing non-blank lines of a file.
func LastLines(path string, count int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var lines []string
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) <= count {
		return lines
	}
	return lines[len(lines)-count:]
}
