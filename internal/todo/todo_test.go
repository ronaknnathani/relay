package todo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNextID(t *testing.T) {
	if got := NextID(nil); got != 1 {
		t.Errorf("empty: got %d, want 1", got)
	}
	if got := NextID([]Todo{{ID: 3}, {ID: 1}, {ID: 2}}); got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

func TestPending(t *testing.T) {
	in := []Todo{{ID: 1}, {ID: 2, Done: true}, {ID: 3}}
	got := Pending(in)
	want := []Todo{{ID: 1}, {ID: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMarkDone(t *testing.T) {
	todos := []Todo{{ID: 1}, {ID: 2}}
	t2, ok := MarkDone(todos, 2)
	if !ok || !t2.Done {
		t.Errorf("MarkDone(2) ok=%v done=%v", ok, t2.Done)
	}
	if !todos[1].Done {
		t.Errorf("original slice not mutated")
	}
}

func TestMarkDoneNotFound(t *testing.T) {
	if _, ok := MarkDone([]Todo{{ID: 1}}, 99); ok {
		t.Error("expected ok=false for missing id")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "todos.json")
	original := []Todo{{ID: 1, Description: "first", Source: "main", Created: "2026-05-12T00:00:00Z"}}
	if err := Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded := Load(path)
	if !reflect.DeepEqual(loaded, original) {
		t.Errorf("round-trip mismatch: got %v want %v", loaded, original)
	}
}

func TestSaveCreatesGitignore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "todos.json")
	if err := Save(path, []Todo{{ID: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gi := readGitignore(t, dir)
	if !strings.Contains(gi, "todos.json") {
		t.Errorf(".gitignore missing todos.json: %q", gi)
	}
}

func TestSaveGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "todos.json")
	for range 3 {
		if err := Save(path, []Todo{{ID: 1}}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	gi := readGitignore(t, dir)
	if strings.Count(gi, "todos.json") != 1 {
		t.Errorf(".gitignore should mention todos.json exactly once: %q", gi)
	}
}

func readGitignore(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	return string(b)
}
