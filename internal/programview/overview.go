package programview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ronaknnathani/relay/internal/program"
)

// BuildOverview constructs a local-only cross-program snapshot.
func BuildOverview(
	programs []program.Program,
	discoveryDiagnostics []program.DiscoveryDiagnostic,
	now func() time.Time,
) OverviewSnapshot {
	if now == nil {
		now = time.Now
	}
	generatedAt := now().UTC()
	snapshot := OverviewSnapshot{
		Schema:      OverviewSchemaVersion,
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Refresh:     RefreshDTO{Status: "fresh"},
		Programs:    []ProgramOverviewDTO{},
		Work:        []OverviewWorkItemDTO{},
		Diagnostics: []OverviewDiagnosticDTO{},
	}
	for _, diagnostic := range discoveryDiagnostics {
		snapshot.Diagnostics = append(snapshot.Diagnostics, OverviewDiagnosticDTO{
			Directory: diagnostic.Directory,
			Message:   diagnostic.Err.Error(),
		})
	}

	sortedPrograms := append([]program.Program(nil), programs...)
	sort.Slice(sortedPrograms, func(i, j int) bool {
		return sortedPrograms[i].Slug < sortedPrograms[j].Slug
	})
	for _, current := range sortedPrograms {
		observed, plan, diagnostics := overviewProgramState(current, generatedAt)
		snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostics...)
		displayTitle, summary, diagnostic := overviewDisplayIdentity(current)
		if diagnostic != nil {
			snapshot.Diagnostics = append(snapshot.Diagnostics, *diagnostic)
		}
		snapshot.Programs = append(snapshot.Programs, ProgramOverviewDTO{
			Slug:          current.Slug,
			Title:         current.Title,
			DisplayTitle:  displayTitle,
			Summary:       summary,
			State:         string(current.State),
			UpdatedAt:     current.UpdatedAt,
			Progress:      progressDTO(observed.Items),
			Ready:         len(plan.Ready),
			InFlight:      len(plan.InFlight),
			Blocked:       len(plan.Blocked),
			OpenDecisions: len(plan.OpenDecisions),
			NextAction:    plan.NextAction,
		})
		appendOverviewWork(&snapshot, observed, plan, displayTitle)
	}
	sortOverviewWork(snapshot.Work)
	return snapshot
}

func overviewProgramState(
	current program.Program,
	generatedAt time.Time,
) (program.Program, program.View, []OverviewDiagnosticDTO) {
	views, warnings, err := projectViews(current, nil)
	diagnostics := make([]OverviewDiagnosticDTO, 0, len(warnings)+1)
	for _, warning := range warnings {
		diagnostics = append(diagnostics, OverviewDiagnosticDTO{
			Directory: current.Slug,
			Message:   warning.Message,
		})
	}
	if err != nil {
		diagnostics = append(diagnostics, OverviewDiagnosticDTO{
			Directory: current.Slug,
			Message:   fmt.Sprintf("load local project state: %v", err),
		})
		views = nil
	}
	observed, _, err := reconcileSnapshot(current, views, generatedAt)
	if err != nil {
		diagnostics = append(diagnostics, OverviewDiagnosticDTO{
			Directory: current.Slug,
			Message:   err.Error(),
		})
		observed = current
	}
	return observed, observed.Plan(views), diagnostics
}

func overviewDisplayIdentity(current program.Program) (string, string, *OverviewDiagnosticDTO) {
	path := filepath.Join(program.ProgramDir(program.ActiveDir(), current.Slug), "goal.md")
	data, err := os.ReadFile(path)
	if err == nil {
		title, summary := displayIdentity(current.Title, string(data))
		return title, summary, nil
	}
	title, summary := displayIdentity(current.Title, "")
	if errors.Is(err, os.ErrNotExist) {
		return title, summary, nil
	}
	return title, summary, &OverviewDiagnosticDTO{
		Directory: current.Slug,
		Message:   fmt.Sprintf("read program goal %s: %v", path, err),
	}
}

func appendOverviewWork(
	snapshot *OverviewSnapshot,
	current program.Program,
	plan program.View,
	displayTitle string,
) {
	reasons := make(map[string][]string, len(plan.Blocked))
	for _, blocked := range plan.Blocked {
		reasons[blocked.Item.ID] = append([]string(nil), blocked.Reasons...)
	}
	for _, item := range current.Items {
		switch item.Status {
		case program.ItemDispatched, program.ItemInReview, program.ItemBlocked:
			snapshot.Work = append(snapshot.Work, OverviewWorkItemDTO{
				ProgramSlug:  current.Slug,
				ProgramTitle: displayTitle,
				ID:           item.ID,
				Title:        item.Title,
				Priority:     string(item.Priority),
				Status:       string(item.Status),
				Reasons:      reasons[item.ID],
			})
		}
	}
}

func sortOverviewWork(work []OverviewWorkItemDTO) {
	sort.Slice(work, func(i, j int) bool {
		left, right := work[i], work[j]
		if overviewStatusRank(left.Status) != overviewStatusRank(right.Status) {
			return overviewStatusRank(left.Status) < overviewStatusRank(right.Status)
		}
		if overviewPriorityRank(left.Priority) != overviewPriorityRank(right.Priority) {
			return overviewPriorityRank(left.Priority) < overviewPriorityRank(right.Priority)
		}
		if left.ProgramSlug != right.ProgramSlug {
			return left.ProgramSlug < right.ProgramSlug
		}
		return numberedID(left.ID) < numberedID(right.ID)
	})
}

func overviewStatusRank(status string) int {
	switch program.ItemStatus(status) {
	case program.ItemDispatched:
		return 0
	case program.ItemInReview:
		return 1
	default:
		return 2
	}
}

func overviewPriorityRank(priority string) int {
	switch program.Priority(priority) {
	case program.PriorityP0:
		return 0
	case program.PriorityP1:
		return 1
	case program.PriorityP2:
		return 2
	default:
		return 3
	}
}
