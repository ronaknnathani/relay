package cli

import (
	"time"

	"github.com/ronaknnathani/relay/internal/ui"
)

// displayZone is the zone human output renders timestamps in. It reads
// time.Local on every call, so a command renders where it is actually running,
// and it is a seam so a test can pin a zone rather than move the whole process
// into one.
var displayZone = func() *time.Location { return time.Local }

// localTime renders one stored timestamp for a human reader. Relay records
// every timestamp in UTC and every machine surface — `--json` output, runtime
// records, digests, the program UI payload — keeps that value exactly as
// stored; only text a person reads goes through here. A value that is not
// RFC3339 is printed as recorded rather than blanked.
func localTime(value string) string {
	return ui.LocalTimeText(value, displayZone())
}
