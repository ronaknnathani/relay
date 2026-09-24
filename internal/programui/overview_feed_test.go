package programui

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

func TestOverviewFeedReplacesRetainsAndRecovers(t *testing.T) {
	now := time.Date(2026, 9, 24, 17, 0, 0, 0, time.UTC)
	var phase atomic.Int32
	feed := newOverviewFeed(
		programview.OverviewSnapshot{
			Schema:      programview.OverviewSchemaVersion,
			Programs:    []programview.ProgramOverviewDTO{{Slug: "initial"}},
			Work:        []programview.OverviewWorkItemDTO{},
			Diagnostics: []programview.OverviewDiagnosticDTO{},
		},
		time.Hour,
		func() time.Time { return now },
		func() (programview.OverviewSnapshot, error) {
			switch phase.Load() {
			case 0:
				return programview.OverviewSnapshot{
					Schema:      programview.OverviewSchemaVersion,
					Programs:    []programview.ProgramOverviewDTO{{Slug: "fresh"}},
					Work:        []programview.OverviewWorkItemDTO{},
					Diagnostics: []programview.OverviewDiagnosticDTO{},
				}, nil
			case 1:
				return programview.OverviewSnapshot{}, errors.New("storage unavailable")
			default:
				return programview.OverviewSnapshot{
					Schema:      programview.OverviewSchemaVersion,
					Programs:    []programview.ProgramOverviewDTO{{Slug: "recovered"}},
					Work:        []programview.OverviewWorkItemDTO{},
					Diagnostics: []programview.OverviewDiagnosticDTO{},
				}, nil
			}
		},
	)

	feed.Refresh()
	eventually(t, time.Second, func() bool {
		return feed.Get().Programs[0].Slug == "fresh"
	})

	phase.Store(1)
	feed.Refresh()
	eventually(t, time.Second, func() bool {
		snapshot := feed.Get()
		return snapshot.Refresh.Status == "failed" &&
			snapshot.Programs[0].Slug == "fresh" &&
			strings.Contains(snapshot.Refresh.Error, "storage unavailable") &&
			len(snapshot.Diagnostics) == 1
	})

	phase.Store(2)
	feed.Refresh()
	eventually(t, time.Second, func() bool {
		snapshot := feed.Get()
		return snapshot.Refresh.Status == "fresh" &&
			snapshot.Programs[0].Slug == "recovered" &&
			len(snapshot.Diagnostics) == 0
	})
}
