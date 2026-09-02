package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tech lead skill and program guide describe the focusless live-pane
// doorbell implemented by the patrol and worker notification paths.
func TestProgramDocsDescribeLiveTLDoorbells(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: filepath.Join("skills", "tl", "SKILL.md"),
			required: []string{
				"payload-free doorbell to this exact live pane",
				"terminal-session control stream",
				"never focuses the pane",
				"another tech lead session",
				"suppresses all further doorbells",
			},
			forbidden: []string{
				"/loop", "/every", "program worker prompt",
				"fresh, bounded tech lead-role session",
				"RELAY_AUTOMATED_TURN=1",
			},
		},
		{
			path: filepath.Join("docs", "programs.md"),
			required: []string{
				"Live tech lead doorbells",
				"herdr agent prompt <tl-pane>",
				"herdr terminal session control <tl-pane> --takeover",
				`{"type":"terminal.input","bytes":"DQ=="}`,
				"still idle after the grace period",
				"current dimensions",
				"currently focused",
				"records an `uncertain` wake",
				"duplicated tech leads",
				"sorted unread worker-outbox message ids",
			},
			forbidden: []string{
				"relay program tl turn <slug>",
				"fresh, bounded, same-role",
				"RELAY_AUTOMATED_TURN_SESSION_ID",
			},
		},
		{
			path: "README.md",
			required: []string{
				"existing idle tech lead pane",
				"changing the user's focus",
			},
			forbidden: []string{"bounded tech lead-role session", "verified headless turn"},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, test.path))
			if err != nil {
				t.Fatal(err)
			}
			body := string(data)
			for _, want := range test.required {
				if !strings.Contains(body, want) {
					t.Errorf("%s is missing %q", test.path, want)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s still contains retired transport text %q", test.path, forbidden)
				}
			}
		})
	}
}

// The patrol pane is where the CEO watches the patrol work, and a wake is an
// instruction to run the next governance action rather than a status ping. Both
// have to be documented where the tech lead and the operator will look.
func TestProgramDocsDescribeVisiblePatrolEventsAndWakeFollowThrough(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: filepath.Join("skills", "tl", "SKILL.md"),
			required: []string{
				"relay-patrol:$PROGRAM",
				"program tick",
				"dispatch",
				"start or adopt",
			},
			forbidden: []string{"patrol log file", "log_path"},
		},
		{
			path: filepath.Join("docs", "programs.md"),
			required: []string{
				"START program=",
				"TICK  cadence=",
				"WAKE  TL delivered",
				"next=01:00:00",
				"stderr",
				"never written to a file",
			},
			forbidden: []string{
				"patrol log file", "log_path", "next tick at=", "tick reasons=",
			},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, test.path))
			if err != nil {
				t.Fatal(err)
			}
			body := string(data)
			for _, want := range test.required {
				if !strings.Contains(body, want) {
					t.Errorf("%s is missing %q", test.path, want)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s documents a retired patrol surface: %q", test.path, forbidden)
				}
			}
		})
	}
}

// A CEO change to a pull request that already exists, and the retirement of a
// merged item's runtime, are both destructive if routed by hand. The tech lead
// skill and the program guide must name the exact commands, the state that
// decides the route, and the force-archive policy.
func TestProgramDocsDescribeChangeRoutingAndMergedCleanup(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: filepath.Join("skills", "tl", "SKILL.md"),
			required: []string{
				"relay program worker request-change",
				"never by messaging the worker yourself",
				"Open and unapproved",
				"in GitHub's merge queue",
				"pending follow-up work item",
				"relay program worker cleanup",
				"merged-worker-cleanup:<item>",
				"relay archive <child-project-slug> --force",
				"discards dirty and untracked files",
				"only ever accepts an item Relay records as `merged`",
				// A watcher that merged its pull request exits on its own and
				// keeps its tab. The tech lead has to know that a stopped
				// watcher is not a retired one and that cleanup closes it.
				"**A watcher that already stopped still needs this command.**",
				"it never closes its own tab",
				"Cleanup's first step is what closes it.",
				"recorded tab and pane ids are cleared",
				"Never conclude the item is",
				// Local output, UTC state.
				"Everything Relay stores stays UTC",
				"compare timestamps from `--json`",
			},
			forbidden: []string{
				"message the worker first to see",
			},
		},
		{
			path: filepath.Join("docs", "programs.md"),
			required: []string{
				"relay program worker request-change",
				"`mergeStateStatus == QUEUED`",
				"auto-merge merely armed",
				"follow_up_of",
				"request_hash",
				"never rolls the item back",
				"relay program worker cleanup",
				"Stop the child pull request watcher",
				"End the item's one worker session",
				"Close that exact tab",
				"Archive the child project",
				"deliberately destructive",
				"merged-worker-cleanup:<item>",
				"A completed watcher is the normal case",
				"This command is what closes that tab.",
				"recorded tab and pane ids are cleared",
				"stamped in the host's local zone with the UTC offset spelled out",
				"only text a person reads is translated",
				"`--json` returns the stored UTC record unchanged",
			},
		},
		{
			path: filepath.Join("skills", "deliver-pr", "SKILL.md"),
			required: []string{
				"make the change on the branch and pull request you already have",
				"sends `/exit`",
				"force-removed",
			},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, test.path))
			if err != nil {
				t.Fatal(err)
			}
			body := string(data)
			for _, want := range test.required {
				if !strings.Contains(body, want) {
					t.Errorf("%s is missing %q", test.path, want)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s still contains %q", test.path, forbidden)
				}
			}
		})
	}
}
