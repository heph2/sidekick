package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	got := Slug("Implement Sidekick: plan -> code + review!")
	want := "implement-sidekick-plan-code-review"
	if got != want {
		t.Fatalf("Slug() = %q, want %q", got, want)
	}
}

func TestUniqueDirDistinctOnCollision(t *testing.T) {
	root := t.TempDir()
	id1, dir1, err := UniqueDir(root, "same task")
	if err != nil {
		t.Fatalf("UniqueDir() error = %v", err)
	}
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatal(err)
	}
	id2, dir2, err := UniqueDir(root, "same task")
	if err != nil {
		t.Fatalf("UniqueDir() error = %v", err)
	}
	if id1 == id2 || dir1 == dir2 {
		t.Fatalf("UniqueDir() collided: %q == %q", dir1, dir2)
	}
	if !strings.HasSuffix(id2, "-2") {
		t.Fatalf("id2 = %q, want suffix -2", id2)
	}
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	id3, dir3, err := UniqueDir(root, "same task")
	if err != nil {
		t.Fatalf("UniqueDir() error = %v", err)
	}
	if dir3 == dir1 || dir3 == dir2 {
		t.Fatalf("UniqueDir() collided on third call: %q", dir3)
	}
	if !strings.HasSuffix(id3, "-3") {
		t.Fatalf("id3 = %q, want suffix -3", id3)
	}
}

func TestFindWaiting(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, Root)
	mk := func(name string, done bool) string {
		dir := filepath.Join(runs, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if done {
			if err := os.WriteFile(filepath.Join(dir, "planner.done"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	mk("20260101-000000-old", true) // already released
	waiting := mk("20260202-000000-new", false)

	got, err := FindWaiting(root)
	if err != nil {
		t.Fatalf("FindWaiting() error = %v", err)
	}
	if got != waiting {
		t.Fatalf("FindWaiting() = %q, want %q", got, waiting)
	}
}
