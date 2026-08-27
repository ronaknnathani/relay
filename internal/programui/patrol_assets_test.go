package programui

import (
	"strings"
	"testing"
)

func TestProgramUIShowsPatrolCadenceAndDiagnostics(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`<dt>Patrol</dt>`,
		`id="patrol-status"`,
		`id="patrol-note"`,
	})
	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		`dom.patrolStatus = byID("patrol-status")`,
		`const patrol = snapshotOf().patrol || {}`,
		`formatPatrolCadence(count(patrol.delay_seconds))`,
		`patrol.cto_present ? "CTO present" : "CTO unavailable"`,
		`text(patrol.warning)`,
		`["Patrol", patrolReasons()]`,
		`function patrolReasons()`,
	})
	styles := readAsset(t, "assets/app.css")
	requireContains(t, "app.css", styles, []string{
		"grid-template-columns: repeat(6, minmax(0, 1fr))",
	})
}

// The bounded CTO turn transcript may contain repository content, so the UI
// surfaces only its status and log location, never the log body.
func TestProgramUIShowsBoundedTurnMetadataWithoutTheTranscript(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{`id="patrol-turn"`})
	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		`dom.patrolTurn = byID("patrol-turn")`,
		`function patrolTurnNote(turn)`,
		"`last turn ${humanize(status)}`",
		"`log ${turn.log_path}`",
		"`last turn error: ${turn.error}`",
	})
	for _, forbidden := range []string{"turn.log_text", "turn.transcript", "turn.output"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("app.js inlines the bounded turn transcript via %q", forbidden)
		}
	}
}
