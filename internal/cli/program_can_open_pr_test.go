package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func createCanOpenPRProgram(t *testing.T, maxOpenPRs int) (program.Program, program.WorkItem) {
	t.Helper()
	p, item, _ := createDispatchProgram(t, "governance", maxOpenPRs)
	if err := p.DispatchItem(item.ID, "managed-child"); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	p, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:        "managed-child",
		Repo:        p.Repo,
		Branch:      "test/managed-child",
		Program:     p.Slug,
		ProgramItem: item.ID,
	})
	return p, item
}

func snapshotFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProgramCanOpenPRUsesItsReservationAtZeroAvailableAndIsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, item := createCanOpenPRProgram(t, 3)
	if err := p.GrantOpenPR(item.ID, "cto", nil); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
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
			Branch: "missing-open-branch-" + suffix,
			PR:     project.PRInfo{Number: &number},
		})
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:   "branch-without-pr",
		Repo:   p.Repo,
		Branch: "missing-no-pr-branch",
	})
	startSHA := gitOutput(t, p.Repo, "rev-parse", "HEAD")
	runProgramTestGit(t, p.Repo, "checkout", "-q", "-b", "merged-capacity-pr")
	if err := os.WriteFile(filepath.Join(p.Repo, "merged-capacity.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runProgramTestGit(t, p.Repo, "add", "merged-capacity.txt")
	runProgramTestGit(t, p.Repo, "commit", "-q", "-m", "merged capacity work")
	runProgramTestGit(t, p.Repo, "checkout", "-q", "main")
	runProgramTestGit(t, p.Repo, "merge", "-q", "--ff-only", "merged-capacity-pr")
	mergedPR := 8
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:       "merged-pr",
		Repo:       p.Repo,
		Branch:     "merged-capacity-pr",
		BaseBranch: "main",
		StartSHA:   startSHA,
		PR:         project.PRInfo{Number: &mergedPR},
	})
	differentRepoPR := 9
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug: "different-repo",
		Repo: "/different/repo",
		PR:   project.PRInfo{Number: &differentRepoPR},
	})
	runProgramTestGit(t, p.Repo, "update-ref", "refs/pull/77/head", "HEAD")
	before := snapshotFiles(t, filepath.Join(home, ".relay"))

	out, err := runProgramCommand(t, "can-open-pr", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("program can-open-pr: %v", err)
	}
	var got struct {
		Program  string           `json:"program"`
		Item     string           `json:"item"`
		Allowed  bool             `json:"allowed"`
		Capacity program.Capacity `json:"capacity"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	want := struct {
		Program  string           `json:"program"`
		Item     string           `json:"item"`
		Allowed  bool             `json:"allowed"`
		Capacity program.Capacity `json:"capacity"`
	}{
		Program: p.Slug,
		Item:    item.ID,
		Allowed: true,
		Capacity: program.Capacity{
			Limit:     3,
			Open:      2,
			Reserved:  1,
			Available: 0,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("can-open-pr output = %+v, want %+v", got, want)
	}
	after := snapshotFiles(t, filepath.Join(home, ".relay"))
	if !reflect.DeepEqual(before, after) {
		for path, data := range before {
			if !bytes.Equal(data, after[path]) {
				t.Errorf("can-open-pr modified %s", path)
			}
		}
		t.Fatal("can-open-pr modified Relay state")
	}
}

func TestProgramCanOpenPRRequiresGrantAndIsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, item := createCanOpenPRProgram(t, 3)
	before := snapshotFiles(t, filepath.Join(home, ".relay"))

	_, err := runProgramCommand(t, "can-open-pr", p.Slug, item.ID)
	if err == nil {
		t.Fatal("can-open-pr passed without a grant")
	}
	for _, want := range []string{
		"outstanding open-PR grant",
		"relay program message send " + p.Slug + " " + item.ID + " --kind pr-open",
		"stop",
	} {
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Errorf("grant error %q missing %q", err, want)
		}
	}
	after := snapshotFiles(t, filepath.Join(home, ".relay"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed can-open-pr modified Relay state")
	}
}

func TestProgramCanOpenPRAllowsExistingManagedPRAtCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, item := createCanOpenPRProgram(t, 3)
	for i := 1; i <= 2; i++ {
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
			Branch: "missing-open-branch-" + suffix,
			PR:     project.PRInfo{Number: &number},
		})
	}
	managedPR := 3
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:        "managed-child",
		Repo:        p.Repo,
		Branch:      "missing-managed-branch",
		Program:     p.Slug,
		ProgramItem: item.ID,
		PR:          project.PRInfo{Number: &managedPR},
	})
	if _, err := p.Reconcile([]program.ProjectView{{
		Slug:  "managed-child",
		Repo:  p.Repo,
		HasPR: true,
		PRRef: "#3",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	if _, err := runProgramCommand(t, "can-open-pr", p.Slug, item.ID); err != nil {
		t.Fatalf("existing managed PR should be adoptable at capacity: %v", err)
	}
}

func TestProgramCanOpenPRRejectsBlockedItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, item := createCanOpenPRProgram(t, 3)
	if err := p.BlockItem(item.ID, "paused risk"); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "can-open-pr", p.Slug, item.ID); err == nil ||
		!bytes.Contains([]byte(err.Error()), []byte("not dispatched or in-review")) {
		t.Fatalf("blocked can-open-pr error = %v", err)
	}
}

func TestProgramCanOpenPRRefusesDecisionsAndContractTamper(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *program.Program, program.WorkItem)
		want   string
	}{
		{
			name: "program decision",
			mutate: func(t *testing.T, p *program.Program, _ program.WorkItem) {
				if _, _, err := p.OpenDecision(program.Decision{
					Kind: program.DecisionQuestion, RaisedBy: program.RaisedByCTO, Question: "Proceed?",
				}); err != nil {
					t.Fatal(err)
				}
			},
			want: "unresolved program decision",
		},
		{
			name: "item decision",
			mutate: func(t *testing.T, p *program.Program, item program.WorkItem) {
				if _, _, err := p.OpenDecision(program.Decision{
					Kind: program.DecisionConflict, RaisedBy: program.RaisedByWorker,
					ItemID: item.ID, Question: "Scope conflict?",
				}); err != nil {
					t.Fatal(err)
				}
			},
			want: "unresolved item decision",
		},
		{
			name: "contract tamper",
			mutate: func(t *testing.T, p *program.Program, _ program.WorkItem) {
				contractPath := filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), filepath.FromSlash(p.Contracts[0].Path))
				if err := os.Chmod(contractPath, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(contractPath, []byte("tampered\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "sha256 mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			p, item := createCanOpenPRProgram(t, 3)
			tt.mutate(t, &p, item)
			if tt.name != "contract tamper" {
				if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
					t.Fatal(err)
				}
			}
			_, err := runProgramCommand(t, "can-open-pr", p.Slug, item.ID)
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(tt.want)) {
				t.Fatalf("can-open-pr error = %v, want %q", err, tt.want)
			}
		})
	}
}
