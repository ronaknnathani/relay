package programview

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/program"
)

func TestDisplayIdentityPrefersGoalHeadingAndFirstParagraph(t *testing.T) {
	goal := "<!-- notes -->\n\n# Workload MP\n\nA multiproduct that runs scheduled workloads on shared\ncompute so teams stop hand-rolling cron jobs.\n\n- not the summary\n\n## Architecture\n"
	title, summary := displayIdentity("Build a workload multiproduct. It should do many things.", goal)

	if title != "Workload MP" {
		t.Errorf("display title = %q, want the goal.md H1", title)
	}
	want := "A multiproduct that runs scheduled workloads on shared compute so teams stop hand-rolling cron jobs."
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

func TestDisplayIdentityFallsBackToShortenedProgramTitle(t *testing.T) {
	longTitle := "Redesign the whole workload platform so that every team can schedule batch work. " +
		"Also migrate the legacy scheduler and delete it afterwards."
	title, summary := displayIdentity(longTitle, "")

	if title == longTitle {
		t.Error("display title must not repeat the raw program prompt")
	}
	if len([]rune(title)) > displayTitleLimit+1 {
		t.Errorf("display title %q is longer than the clamp", title)
	}
	if title != "Redesign the whole workload platform so that every team can schedule…" {
		t.Errorf("display title = %q, want the clamped first sentence", title)
	}
	if summary != "" {
		t.Errorf("summary = %q, want empty without goal.md prose", summary)
	}
}

func TestDisplayIdentitySkipsCodeListsAndQuotesWhenSummarizing(t *testing.T) {
	goal := "# Relay V1\n\n```\nnot prose\n```\n\n> quoted\n\n1. numbered\n\nRelay coordinates dependent pull requests for one program.\n"
	title, summary := displayIdentity("Relay V1", goal)

	if title != "Relay V1" {
		t.Errorf("display title = %q", title)
	}
	if summary != "Relay coordinates dependent pull requests for one program." {
		t.Errorf("summary = %q, want the first prose paragraph", summary)
	}
}

func TestDisplayIdentityHandlesEmptyProgram(t *testing.T) {
	title, summary := displayIdentity("", "")
	if title != "" || summary != "" {
		t.Errorf("displayIdentity(\"\", \"\") = %q, %q, want empty strings", title, summary)
	}
}

func TestBuildExposesDisplayTitleAndSummaryFromGoal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	at := "2026-08-25T16:00:00Z"
	p := program.Program{
		Revision:            1,
		Slug:                "workload-mp",
		Title:               "Create a workload multiproduct with schedulers and quotas. Ship it in stages.",
		Repo:                repo,
		State:               program.StateActive,
		Agent:               "copilot",
		MaxOpenPRs:          3,
		CreatedAt:           at,
		UpdatedAt:           at,
		ApprovalRequestedAt: at,
		ApprovedAt:          at,
		ApprovedBy:          "ceo",
		Items:               []program.WorkItem{},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	writeTestFile(t, filepath.Join(programDir, "goal.md"),
		"# Workload MP\n\nOne place to schedule shared batch work.\n\n## Architecture\n\nDetails.\n")

	got, err := Build(p.Slug, Options{Now: func() time.Time { return time.Date(2026, 8, 25, 17, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if got.Program.DisplayTitle != "Workload MP" {
		t.Errorf("display_title = %q, want the goal.md H1", got.Program.DisplayTitle)
	}
	if got.Program.Summary != "One place to schedule shared batch work." {
		t.Errorf("summary = %q, want the first goal.md paragraph", got.Program.Summary)
	}
	if got.Program.Title != p.Title {
		t.Errorf("title = %q, want the durable program title preserved", got.Program.Title)
	}
}

func TestBuildFallsBackToShortTitleWhenGoalHasNoHeading(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	at := "2026-08-25T16:00:00Z"
	p := program.Program{
		Revision:            1,
		Slug:                "no-heading",
		Title:               "Ship the thing",
		Repo:                repo,
		State:               program.StateActive,
		Agent:               "copilot",
		MaxOpenPRs:          3,
		CreatedAt:           at,
		UpdatedAt:           at,
		ApprovalRequestedAt: at,
		ApprovedAt:          at,
		ApprovedBy:          "ceo",
		Items:               []program.WorkItem{},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	got, err := Build(p.Slug, Options{Now: func() time.Time { return time.Date(2026, 8, 25, 17, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if got.Program.DisplayTitle != "Ship the thing" {
		t.Errorf("display_title = %q, want the program title fallback", got.Program.DisplayTitle)
	}
	if got.Program.Summary != "" {
		t.Errorf("summary = %q, want empty without goal.md", got.Program.Summary)
	}
}
