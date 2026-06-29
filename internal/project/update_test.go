package project

import (
	"reflect"
	"testing"
)

func TestApplySet(t *testing.T) {
	m := &Manifest{PhasesCompleted: []string{"init", "plan"}}
	if err := ApplySet(m, "phases_completed=plan,discuss"); err != nil {
		t.Fatalf("ApplySet: %v", err)
	}
	want := []string{"plan", "discuss"}
	if !reflect.DeepEqual(m.PhasesCompleted, want) {
		t.Errorf("got %v, want %v", m.PhasesCompleted, want)
	}
}

func TestApplySetEmpty(t *testing.T) {
	m := &Manifest{PhasesCompleted: []string{"init"}}
	if err := ApplySet(m, "phases_completed="); err != nil {
		t.Fatalf("ApplySet: %v", err)
	}
	if len(m.PhasesCompleted) != 0 {
		t.Errorf("expected empty slice, got %v", m.PhasesCompleted)
	}
}

func TestApplyAdd(t *testing.T) {
	m := &Manifest{PhasesRemaining: []string{"plan", "discuss"}}
	if err := ApplyAdd(m, "phases_remaining=implement"); err != nil {
		t.Fatalf("ApplyAdd: %v", err)
	}
	want := []string{"plan", "discuss", "implement"}
	if !reflect.DeepEqual(m.PhasesRemaining, want) {
		t.Errorf("got %v, want %v", m.PhasesRemaining, want)
	}
}

func TestApplyAddDuplicate(t *testing.T) {
	m := &Manifest{PhasesRemaining: []string{"plan"}}
	if err := ApplyAdd(m, "phases_remaining=plan"); err != nil {
		t.Fatalf("ApplyAdd: %v", err)
	}
	if len(m.PhasesRemaining) != 1 {
		t.Errorf("expected no duplicate, got %v", m.PhasesRemaining)
	}
}

func TestApplyRemove(t *testing.T) {
	m := &Manifest{PhasesRemaining: []string{"plan", "discuss", "implement"}}
	if err := ApplyRemove(m, "phases_remaining=discuss"); err != nil {
		t.Fatalf("ApplyRemove: %v", err)
	}
	want := []string{"plan", "implement"}
	if !reflect.DeepEqual(m.PhasesRemaining, want) {
		t.Errorf("got %v, want %v", m.PhasesRemaining, want)
	}
}

func TestApplyUnknownField(t *testing.T) {
	m := &Manifest{}
	if err := ApplySet(m, "bogus=x"); err == nil {
		t.Error("expected error for unknown field")
	}
}

func TestParseFieldValueMissingEquals(t *testing.T) {
	if _, _, err := ParseFieldValue("noequals"); err == nil {
		t.Error("expected error for missing =")
	}
}
