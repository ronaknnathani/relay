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
		Worktree:       "/tmp/wt",
		SystemPrompt:   "Active relay project: demo. Phase: plan. Mode: full.",
		SessionName:    "relay:demo",
		Command:        "plan",
		CommandArgs:    "demo",
		PermissionMode: "default",
	}

	tests := []struct {
		name string
		opts LaunchOptions
		want []string
	}{
		{
			name: "default mode (no permission flag)",
			opts: base,
			want: []string{
				"--append-system-prompt", "Active relay project: demo. Phase: plan. Mode: full.",
				"-n", "relay:demo",
				"/plan demo",
			},
		},
		{
			name: "auto mode → acceptEdits",
			opts: func() LaunchOptions { o := base; o.PermissionMode = "auto"; return o }(),
			want: []string{
				"--permission-mode", "acceptEdits",
				"--append-system-prompt", "Active relay project: demo. Phase: plan. Mode: full.",
				"-n", "relay:demo",
				"/plan demo",
			},
		},
		{
			name: "empty mode falls back to auto",
			opts: func() LaunchOptions { o := base; o.PermissionMode = ""; return o }(),
			want: []string{
				"--permission-mode", "acceptEdits",
				"--append-system-prompt", "Active relay project: demo. Phase: plan. Mode: full.",
				"-n", "relay:demo",
				"/plan demo",
			},
		},
		{
			name: "bypass mode → dangerously-skip",
			opts: func() LaunchOptions { o := base; o.PermissionMode = "bypass"; return o }(),
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
		"--add-dir", "/tmp/proj",
		"--context", "long_context",
		"--allow-all",
		"-i", `Run the relay "plan" skill for slug demo.`,
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

	// prompt mode omits the allow-all flag so Copilot asks before acting.
	oPrompt := o
	oPrompt.PermissionMode = "prompt"
	for _, a := range (copilot{}).LaunchArgs(oPrompt) {
		if a == "--allow-all" {
			t.Errorf("prompt mode emitted %q; should ask for permissions", a)
		}
	}
	for _, a := range (copilot{}).LaunchArgs(o) {
		if a == "-p" || a == "--prompt" || a == "--no-ask-user" || a == "--plugin-dir" {
			t.Errorf("LaunchArgs emitted disallowed flag %q", a)
		}
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

func TestCodexLaunchArgs(t *testing.T) {
	o := LaunchOptions{
		Worktree:    "/tmp/wt",
		ProjectDir:  "/tmp/proj",
		SessionName: "relay:demo",
		Command:     "plan",
		CommandArgs: "demo",
	}
	want := []string{
		"-C", "/tmp/wt",
		"--add-dir", "/tmp/proj",
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
		`Run the relay "plan" skill for slug demo.`,
	}
	if got := (codex{}).LaunchArgs(o); !reflect.DeepEqual(got, want) {
		t.Errorf("LaunchArgs mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	oPrompt := o
	oPrompt.PermissionMode = "prompt"
	gotPrompt := (codex{}).LaunchArgs(oPrompt)
	if !slices.Contains(gotPrompt, "on-request") {
		t.Errorf("prompt mode args = %#v, want ask-for-approval on-request", gotPrompt)
	}

	oBypass := o
	oBypass.PermissionMode = "bypass"
	gotBypass := (codex{}).LaunchArgs(oBypass)
	if !slices.Contains(gotBypass, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("bypass mode args = %#v, want bypass flag", gotBypass)
	}

	o.ProjectDir = ""
	for _, a := range (codex{}).LaunchArgs(o) {
		if a == "--add-dir" {
			t.Error("LaunchArgs emitted --add-dir with no ProjectDir")
		}
		if a == "-n" {
			t.Error("LaunchArgs emitted unsupported Codex session-name flag")
		}
	}
}

func TestCodexPrepareWritesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	o := LaunchOptions{Worktree: dir, SystemPrompt: "Active relay project: demo. Phase: plan."}
	if err := (codex{}).Prepare(o); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(data), o.SystemPrompt) {
		t.Errorf("AGENTS.md missing context line: %q", data)
	}
}

func TestCodexCapabilities(t *testing.T) {
	c := (codex{}).Capabilities()
	if c.Subagents != SubagentToml {
		t.Errorf("Subagents = %v, want SubagentToml", c.Subagents)
	}
	if !c.LargeContext {
		t.Error("LargeContext = false, want true")
	}
	if c.DeterministicSlash {
		t.Error("DeterministicSlash = true, want false (prose invocation)")
	}
	if c.ContextInjection != ContextFile {
		t.Errorf("ContextInjection = %v, want ContextFile", c.ContextInjection)
	}
	if got := c.ToolNames.Name("AskUserQuestion"); got != "ask the user" {
		t.Errorf("AskUserQuestion tool name = %q, want ask the user", got)
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

func TestGetCodex(t *testing.T) {
	a, err := Get("codex")
	if err != nil {
		t.Fatalf("Get(codex): %v", err)
	}
	if a.Name() != "codex" {
		t.Errorf("Get(codex).Name() = %q, want codex", a.Name())
	}
}

func TestVerifyCopilotSkillsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	generated := filepath.Join(PackageDir("copilot"), "skills", "deliver-pr")
	installed := filepath.Join(home, ".copilot", "skills", "deliver-pr")
	if err := os.MkdirAll(generated, 0755); err != nil {
		t.Fatalf("mkdir generated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(generated, "SKILL.md"), []byte("# Deliver PR\n"), 0644); err != nil {
		t.Fatalf("write generated: %v", err)
	}
	err := VerifySkillsInstalled(copilot{}, "deliver-pr")
	if err == nil {
		t.Fatal("VerifySkillsInstalled missing copilot install: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Relay-managed workflows require `relay setup copilot`") {
		t.Fatalf("missing install error = %v, want relay setup hint", err)
	}
	if !strings.Contains(err.Error(), "`npx skills add ./skills-template` installs standalone skills only") {
		t.Fatalf("missing install error = %v, want standalone install distinction", err)
	}
	// A real (non-symlink) dir shadowing relay's skill must NOT satisfy the
	// command-skill check.
	if err := os.MkdirAll(installed, 0755); err != nil {
		t.Fatalf("mkdir installed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), []byte("# Deliver PR\n"), 0644); err != nil {
		t.Fatalf("write installed: %v", err)
	}
	err = VerifySkillsInstalled(copilot{}, "deliver-pr")
	if err == nil {
		t.Fatal("VerifySkillsInstalled shadowing real dir: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Relay-managed workflows require `relay setup copilot`") {
		t.Fatalf("shadowing skill error = %v, want relay setup hint", err)
	}
	if !strings.Contains(err.Error(), "`npx skills add ./skills-template` installs standalone skills only") {
		t.Fatalf("shadowing skill error = %v, want standalone install distinction", err)
	}
	// A relay-managed symlink (how `relay setup` installs skills) satisfies it.
	if err := os.RemoveAll(installed); err != nil {
		t.Fatalf("rm installed dir: %v", err)
	}
	if err := os.Symlink(generated, installed); err != nil {
		t.Fatalf("symlink installed: %v", err)
	}
	if err := VerifySkillsInstalled(copilot{}, "deliver-pr"); err != nil {
		t.Fatalf("VerifySkillsInstalled: %v", err)
	}
	if err := VerifySkillsInstalled(copilot{}, "missing"); err == nil {
		t.Fatal("VerifySkillsInstalled missing skill: expected error, got nil")
	}
}

func TestVerifyCodexSkillsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	generated := filepath.Join(PackageDir("codex"), "skills", "deliver-pr")
	installed := filepath.Join(home, ".codex", "skills", "deliver-pr")
	if err := os.MkdirAll(generated, 0755); err != nil {
		t.Fatalf("mkdir generated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(generated, "SKILL.md"), []byte("# Deliver PR\n"), 0644); err != nil {
		t.Fatalf("write generated: %v", err)
	}
	err := VerifySkillsInstalled(codex{}, "deliver-pr")
	if err == nil {
		t.Fatal("VerifySkillsInstalled missing codex install: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Relay-managed workflows require `relay setup codex`") {
		t.Fatalf("missing install error = %v, want relay setup hint", err)
	}
	if !strings.Contains(err.Error(), "`npx skills add ./skills-template` installs standalone skills only") {
		t.Fatalf("missing install error = %v, want standalone install distinction", err)
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir installed parent: %v", err)
	}
	if err := os.Symlink(generated, installed); err != nil {
		t.Fatalf("symlink installed: %v", err)
	}
	if err := VerifySkillsInstalled(codex{}, "deliver-pr"); err != nil {
		t.Fatalf("VerifySkillsInstalled: %v", err)
	}
}

func TestInstallErrorMentionsSetup(t *testing.T) {
	err := installError("copilot", os.ErrPermission)
	if !strings.Contains(err.Error(), "Relay-managed workflows require `relay setup copilot`") {
		t.Fatalf("installError = %v, want relay setup hint", err)
	}
	if !strings.Contains(err.Error(), "`npx skills add ./skills-template` installs standalone skills only") {
		t.Fatalf("installError = %v, want standalone install distinction", err)
	}
}

func TestVerifyClaudeSkillsInstalledChecksGeneratedSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	generated := filepath.Join(PackageDir("claude"), "skills", "deliver-pr")
	installed := filepath.Join(home, ".claude", "skills", "deliver-pr")
	if err := os.MkdirAll(generated, 0755); err != nil {
		t.Fatalf("mkdir generated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(generated, "SKILL.md"), []byte("# Deliver PR\n"), 0644); err != nil {
		t.Fatalf("write generated: %v", err)
	}
	if err := VerifySkillsInstalled(claude{}, "deliver-pr"); err == nil {
		t.Fatal("VerifySkillsInstalled missing claude install: expected error, got nil")
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir installed parent: %v", err)
	}
	if err := os.Symlink(generated, installed); err != nil {
		t.Fatalf("symlink installed: %v", err)
	}
	if err := VerifySkillsInstalled(claude{}, "deliver-pr"); err != nil {
		t.Fatalf("VerifySkillsInstalled: %v", err)
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
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("error %q should list supported agents", err)
	}
}

func TestNames(t *testing.T) {
	names := Names()
	if !slices.Contains(names, "claude") {
		t.Errorf("Names() = %v, want it to contain claude", names)
	}
	if !slices.Contains(names, "codex") {
		t.Errorf("Names() = %v, want it to contain codex", names)
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
