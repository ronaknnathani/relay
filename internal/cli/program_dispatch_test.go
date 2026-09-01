package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func createDispatchProgram(t *testing.T, slug string, maxOpenPRs int) (program.Program, program.WorkItem, program.Contract) {
	t.Helper()
	installManagedHerdrFakes(t, nil)
	repo := newTestRepo(t)
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New(slug, "Ship governed changes", repoRoot, "copilot", maxOpenPRs)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	dependency, err := p.AddItem(program.WorkItem{Title: "Foundation", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(dependency.ID, "foundation"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Reconcile([]program.ProjectView{{
		Slug:   "foundation",
		Repo:   repoRoot,
		Merged: true,
	}}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repoRoot, "architecture.md")
	if err := os.WriteFile(source, []byte("# Binding architecture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	programDir := program.ProgramDir(program.ActiveDir(), slug)
	contract, err := p.PublishContract(programDir, "architecture", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ApproveContract(contract.Ref, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{
		Title:        "Build managed dispatch",
		Priority:     program.PriorityP1,
		Dependencies: []string{dependency.ID},
		ContractRefs: []string{contract.Ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	return p, item, contract
}

func TestProgramDispatchCreatesManagedChildWithoutLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERDR_ENV", "")
	saveProgramTestConfig(t)
	p, item, contract := createDispatchProgram(t, "governance", 3)

	launched := false
	previousLaunch := launchAgent
	launchAgent = func(agent.Agent, agent.LaunchOptions) error {
		launched = true
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	out, err := runProgramCommand(t, "dispatch", p.Slug, item.ID)
	if err != nil {
		t.Fatalf("program dispatch: %v", err)
	}
	if launched {
		t.Fatal("default dispatch launched an agent")
	}
	childSlug := "governance-" + item.ID
	if !strings.Contains(out, "relay program worker start "+p.Slug+" "+item.ID) {
		t.Fatalf("dispatch output = %q", out)
	}
	if strings.Contains(out, "relay resume "+childSlug) {
		t.Fatalf("dispatch output still offers a non-Herdr fallback: %q", out)
	}

	childDir := filepath.Join(project.ActiveDir(), childSlug)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), childSlug))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Title != item.Title || manifest.Workflow != "deliver-pr" ||
		manifest.Program != p.Slug || manifest.ProgramItem != item.ID ||
		manifest.Repo != p.Repo || manifest.Worktree == nil {
		t.Fatalf("child manifest = %+v", manifest)
	}
	if _, err := os.Stat(*manifest.Worktree); err != nil {
		t.Fatalf("child worktree: %v", err)
	}
	if !strings.HasSuffix(manifest.Branch, childSlug) {
		t.Fatalf("child branch = %q", manifest.Branch)
	}

	source, err := os.ReadFile(filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), filepath.FromSlash(contract.Path)))
	if err != nil {
		t.Fatal(err)
	}
	copiedPath := filepath.Join(childDir, filepath.FromSlash(contract.Path))
	copied, err := os.ReadFile(copiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copied, source) {
		t.Fatalf("copied contract = %q, want %q", copied, source)
	}
	assignment, err := os.ReadFile(filepath.Join(childDir, "assignment.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		p.Title, item.Title, string(item.Priority), "w1", contract.Ref, contract.SHA256,
		filepath.ToSlash(contract.Path), "relay program can-open-pr governance " + item.ID,
		"relay program message outbox governance " + item.ID + " --json",
		"relay program message send governance " + item.ID + ` --kind question --body "<describe the issue and requested decision>"`,
		"relay program message send governance " + item.ID + ` --kind conflict --body "<describe the conflict, impact, and requested decision>"`,
		"relay program message send governance " + item.ID + ` --kind plan --body "<describe the plan and requested review>"`,
		"relay program message send governance " + item.ID + ` --kind pr-open --body "<request an open-PR capacity grant>"`,
		"Do not send another pr-open request while one is unread",
		"acknowledge the grant inbox message only after open-pr succeeds",
		"clarify", "plan", "tech lead", "CEO",
	} {
		if !strings.Contains(string(assignment), want) {
			t.Errorf("assignment missing %q:\n%s", want, assignment)
		}
	}
	if strings.Contains(string(assignment), "program decision open") {
		t.Fatalf("assignment allows worker program state writes:\n%s", assignment)
	}
	for _, relative := range []string{
		"mail/inbox",
		"mail/outbox",
		"mail/processed/inbox",
		"mail/processed/outbox",
	} {
		info, err := os.Stat(filepath.Join(childDir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("mailbox directory %s: %v", relative, err)
		}
		if !info.IsDir() {
			t.Fatalf("mailbox path %s is not a directory", relative)
		}
	}

	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	gotItem, _ := loaded.Item(item.ID)
	if gotItem.Status != program.ItemDispatched || gotItem.ProjectSlug != childSlug {
		t.Fatalf("dispatched item = %+v", gotItem)
	}
	progress, err := os.ReadFile(program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(progress), "Dispatched item "+item.ID+" to project "+childSlug) {
		t.Fatalf("progress = %q", progress)
	}
}

func TestProgramDispatchInsideHerdrPrintsExplicitWorkerStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	saveProgramTestConfig(t)
	p, item, _ := createDispatchProgram(t, "governance", 3)

	out, err := runProgramCommand(t, "dispatch", p.Slug, item.ID)
	if err != nil {
		t.Fatalf("program dispatch: %v", err)
	}
	want := "relay program worker start " + p.Slug + " " + item.ID
	if !strings.Contains(out, want) {
		t.Fatalf("dispatch output %q missing %q", out, want)
	}
	if strings.Contains(out, "relay resume ") {
		t.Fatalf("dispatch output includes direct resume: %q", out)
	}
}

func TestProgramDispatchLaunchesDeliverPRWithOverrideAfterPersistence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERDR_ENV", "1")
	saveProgramTestConfig(t)
	p, item, _ := createDispatchProgram(t, "governance", 3)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	p.Agent = "claude"
	if err := program.Save(path, p); err != nil {
		t.Fatal(err)
	}

	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
		loaded, err := program.Load(path)
		if err != nil {
			return err
		}
		loadedItem, _ := loaded.Item(item.ID)
		if loadedItem.Status != program.ItemDispatched {
			return os.ErrInvalid
		}
		if _, err := os.Stat(filepath.Join(project.ActiveDir(), "managed-child", "assignment.md")); err != nil {
			return err
		}
		got = options
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	if _, err := runProgramCommand(t, "dispatch", p.Slug, item.ID,
		"--name", "managed-child", "--agent", "copilot", "--launch"); err != nil {
		t.Fatalf("program dispatch --launch: %v", err)
	}
	childSlug := "managed-child"
	want := agent.LaunchOptions{
		Worktree:       filepath.Join(p.Repo, ".worktrees", "test_"+childSlug),
		ProjectDir:     filepath.Join(home, ".relay", "projects", "active", childSlug),
		SystemPrompt:   "Active relay project: " + childSlug + ". Workflow: deliver-pr. Mode: full.",
		SessionName:    "relay:" + childSlug,
		Command:        "deliver-pr",
		CommandArgs:    childSlug,
		WorkflowGoal:   item.Title,
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch options:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestProgramDispatchReusesPreLinkedExistingChild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	p, item, contract := createDispatchProgram(t, "governance", 3)
	childSlug := "existing-child"
	if err := p.LinkItem(item.ID, childSlug); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(p.Repo, ".worktrees", "existing-child")
	if err := project.Save(project.ManifestPath(project.ActiveDir(), childSlug), project.Manifest{
		Slug: childSlug, Title: item.Title, Repo: p.Repo, Branch: "test/existing-child",
		Agent: "copilot", Workflow: "deliver-pr", Worktree: &worktree,
	}); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), filepath.FromSlash(contract.Path)))
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(projectDir, filepath.FromSlash(contract.Path))
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, source, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "assignment.md"), []byte("stale assignment\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runProgramCommand(t, "dispatch", p.Slug, item.ID); err != nil {
		t.Fatalf("dispatch pre-linked child: %v", err)
	}
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), childSlug))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Program != p.Slug || manifest.ProgramItem != item.ID {
		t.Fatalf("child association = %q/%q", manifest.Program, manifest.ProgramItem)
	}
	assignment, err := os.ReadFile(filepath.Join(projectDir, "assignment.md"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(assignment, []byte("stale assignment\n")) || !bytes.Contains(assignment, []byte(item.Title)) {
		t.Fatalf("assignment was not rewritten: %q", assignment)
	}
	for _, relative := range []string{
		"mail/inbox",
		"mail/outbox",
		"mail/processed/inbox",
		"mail/processed/outbox",
	} {
		if info, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(relative))); err != nil || !info.IsDir() {
			t.Fatalf("reused child mailbox directory %s: %v", relative, err)
		}
	}
	copied, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied, source) {
		t.Fatalf("verified contract snapshot changed: %q", copied)
	}
}

