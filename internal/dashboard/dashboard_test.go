package dashboard

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

func TestMarshalNilSlicesEmitEmptyArrays(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	out := Marshal(nil, nil, now)
	var got Data
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Active == nil || got.Archived == nil {
		t.Errorf("expected empty (non-nil) slices, got Active=%v Archived=%v", got.Active, got.Archived)
	}
	if got.Generated != "2026-05-12T12:00:00Z" {
		t.Errorf("Generated = %q, want 2026-05-12T12:00:00Z", got.Generated)
	}
}

func TestMarshalIncludesManifests(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	active := []project.Manifest{{Slug: "a", Phase: "plan"}}
	out := Marshal(active, nil, now)
	var got Data
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Active) != 1 || got.Active[0].Slug != "a" {
		t.Errorf("expected one active manifest with slug 'a', got %+v", got.Active)
	}
}
