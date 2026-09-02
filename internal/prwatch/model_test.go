package prwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestFingerprintCoversSortedUniqueKeysOnly(t *testing.T) {
	items := []Item{
		{Key: "comment:2:2026-01-02T00:00:00Z", Body: "second body"},
		{Key: "check:build:12345:failure", Body: "first body"},
		{Key: "comment:2:2026-01-02T00:00:00Z", Body: "duplicate key, different body"},
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"check:build:12345:failure",
		"comment:2:2026-01-02T00:00:00Z",
	}, "\n")))
	want := hex.EncodeToString(sum[:])

	if got := Fingerprint(items); got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
	reordered := []Item{items[1], items[0]}
	if got := Fingerprint(reordered); got != want {
		t.Errorf("Fingerprint of reordered items = %q, want %q", got, want)
	}
}

func TestFingerprintIgnoresBodies(t *testing.T) {
	base := []Item{{Key: "comment:7:2026-03-04T05:06:07Z", Body: "please rename this"}}
	other := []Item{{Key: "comment:7:2026-03-04T05:06:07Z", Body: "totally different text"}}

	if Fingerprint(base) != Fingerprint(other) {
		t.Error("Fingerprint changed when only a body changed")
	}
}

func TestFingerprintOfNoActionableItemsIsEmpty(t *testing.T) {
	if got := Fingerprint(nil); got != "" {
		t.Errorf("Fingerprint(nil) = %q, want empty", got)
	}
	if got := Fingerprint([]Item{}); got != "" {
		t.Errorf("Fingerprint(empty) = %q, want empty", got)
	}
}

func TestValidateFingerprint(t *testing.T) {
	valid := Fingerprint([]Item{{Key: "check:build:1:failure"}})
	for _, test := range []struct {
		name        string
		fingerprint string
		wantErr     bool
	}{
		{name: "computed", fingerprint: valid},
		{name: "empty", fingerprint: "", wantErr: true},
		{name: "uppercase", fingerprint: strings.ToUpper(valid), wantErr: true},
		{name: "short", fingerprint: valid[:63], wantErr: true},
		{name: "long", fingerprint: valid + "a", wantErr: true},
		{name: "path traversal", fingerprint: "../" + valid[3:], wantErr: true},
		{name: "non hex", fingerprint: strings.Repeat("z", 64), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateFingerprint(test.fingerprint)
			if test.wantErr && err == nil {
				t.Errorf("ValidateFingerprint(%q) = nil, want error", test.fingerprint)
			}
			if !test.wantErr && err != nil {
				t.Errorf("ValidateFingerprint(%q) = %v, want nil", test.fingerprint, err)
			}
		})
	}
}

func TestCadenceForScheduledCheck(t *testing.T) {
	want := []time.Duration{
		FastCadence, FastCadence, FastCadence, FastCadence,
		MediumCadence, MediumCadence,
		SlowCadence, SlowCadence,
	}
	for i, expected := range want {
		check := i + 1
		if got := CadenceFor(check); got != expected {
			t.Errorf("CadenceFor(%d) = %s, want %s", check, got, expected)
		}
	}
}

func TestParseMode(t *testing.T) {
	for _, value := range []string{"standalone", "managed", "stack"} {
		mode, err := ParseMode(value)
		if err != nil {
			t.Fatalf("ParseMode(%q) = %v", value, err)
		}
		if string(mode) != value {
			t.Errorf("ParseMode(%q) = %q", value, mode)
		}
	}
	if _, err := ParseMode("Standalone"); err == nil {
		t.Error("ParseMode(\"Standalone\") = nil error, want rejection")
	}
	if _, err := ParseMode(""); err == nil {
		t.Error("ParseMode(\"\") = nil error, want rejection")
	}
}
