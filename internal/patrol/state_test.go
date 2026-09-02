package patrol

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestStateRoundTripKeepsArraysNonNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := State{
		Schema:      SchemaVersion,
		Version:     1,
		ProgramSlug: "adaptive",
		Status:      StatusRunning,
		Reasons:     []Reason{},
	}
	if err := WriteState(want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(want.ProgramSlug)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state:\n got: %#v\nwant: %#v", got, want)
	}
	data, err := os.ReadFile(StatePath(want.ProgramSlug))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["reasons"].([]any); !ok {
		t.Fatalf("reasons JSON = %#v, want array", raw["reasons"])
	}
}
