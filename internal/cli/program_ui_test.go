package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programui"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestProgramViewCompatibilityWrappers(t *testing.T) {
	repo := t.TempDir()
	manifest := project.Manifest{Slug: "child", Repo: repo, Branch: "feature"}
	if _, err := activeProjectView(manifest); err != nil {
		t.Fatal(err)
	}
	hasPR, ref, err := projectPR(manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if hasPR || ref != "" {
		t.Fatalf("recorded PR = %t %q", hasPR, ref)
	}
}

func TestProgramUICommandPassesOpenFlagsAndAllowsArchivedPrograms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := program.New("relay-v1", "Relay V1", filepath.Join(home, "repo"), "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := program.Archive(p.Slug); err != nil {
		t.Fatal(err)
	}

	originalServe := serveProgramUI
	t.Cleanup(func() { serveProgramUI = originalServe })
	var got programui.Options
	serveProgramUI = func(_ context.Context, options programui.Options) error {
		got = options
		return nil
	}

	command := newRootCmd()
	command.SetArgs([]string{"program", "ui", p.Slug, "--port", "0", "--no-open"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.Slug != p.Slug || got.Port != 0 || got.Open {
		t.Fatalf("server options = %+v", got)
	}
}

func TestProgramUICommandOpensByDefaultAndRejectsUnknownSlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := program.New("relay-v1", "Relay V1", filepath.Join(home, "repo"), "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	originalServe := serveProgramUI
	t.Cleanup(func() { serveProgramUI = originalServe })
	calls := 0
	serveProgramUI = func(_ context.Context, options programui.Options) error {
		calls++
		if !options.Open {
			t.Error("program UI did not open by default")
		}
		return nil
	}
	command := newRootCmd()
	command.SetArgs([]string{"program", "ui", p.Slug})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	command = newRootCmd()
	command.SetArgs([]string{"program", "ui", "missing"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err == nil {
		t.Fatal("unknown program unexpectedly started UI")
	}
	if calls != 1 {
		t.Fatalf("serve calls = %d, want 1", calls)
	}
}
