package program

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestStoreRoundTripAndDoubleCreate(t *testing.T) {
	home := setTestHome(t)
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	if err := Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Create(p); err == nil {
		t.Fatal("second Create succeeded")
	}
	path := ManifestPath(ActiveDir(), p.Slug)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || !strings.Contains(string(data), "\n  \"slug\"") {
		t.Fatalf("manifest is not indented with trailing newline:\n%s", data)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.UpdatedAt == "" {
		t.Fatal("UpdatedAt is empty")
	}
	p.UpdatedAt = loaded.UpdatedAt
	if !reflect.DeepEqual(loaded, p) {
		t.Fatalf("round trip mismatch:\n got: %+v\nwant: %+v", loaded, p)
	}
	found, err := Find(p.Slug)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found != path {
		t.Fatalf("Find = %s, want %s", found, path)
	}
	if want := filepath.Join(home, ".relay", "programs", "active", p.Slug, "program.json"); path != want {
		t.Fatalf("manifest path = %s, want %s", path, want)
	}
	loadedItem, ok := loaded.Item(item.ID)
	if !ok || loadedItem.Title != "change" {
		t.Fatalf("loaded item = %+v, %v", loadedItem, ok)
	}
}

func TestSaveIsAtomicAndValidates(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	if err := Create(p); err != nil {
		t.Fatal(err)
	}
	path := ManifestPath(ActiveDir(), p.Slug)
	p.Title = "updated"
	if err := Save(path, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p.Title = ""
	if err := Save(path, p); err == nil {
		t.Fatal("invalid Save succeeded")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "updated" {
		t.Fatalf("saved title = %q", loaded.Title)
	}
}

func TestSaveRejectsStaleRevision(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	if p.Revision != 1 {
		t.Fatalf("new program revision = %d, want 1", p.Revision)
	}
	if err := Create(p); err != nil {
		t.Fatal(err)
	}
	path := ManifestPath(ActiveDir(), p.Slug)
	first, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	first.Title = "first writer"
	second.Title = "second writer"
	if err := Save(path, first); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := Save(path, second); err == nil || !strings.Contains(err.Error(), "stale program revision") {
		t.Fatalf("second Save error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "first writer" || loaded.Revision != 2 {
		t.Fatalf("saved program = %+v", loaded)
	}
}

func TestSaveRejectsExistingLock(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	if err := Create(p); err != nil {
		t.Fatal(err)
	}
	path := ManifestPath(ActiveDir(), p.Slug)
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("busy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Title = "must not save"
	err := Save(path, p)
	if err == nil || !strings.Contains(err.Error(), "save lock") || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("Save lock error = %v", err)
	}
	loaded, loadErr := Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.Title == p.Title {
		t.Fatal("Save overwrote program while lock existed")
	}
}

func TestLegacyRevisionZeroLoadsAndAdvances(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	p.Revision = 0
	path := ManifestPath(ActiveDir(), p.Slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := encodeProgram(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Revision != 0 {
		t.Fatalf("legacy revision = %d", legacy.Revision)
	}
	legacy.Title = "migrated"
	if err := Save(path, legacy); err != nil {
		t.Fatal(err)
	}
	advanced, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Revision != 1 {
		t.Fatalf("advanced revision = %d, want 1", advanced.Revision)
	}
}

func TestArchiveAndLoadAll(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	if err := Create(p); err != nil {
		t.Fatal(err)
	}
	if err := Archive(p.Slug); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := os.Stat(ManifestPath(ActiveDir(), p.Slug)); !os.IsNotExist(err) {
		t.Fatalf("active manifest stat error = %v", err)
	}
	if _, err := os.Stat(ManifestPath(ArchivedDir(), p.Slug)); err != nil {
		t.Fatalf("archived manifest: %v", err)
	}
	found, err := Find(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if found != ManifestPath(ArchivedDir(), p.Slug) {
		t.Fatalf("Find archived = %s", found)
	}
	active, err := LoadAll(ActiveDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active programs = %v", active)
	}
	archived, err := LoadAll(ArchivedDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].Slug != p.Slug {
		t.Fatalf("archived programs = %+v", archived)
	}
}

func TestLoadRejectsCorruptAndInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(corrupt); err == nil || !strings.Contains(err.Error(), "parse program") {
		t.Fatalf("corrupt Load error = %v", err)
	}

	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(invalid); err == nil || !strings.Contains(err.Error(), "validate program") {
		t.Fatalf("invalid Load error = %v", err)
	}
}

func TestAppendLogs(t *testing.T) {
	dir := t.TempDir()
	progress := ProgressPath(dir)
	decisions := DecisionLogPath(dir)
	if err := AppendProgress(progress, "started w1"); err != nil {
		t.Fatal(err)
	}
	if err := AppendDecisionLog(decisions, "d1 approved"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		progress:  "started w1",
		decisions: "d1 approved",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s = %q", path, data)
		}
	}
}