func TestProgramDispatchRejectsConflictingNameWithoutSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	p, item, _ := createDispatchProgram(t, "governance", 3)
	if err := p.LinkItem(item.ID, "linked-child"); err != nil {
		t.Fatal(err)
	}
	programPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	if err := program.Save(programPath, p); err != nil {
		t.Fatal(err)
	}
	programBefore, err := os.ReadFile(programPath)
	if err != nil {
		t.Fatal(err)
	}
	branchesBefore := gitOutput(t, p.Repo, "branch", "--format=%(refname:short)")

	_, err = runProgramCommand(t, "dispatch", p.Slug, item.ID, "--name", "different-child")
	if err == nil || !strings.Contains(err.Error(), "already linked to project \"linked-child\"") {
		t.Fatalf("conflicting name error = %v", err)
	}
	programAfter, readErr := os.ReadFile(programPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(programBefore, programAfter) {
		t.Fatal("conflicting dispatch modified program state")
	}
	if branchesAfter := gitOutput(t, p.Repo, "branch", "--format=%(refname:short)"); branchesAfter != branchesBefore {
		t.Fatalf("conflicting dispatch changed branches:\nbefore: %s\nafter: %s", branchesBefore, branchesAfter)
	}
	for _, slug := range []string{"linked-child", "different-child"} {
		if _, statErr := os.Stat(project.ManifestPath(project.ActiveDir(), slug)); !os.IsNotExist(statErr) {
			t.Fatalf("conflicting dispatch created project %q: %v", slug, statErr)
		}
	}
}

