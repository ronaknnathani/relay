package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestClaudeLaunchArgs(t *testing.T) {
	base := LaunchOptions{
		Worktree:     "/tmp/wt",
		SystemPrompt: "Active relay project: demo. Phase: plan. Mode: full.",
		SessionName:  "relay:demo",
		Command:      "plan",
		CommandArgs:  "demo",
	}

	tests := []struct {
		name string
		opts LaunchOptions
		want []string
	}{
		{
			name: "without skip permissions",
			opts: base,
			want: []string{
				"--append-system-prompt", "Active relay project: demo. Phase: plan. Mode: full.",
				"-n", "relay:demo",
				"/plan demo",
			},
		},
		{
			name: "with skip permissions",
			opts: func() LaunchOptions { o := base; o.SkipPermissions = true; return o }(),
			want: []string{
				"--dangerously-skip-permissions",
				"--append-system-prompt", "Active relay project: demo. Phase: plan. Mode: full.",
				"-n", "relay:demo",
				"/plan demo",
			},
		},
		{
			name: "empty command args",
			opts: func() LaunchOptions { o := base; o.CommandArgs = ""; return o }(),
			want: []string{
				"--append-system-prompt", "Active relay project: demo. Phase: plan. Mode: full.",
				"-n", "relay:demo",
				"/plan",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claude{}.LaunchArgs(tt.opts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LaunchArgs mismatch:\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestClaudePrepareIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := (claude{}).Prepare(LaunchOptions{Worktree: dir}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Prepare wrote files into %s: %v", filepath.Base(dir), entries)
	}
}

func TestClaudeCapabilities(t *testing.T) {
	want := Capabilities{
		Subagents:          SubagentTask,
		LargeContext:       true,
		DeterministicSlash: true,
		LifecycleHook:      HookNone,
		ContextInjection:   ContextFlag,
		ToolNames:          nil,
		PermissionFlag:     "--dangerously-skip-permissions",
	}
	if got := (claude{}).Capabilities(); !reflect.DeepEqual(got, want) {
		t.Errorf("Capabilities mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCopilotLaunchArgs(t *testing.T) {
	o := LaunchOptions{
		Worktree:    "/tmp/wt",
		ProjectDir:  "/tmp/proj",
		SessionName: "relay:demo",
		Command:     "plan",
		CommandArgs: "demo",
	}
	want := []string{
		"-C", "/tmp/wt",
		"-n", "relay:demo",
		"--plugin-dir", PackageDir("copilot"),
		"--add-dir", "/tmp/proj",
		"--context", "long_context",
		"--allow-all-tools",
		"--no-ask-user",
		"-p", `Run the relay "plan" skill for slug demo.`,
	}
	if got := (copilot{}).LaunchArgs(o); !reflect.DeepEqual(got, want) {
		t.Errorf("LaunchArgs mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	// No ProjectDir → no --add-dir flag is emitted.
	o3 := o
	o3.ProjectDir = ""
	for _, a := range (copilot{}).LaunchArgs(o3) {
		if a == "--add-dir" {
			t.Error("LaunchArgs emitted --add-dir with no ProjectDir")
		}
	}

	// No args → prose omits the slug clause but still names the skill.
	o2 := o
	o2.CommandArgs = ""
	got := (copilot{}).LaunchArgs(o2)
	if got[len(got)-1] != `Run the relay "plan" skill.` {
		t.Errorf("empty-args prompt = %q, want skill-named prose", got[len(got)-1])
	}
}

func TestCopilotPrepareWritesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	o := LaunchOptions{Worktree: dir, SystemPrompt: "Active relay project: demo. Phase: plan."}
	if err := (copilot{}).Prepare(o); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(data), o.SystemPrompt) {
		t.Errorf("AGENTS.md missing context line: %q", data)
	}
	if !strings.HasPrefix(string(data), "# relay") {
		t.Errorf("AGENTS.md missing relay heading: %q", data)
	}
}

func TestCopilotPrepareExcludesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0755); err != nil {
		t.Fatalf("mkdir .git/info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("# existing\n"), 0644); err != nil {
		t.Fatalf("seed exclude: %v", err)
	}
	o := LaunchOptions{Worktree: dir, SystemPrompt: "ctx"}
	if err := (copilot{}).Prepare(o); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Idempotent: a second Prepare must not duplicate the entry.
	if err := (copilot{}).Prepare(o); err != nil {
		t.Fatalf("Prepare (2): %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if n := strings.Count(string(data), "AGENTS.md"); n != 1 {
		t.Errorf("exclude has AGENTS.md %d times, want 1: %q", n, data)
	}
}

func TestCopilotCapabilities(t *testing.T) {
	c := (copilot{}).Capabilities()
	if c.Subagents != SubagentTask {
		t.Errorf("Subagents = %v, want SubagentTask", c.Subagents)
	}
	if !c.LargeContext {
		t.Error("LargeContext = false, want true")
	}
	if c.DeterministicSlash {
		t.Error("DeterministicSlash = true, want false (prose invocation)")
	}
	if c.LifecycleHook != HookNone {
		t.Errorf("LifecycleHook = %v, want HookNone", c.LifecycleHook)
	}
	if c.ContextInjection != ContextFile {
		t.Errorf("ContextInjection = %v, want ContextFile", c.ContextInjection)
	}
	if c.PermissionFlag != "--allow-all-tools" {
		t.Errorf("PermissionFlag = %q, want --allow-all-tools", c.PermissionFlag)
	}
	wantTools := map[string]string{
		"Bash": "bash", "Read": "view", "Write": "create", "Edit": "edit",
		"Glob": "glob", "Grep": "grep", "Agent": "task",
		"WebFetch": "web_fetch", "AskUserQuestion": "ask_user",
	}
	for canon, name := range wantTools {
		if got := c.ToolNames.Name(canon); got != name {
			t.Errorf("ToolNames[%q] = %q, want %q", canon, got, name)
		}
	}
}

func TestGetCopilot(t *testing.T) {
	a, err := Get("copilot")
	if err != nil {
		t.Fatalf("Get(copilot): %v", err)
	}
	if a.Name() != "copilot" {
		t.Errorf("Get(copilot).Name() = %q, want copilot", a.Name())
	}
}

func TestToolNameMap(t *testing.T) {
	m := ToolNameMap{"Read": "view"}
	if got := m.Name("Read"); got != "view" {
		t.Errorf("Name(Read) = %q, want view", got)
	}
	if got := m.Name("Bash"); got != "Bash" {
		t.Errorf("Name(Bash) = %q, want Bash (unmapped passthrough)", got)
	}
	var nilMap ToolNameMap
	if got := nilMap.Name("Read"); got != "Read" {
		t.Errorf("nil map Name(Read) = %q, want Read", got)
	}
}

func TestGet(t *testing.T) {
	for _, name := range []string{"", "claude"} {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		if a.Name() != "claude" {
			t.Errorf("Get(%q) returned %q, want claude", name, a.Name())
		}
	}

	_, err := Get("nope")
	if err == nil {
		t.Fatal("Get(\"nope\"): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error %q should list supported agents", err)
	}
}

func TestNames(t *testing.T) {
	names := Names()
	if !slices.Contains(names, "claude") {
		t.Errorf("Names() = %v, want it to contain claude", names)
	}
}

func TestResolveName(t *testing.T) {
	cases := []struct {
		name                           string
		requested, manifest, configDef string
		want                           string
	}{
		{"requested wins", "copilot", "codex", "claude", "copilot"},
		{"manifest when no request", "", "codex", "claude", "codex"},
		{"config default when neither", "", "", "claude", "claude"},
		{"empty when all empty", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveName(c.requested, c.manifest, c.configDef); got != c.want {
				t.Errorf("ResolveName(%q,%q,%q) = %q, want %q", c.requested, c.manifest, c.configDef, got, c.want)
			}
		})
	}
}
