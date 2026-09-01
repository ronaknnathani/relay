package role_test

import (
	"testing"

	"github.com/ronaknnathani/relay/internal/role"
)

func TestCanonicalIdentitiesAreTL(t *testing.T) {
	if role.TL != "tl" {
		t.Errorf("role.TL = %q, want %q", role.TL, "tl")
	}
	if role.AutomatedTLPrefix != "tl-automated:" {
		t.Errorf("role.AutomatedTLPrefix = %q, want %q", role.AutomatedTLPrefix, "tl-automated:")
	}
	if role.LegacyCTO != "cto" {
		t.Errorf("role.LegacyCTO = %q, want %q", role.LegacyCTO, "cto")
	}
	if role.LegacyAutomatedCTOPrefix != "cto-automated:" {
		t.Errorf("role.LegacyAutomatedCTOPrefix = %q, want %q", role.LegacyAutomatedCTOPrefix, "cto-automated:")
	}
}

func TestNormalizeIdentity(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "legacy exact identity", value: "cto", want: "tl"},
		{name: "canonical identity", value: "tl", want: "tl"},
		{name: "legacy automated identity", value: "cto-automated:3f2504e0", want: "tl-automated:3f2504e0"},
		{name: "canonical automated identity", value: "tl-automated:3f2504e0", want: "tl-automated:3f2504e0"},
		{name: "legacy automated unknown session", value: "cto-automated:unknown", want: "tl-automated:unknown"},
		{name: "other actor", value: "ceo", want: "ceo"},
		{name: "worker actor", value: "worker", want: "worker"},
		{name: "empty", value: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := role.NormalizeIdentity(test.value); got != test.want {
				t.Errorf("NormalizeIdentity(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// Normalization replaces exact identities only. Free text that merely mentions
// the retired role, or a name that happens to contain it, must survive intact:
// a decision answer or a username is content, not an identity.
func TestNormalizeIdentityLeavesFreeTextAndNamesUnchanged(t *testing.T) {
	unchanged := []string{
		"CTO",
		"Cto",
		"cto ",
		" cto",
		"cto-approved",
		"ctocorp",
		"octopus",
		"director",
		"the cto approved this",
		"cto-automated",
		"cto-automated:",
		"cto-automated:BAD-CHARS",
		"cto-automated:0123456789012345678901234567890123",
		"acto-automated:3f2504e0",
		"cto-automated:3f2504e0 extra",
	}
	for _, value := range unchanged {
		if got := role.NormalizeIdentity(value); got != value {
			t.Errorf("NormalizeIdentity(%q) = %q, want it unchanged", value, got)
		}
	}
}

func TestIsAutomatedPredicates(t *testing.T) {
	if !role.IsAutomated("tl-automated:3f2504e0") {
		t.Error("IsAutomated(canonical automated) = false, want true")
	}
	if !role.IsAutomated("cto-automated:3f2504e0") {
		t.Error("IsAutomated(legacy automated) = false, want true")
	}
	if role.IsAutomated("tl") || role.IsAutomated("ceo") || role.IsAutomated("tl-automated:") {
		t.Error("IsAutomated matched a value that does not name a bounded automated turn")
	}
}

func TestIsCanonicalRejectsLegacyIdentities(t *testing.T) {
	canonical := []string{"tl", "tl-automated:3f2504e0", "ceo", "worker", "rnathani"}
	for _, value := range canonical {
		if !role.IsCanonical(value) {
			t.Errorf("IsCanonical(%q) = false, want true", value)
		}
	}
	legacy := []string{"cto", "cto-automated:3f2504e0"}
	for _, value := range legacy {
		if role.IsCanonical(value) {
			t.Errorf("IsCanonical(%q) = true, want false", value)
		}
	}
}