func TestProgramDispatchRefusesInvalidGovernanceState(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (program.Program, program.WorkItem)
		want  string
	}{
		{
			name: "draft program",
			setup: func(t *testing.T) (program.Program, program.WorkItem) {
				p := createCLIProgram(t, "governance")
				item, err := p.AddItem(program.WorkItem{Title: "Draft work", Priority: program.PriorityP1})
				if err != nil {
					t.Fatal(err)
				}
				if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
					t.Fatal(err)
				}
				return p, item
			},
			want: "want active",
		},
		{
			name: "blocked item",
			setup: func(t *testing.T) (program.Program, program.WorkItem) {
				p, item, _ := createDispatchProgram(t, "governance", 3)
				if err := p.BlockItem(item.ID, "scope conflict"); err != nil {
					t.Fatal(err)
				}
				if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
					t.Fatal(err)
				}
				return p, item
			},
			want: "is not pending",
		},
		{
			name: "unapproved contract",
			setup: func(t *testing.T) (program.Program, program.WorkItem) {
				p := createCLIProgram(t, "governance")
				if err := p.Transition(program.StatePendingApproval, ""); err != nil {
					t.Fatal(err)
				}
				if err := p.Transition(program.StateActive, "ceo"); err != nil {
					t.Fatal(err)
				}
				source := filepath.Join(p.Repo, "pending.md")
				if err := os.WriteFile(source, []byte("pending\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				contract, err := p.PublishContract(program.ProgramDir(program.ActiveDir(), p.Slug), "pending", source)
				if err != nil {
					t.Fatal(err)
				}
				item, err := p.AddItem(program.WorkItem{
					Title: "Pending contract", Priority: program.PriorityP1, ContractRefs: []string{contract.Ref},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
					t.Fatal(err)
				}
				return p, item
			},
			want: "not ready",
		},
		{
			name: "tampered contract",
			setup: func(t *testing.T) (program.Program, program.WorkItem) {
				p, item, contract := createDispatchProgram(t, "governance", 3)
				contractPath := filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), filepath.FromSlash(contract.Path))
				if err := os.Chmod(contractPath, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(contractPath, []byte("tampered\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return p, item
			},
			want: "sha256 mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			installManagedHerdrFakes(t, nil)
			saveProgramTestConfig(t)
			p, item := tt.setup(t)
			_, err := runProgramCommand(t, "dispatch", p.Slug, item.ID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("dispatch error = %v, want %q", err, tt.want)
			}
			if _, err := os.Stat(project.ManifestPath(project.ActiveDir(), p.Slug+"-"+item.ID)); !os.IsNotExist(err) {
				t.Fatalf("dispatch refusal created child project: %v", err)
			}
		})
	}
}

func TestProgramDispatchIgnoresOpenPRCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	p, item, _ := createDispatchProgram(t, "governance", 3)
	for i := 1; i <= 3; i++ {
		number := i
		suffix := strconv.Itoa(i)
		openItem, err := p.AddItem(program.WorkItem{
			Title: "Open PR " + suffix, Priority: program.PriorityP1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.DispatchItem(openItem.ID, "open-pr-"+suffix); err != nil {
			t.Fatal(err)
		}
		saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
			Slug:   "open-pr-" + suffix,
			Repo:   p.Repo,
			Branch: "open-pr-" + suffix,
			PR:     project.PRInfo{Number: &number},
		})
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	if _, err := runProgramCommand(t, "dispatch", p.Slug, item.ID); err != nil {
		t.Fatalf("dispatch at PR capacity: %v", err)
	}
}

func TestProgramDispatchRepairFlowReusesRetainedChild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	p, item, _ := createDispatchProgram(t, "governance", 3)
	programPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	// An unusable save lock path makes the durable save fail after the child
	// project already exists, which is the state the repair flow must recover.
	lockPath := programPath + ".lock"
	if err := os.Mkdir(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runProgramCommand(t, "dispatch", p.Slug, item.ID)
	if err == nil {
		t.Fatal("dispatch succeeded despite an unusable program save lock")
	}
	childSlug := p.Slug + "-" + item.ID
	for _, want := range []string{
		"created and retained",
		"relay program item link " + p.Slug + " " + item.ID + " --project " + childSlug,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("dispatch error %q missing %q", err, want)
		}
	}
	manifest, loadErr := project.Load(project.ManifestPath(project.ActiveDir(), childSlug))
	if loadErr != nil {
		t.Fatalf("load retained child: %v", loadErr)
	}
	if manifest.Worktree == nil {
		t.Fatal("retained child has no worktree")
	}
	if _, statErr := os.Stat(*manifest.Worktree); statErr != nil {
		t.Fatalf("retained worktree: %v", statErr)
	}
	persisted, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	persistedItem, _ := persisted.Item(item.ID)
	if persistedItem.Status != program.ItemPending || persistedItem.ProjectSlug != "" {
		t.Fatalf("failed save changed persisted item: %+v", persistedItem)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "item", "link", p.Slug, item.ID, "--project", childSlug); err != nil {
		t.Fatalf("repair item link: %v", err)
	}
	if _, err := runProgramCommand(t, "dispatch", p.Slug, item.ID); err != nil {
		t.Fatalf("dispatch after repair: %v", err)
	}
	repaired, err := program.Load(programPath)
	if err != nil {
		t.Fatal(err)
	}
	repairedItem, _ := repaired.Item(item.ID)
	if repairedItem.Status != program.ItemDispatched || repairedItem.ProjectSlug != childSlug {
		t.Fatalf("repaired item = %+v", repairedItem)
	}
}

func TestDefaultDispatchSlugRetainsItemSuffix(t *testing.T) {
	got := defaultDispatchSlug("a-very-long-program-name-that-needs-shortening", "w123")
	if got != "a-very-long-program-name-that-needs-w123" {
		t.Fatalf("defaultDispatchSlug = %q", got)
	}
	if len(got) > 40 || !strings.HasSuffix(got, "-w123") {
		t.Fatalf("default dispatch slug is not safely shortened: %q", got)
	}
}
