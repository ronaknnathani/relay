package patrol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePatrolState(t *testing.T, slug, content string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := RuntimeDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patrol.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A patrol record written before the tech-lead rename keeps its presence
// signal, so an upgrade never silently reports the live session as absent.
func TestReadStateAcceptsRetiredPresenceField(t *testing.T) {
	writePatrolState(t, "legacy", `{
  "schema": "relay.patrol.v1",
  "version": 1,
  "program_slug": "legacy",
  "status": "running",
  "reasons": [],
  "cto_present": true
}
`)
	state, err := ReadState("legacy")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !state.TLPresent {
		t.Error("tl_present = false, want the retired cto_present value")
	}
}

// An explicit retired false must stay false: pointer decoding is what makes an
// absent flag and a present-but-false flag distinguishable.
func TestReadStateKeepsRetiredPresenceFalse(t *testing.T) {
	writePatrolState(t, "legacy-false", `{
  "schema": "relay.patrol.v1",
  "program_slug": "legacy-false",
  "status": "running",
  "reasons": [],
  "cto_present": false
}
`)
	state, err := ReadState("legacy-false")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.TLPresent {
		t.Error("tl_present = true, want false")
	}
}

// When a record somehow carries both fields the canonical one wins.
func TestReadStatePrefersCanonicalPresenceOverRetired(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "canonical true beats retired false",
			body: `"tl_present": true, "cto_present": false`,
			want: true,
		},
		{
			name: "canonical false beats retired true",
			body: `"tl_present": false, "cto_present": true`,
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			slug := "both-fields"
			writePatrolState(t, slug, `{
  "schema": "relay.patrol.v1",
  "program_slug": "`+slug+`",
  "status": "running",
  "reasons": [],
  `+test.body+`
}
`)
			state, err := ReadState(slug)
			if err != nil {
				t.Fatalf("ReadState: %v", err)
			}
			if state.TLPresent != test.want {
				t.Errorf("tl_present = %t, want %t", state.TLPresent, test.want)
			}
		})
	}
}

// A record with neither field reads as absent rather than failing.
func TestReadStateWithoutPresenceFieldsReportsAbsent(t *testing.T) {
	writePatrolState(t, "no-presence", `{
  "schema": "relay.patrol.v1",
  "program_slug": "no-presence",
  "status": "running",
  "reasons": []
}
`)
	state, err := ReadState("no-presence")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.TLPresent {
		t.Error("tl_present = true, want false when neither field is present")
	}
}

// Decoding a legacy record must not rewrite it: only WriteState does that.
func TestReadStateDoesNotRewriteLegacyPatrolBytes(t *testing.T) {
	const content = `{
  "schema": "relay.patrol.v1",
  "program_slug": "legacy-bytes",
  "status": "running",
  "reasons": [],
  "cto_present": true
}
`
	writePatrolState(t, "legacy-bytes", content)
	if _, err := ReadState("legacy-bytes"); err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	data, err := os.ReadFile(StatePath("legacy-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("ReadState rewrote the record:\n%s", data)
	}
}

// New records emit only the canonical field.
func TestWriteStateEmitsOnlyCanonicalPresenceField(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteState(State{
		Schema: SchemaVersion, Version: 1, ProgramSlug: "canonical",
		Status: StatusRunning, Reasons: []Reason{}, TLPresent: true,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(StatePath("canonical"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cto_present") {
		t.Fatalf("patrol state still emits the retired field:\n%s", data)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if present, ok := raw["tl_present"].(bool); !ok || !present {
		t.Fatalf("tl_present JSON = %#v, want true", raw["tl_present"])
	}
}
