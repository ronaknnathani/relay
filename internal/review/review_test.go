package review

import (
	"path/filepath"
	"testing"
)

func TestParseRef(t *testing.T) {
	nimbus := Ref{Host: "github.com", Owner: "linkedin-multiproduct", Repo: "nimbus", Number: 11268}
	tests := []struct {
		raw  string
		want Ref
	}{
		{"https://github.com/linkedin-multiproduct/nimbus/pull/11268", nimbus},
		{"https://github.com/linkedin-multiproduct/nimbus/pull/11268/files?w=1#diff-abc", nimbus},
		{"github.com/linkedin-multiproduct/nimbus/pull/11268/", nimbus},
		{"  linkedin-multiproduct/nimbus#11268 ", nimbus},
		{"https://GitHub.com/LinkedIn-MultiProduct/Nimbus/pull/11268", nimbus},
		{"https://ghe.example.com/team/svc/pull/3", Ref{Host: "ghe.example.com", Owner: "team", Repo: "svc", Number: 3}},
		{"https://ghe.example.com:8443/team/svc/pull/3", Ref{Host: "ghe.example.com:8443", Owner: "team", Repo: "svc", Number: 3}},
	}
	for _, tt := range tests {
		got, err := ParseRef(tt.raw)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}
}

func TestParseRefRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"the auth code",
		"https://github.com/acme/widget/issues/7",
		"https://github.com/acme/widget/pull/0",
		"https://github.com/acme/widget/pull/abc",
		"https://github.com/acme/widget",
		"https://github.com/../widget/pull/7",
		"ssh://github.com/acme/widget/pull/7",
		"acme/widget#-1",
	} {
		if got, err := ParseRef(raw); err == nil {
			t.Errorf("ParseRef(%q) = %+v, want error", raw, got)
		}
	}
}

func TestMatchesRemote(t *testing.T) {
	ref := Ref{Host: "github.com", Owner: "acme", Repo: "widget", Number: 1}
	matches := []string{
		"https://github.com/acme/widget.git",
		"https://github.com/Acme/Widget",
		"https://user@github.com/acme/widget/",
		"git@github.com:acme/widget.git",
		"ssh://git@github.com:22/acme/widget.git",
	}
	for _, remote := range matches {
		if !ref.MatchesRemote(remote) {
			t.Errorf("MatchesRemote(%q) = false, want true", remote)
		}
	}
	misses := []string{
		"https://ghe.example.com/acme/widget.git",
		"git@github.com:other/widget.git",
		"git@github.com:acme/widget-fork.git",
		"https://github.com/acme/widget/extra",
		"/tmp/acme/widget.git",
		"",
	}
	for _, remote := range misses {
		if ref.MatchesRemote(remote) {
			t.Errorf("MatchesRemote(%q) = true, want false", remote)
		}
	}
}

func TestMatchesRemoteIgnoresWebPort(t *testing.T) {
	ref := Ref{Host: "ghe.example.com:8443", Owner: "team", Repo: "svc", Number: 3}
	for _, remote := range []string{"git@ghe.example.com:team/svc.git", "https://ghe.example.com:8443/team/svc"} {
		if !ref.MatchesRemote(remote) {
			t.Errorf("MatchesRemote(%q) = false, want true", remote)
		}
	}
}

func TestSaveKeepsCreatedAndLoadReportsMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, found, err := Load(dir); err != nil || found {
		t.Fatalf("Load(missing) = found %v, err %v; want not found", found, err)
	}
	if err := Save(dir, Manifest{Number: 7, Created: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, found, err := Load(dir)
	if err != nil || !found {
		t.Fatalf("Load = found %v, err %v", found, err)
	}
	if got.Number != 7 || got.Created != "2020-01-01T00:00:00Z" || got.Updated == "" {
		t.Errorf("Load = %+v, want number 7, original created, and an updated stamp", got)
	}
}
