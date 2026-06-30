// Package todo manages the repo-local todo list stored at
// <repo>/.claude/todos.json.
package todo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/ui"
)

type Todo struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Created     string `json:"created"`
	Done        bool   `json:"done"`
}

// FilePath returns the on-disk path for the current repo's todo list.
func FilePath() (string, error) {
	root, err := gitx.RepoRoot()
	if err != nil {
		return "", fmt.Errorf("locate repo root: %w", err)
	}
	if root == "" {
		return "", fmt.Errorf("not in a git repository")
	}
	return filepath.Join(root, ".claude", "todos.json"), nil
}

// Load reads the todo list from disk. Missing or unparseable files yield
// an empty list with no error (matches existing behavior — a fresh repo
// has no todo file yet).
func Load(path string) []Todo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var todos []Todo
	if err := json.Unmarshal(data, &todos); err != nil {
		return nil
	}
	return todos
}

// Save writes the todo list to disk, creating the .claude directory if
// needed, and ensures .claude/.gitignore excludes todos.json.
func Save(path string, todos []Todo) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(todos, "", "  ")
	if err != nil {
		return fmt.Errorf("encode todos: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write todos: %w", err)
	}
	if err := ensureGitignore(dir); err != nil {
		ui.Warn("could not update .gitignore: %s", err)
	}
	return nil
}

func ensureGitignore(dotClaudeDir string) error {
	gi := filepath.Join(dotClaudeDir, ".gitignore")
	data, _ := os.ReadFile(gi)
	if strings.Contains(string(data), "todos.json") {
		return nil
	}
	f, err := os.OpenFile(gi, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := f.WriteString("todos.json\n"); err != nil {
		return err
	}
	return nil
}

// NextID returns the next free numeric ID for a new todo.
func NextID(todos []Todo) int {
	highest := 0
	for _, t := range todos {
		highest = max(highest, t.ID)
	}
	return highest + 1
}

// Pending returns the subset of todos that are not yet done, preserving order.
func Pending(todos []Todo) []Todo {
	out := make([]Todo, 0, len(todos))
	for _, t := range todos {
		if !t.Done {
			out = append(out, t)
		}
	}
	return out
}

// MarkDone flips the Done flag for the todo with the given id. Returns
// the matched todo (a copy) and true if found.
func MarkDone(todos []Todo, id int) (Todo, bool) {
	for i := range todos {
		if todos[i].ID == id {
			todos[i].Done = true
			return todos[i], true
		}
	}
	return Todo{}, false
}
