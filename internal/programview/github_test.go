package programview

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestPullRequestNumber(t *testing.T) {
	tests := []struct {
		ref  string
		want int
		ok   bool
	}{
		{ref: "#123", want: 123, ok: true},
		{ref: "https://github.com/acme/widgets/pull/456", want: 456, ok: true},
		{ref: "https://github.example/acme/widgets/pull/789/files", want: 789, ok: true},
		{ref: "#0"},
		{ref: "#12x"},
		{ref: "https://github.com/acme/widgets/issues/123"},
		{ref: "https://github.com/acme/widgets/pull/not-a-number"},
	}
	for _, test := range tests {
		t.Run(test.ref, func(t *testing.T) {
			got, ok := PullRequestNumber(test.ref)
			if got != test.want || ok != test.ok {
				t.Fatalf("PullRequestNumber(%q) = (%d, %t), want (%d, %t)", test.ref, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestGHPRIndexLoaderQueriesOnlyRecordedReferences(t *testing.T) {
	var mutex sync.Mutex
	commands := [][]string{}
	loader := ghPRIndexLoader{
		hasOrigin: func(string) bool { return true },
		lookPath:  func(string) (string, error) { return "/usr/bin/gh", nil },
		run: func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
			mutex.Lock()
			commands = append(commands, append([]string{dir, name}, args...))
			mutex.Unlock()
			switch args[2] {
			case "#7":
				return []byte(`{"number":7,"state":"MERGED","url":"https://github.example/acme/repo/pull/7"}`), nil
			case "https://github.example/acme/repo/pull/2048":
				return []byte(`{"number":2048,"state":"CLOSED","url":"https://github.example/acme/repo/pull/2048"}`), nil
			default:
				return nil, fmt.Errorf("unexpected ref %q", args[2])
			}
		},
		cache: newPRStateCache(time.Minute, time.Now),
	}

	index := loader.Load("/repo", []string{"#7", "https://github.example/acme/repo/pull/2048", "#7", "  "})
	if index == nil {
		t.Fatal("index = nil, want resolved recorded references")
	}
	if len(commands) != 2 {
		t.Fatalf("gh commands = %#v, want one per unique reference", commands)
	}
	for _, command := range commands {
		if command[0] != "/repo" || command[1] != "gh" || command[2] != "pr" || command[3] != "view" {
			t.Fatalf("command = %#v, want a direct pr view", command)
		}
		if slices.Contains(command, "list") || slices.Contains(command, "--limit") {
			t.Fatalf("command %#v still lists the whole repository", command)
		}
		if command[len(command)-1] != "number,state,url" {
			t.Fatalf("command = %#v, want minimal JSON fields", command)
		}
	}
	tests := []struct {
		ref   string
		state PRState
		found bool
	}{
		{ref: "#7", state: PRStateMerged, found: true},
		{ref: "https://github.example/acme/repo/pull/7", state: PRStateMerged, found: true},
		{ref: "https://github.example/acme/repo/pull/2048", state: PRStateClosed, found: true},
		{ref: "#2048", state: PRStateClosed, found: true},
		{ref: "#99"},
	}
	for _, test := range tests {
		state, found := index.Lookup(test.ref)
		if state != test.state || found != test.found {
			t.Fatalf("Lookup(%q) = (%q, %t), want (%q, %t)", test.ref, state, found, test.state, test.found)
		}
	}
}

func TestGHPRIndexLoaderReusesCachedStateWithinTTL(t *testing.T) {
	runs := 0
	now := time.Unix(1000, 0)
	loader := ghPRIndexLoader{
		hasOrigin: func(string) bool { return true },
		lookPath:  func(string) (string, error) { return "/usr/bin/gh", nil },
		run: func(context.Context, string, string, ...string) ([]byte, error) {
			runs++
			return []byte(`{"number":7,"state":"OPEN","url":"https://github.example/acme/repo/pull/7"}`), nil
		},
		cache: newPRStateCache(30*time.Second, func() time.Time { return now }),
	}

	for range 3 {
		index := loader.Load("/repo", []string{"#7"})
		if state, found := index.Lookup("#7"); !found || state != PRStateOpen {
			t.Fatalf("cached lookup = (%q, %t)", state, found)
		}
	}
	if runs != 1 {
		t.Fatalf("gh runs within the TTL = %d, want 1", runs)
	}

	now = now.Add(31 * time.Second)
	if index := loader.Load("/repo", []string{"#7"}); index == nil {
		t.Fatal("index = nil after TTL expiry")
	}
	if runs != 2 {
		t.Fatalf("gh runs after the TTL = %d, want 2", runs)
	}

	if index := loader.Load("/other-repo", []string{"#7"}); index == nil {
		t.Fatal("index = nil for a second repository")
	}
	if runs != 3 {
		t.Fatalf("gh runs across repositories = %d, want 3", runs)
	}
}

func TestGHPRIndexLoaderBoundsConcurrentSubprocesses(t *testing.T) {
	var mutex sync.Mutex
	inFlight := 0
	peak := 0
	refs := make([]string, 0, 12)
	for number := 1; number <= 12; number++ {
		refs = append(refs, fmt.Sprintf("#%d", number))
	}
	loader := ghPRIndexLoader{
		hasOrigin: func(string) bool { return true },
		lookPath:  func(string) (string, error) { return "/usr/bin/gh", nil },
		run: func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
			mutex.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mutex.Unlock()
			time.Sleep(time.Millisecond)
			mutex.Lock()
			inFlight--
			mutex.Unlock()
			number, _ := PullRequestNumber(args[2])
			return fmt.Appendf(nil,
				`{"number":%d,"state":"OPEN","url":"https://github.example/acme/repo/pull/%d"}`,
				number, number), nil
		},
		cache:       newPRStateCache(time.Minute, time.Now),
		parallelism: 4,
	}

	index := loader.Load("/repo", refs)
	if index == nil {
		t.Fatal("index = nil")
	}
	if peak > 4 {
		t.Fatalf("peak concurrent gh subprocesses = %d, want at most 4", peak)
	}
	for _, ref := range refs {
		if _, found := index.Lookup(ref); !found {
			t.Fatalf("Lookup(%q) missing", ref)
		}
	}
}

func TestGHPRIndexLoaderFallsBackConservatively(t *testing.T) {
	tests := []struct {
		name      string
		refs      []string
		hasOrigin bool
		lookErr   error
		runErr    error
		output    string
		wantRuns  int
	}{
		{name: "no recorded refs", hasOrigin: true},
		{name: "no origin", refs: []string{"#12"}},
		{name: "gh unavailable", refs: []string{"#12"}, hasOrigin: true, lookErr: exec.ErrNotFound},
		{
			name: "view error", refs: []string{"#12"}, hasOrigin: true,
			runErr: errors.New("authentication failed"), wantRuns: 1,
		},
		{name: "malformed JSON", refs: []string{"#12"}, hasOrigin: true, output: "{", wantRuns: 1},
		{
			name: "unknown state", refs: []string{"#12"}, hasOrigin: true,
			output: `{"number":12,"state":"MYSTERY","url":"https://github.example/pull/12"}`, wantRuns: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runs := 0
			output := test.output
			if output == "" {
				output = `{"number":12,"state":"OPEN","url":"https://github.example/pull/12"}`
			}
			loader := ghPRIndexLoader{
				hasOrigin: func(string) bool { return test.hasOrigin },
				lookPath:  func(string) (string, error) { return "/usr/bin/gh", test.lookErr },
				run: func(context.Context, string, string, ...string) ([]byte, error) {
					runs++
					return []byte(output), test.runErr
				},
				cache: newPRStateCache(time.Minute, time.Now),
			}
			if index := loader.Load("/repo", test.refs); index != nil {
				t.Fatalf("index = %#v, want conservative nil fallback", index)
			}
			if runs != test.wantRuns {
				t.Fatalf("gh runs = %d, want %d", runs, test.wantRuns)
			}
		})
	}
}

func TestGHFetcherFetchesAndNormalizesChecks(t *testing.T) {
	var gotDir, gotName string
	var gotArgs []string
	fetcher := NewGHFetcherWithRunner(func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		gotDir, gotName, gotArgs = dir, name, append([]string(nil), args...)
		return []byte(`{
			"number":42,
			"url":"https://github.example/pr/42",
			"state":"OPEN",
			"isDraft":false,
			"mergeable":"MERGEABLE",
			"reviewDecision":"APPROVED",
			"statusCheckRollup":[
				{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},
				{"__typename":"StatusContext","state":"SUCCESS"}
			],
			"title":"Ship it",
			"updatedAt":"2026-08-25T16:00:00Z"
		}`), nil
	})

	got, err := fetcher.Fetch(context.Background(), "/repo", "#42")
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"pr", "view", "#42", "--json",
		"number,url,state,isDraft,mergeable,reviewDecision,statusCheckRollup,title,updatedAt",
	}
	if gotDir != "/repo" || gotName != "gh" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command = dir %q name %q args %v", gotDir, gotName, gotArgs)
	}
	if got.Number != 42 || got.State != "open" || got.Checks != "passing" {
		t.Fatalf("pull request = %+v", got)
	}
}

func TestNormalizeChecks(t *testing.T) {
	tests := []struct {
		name   string
		checks []map[string]any
		want   string
	}{
		{name: "none", checks: []map[string]any{}, want: "none"},
		{name: "failing", checks: []map[string]any{{"conclusion": "FAILURE"}}, want: "failing"},
		{name: "pending", checks: []map[string]any{{"status": "IN_PROGRESS"}}, want: "pending"},
		{name: "unknown", checks: []map[string]any{{"state": "MYSTERY"}}, want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeChecks(test.checks); got != test.want {
				t.Fatalf("normalizeChecks() = %q, want %q", got, test.want)
			}
		})
	}
}
