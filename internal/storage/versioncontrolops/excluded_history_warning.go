package versioncontrolops

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// FederationWarnWriter receives the one-time warning for excluded issue history.
var FederationWarnWriter io.Writer = os.Stderr

var excludedHistoryWarningOnce sync.Once

// WarnExcludedHistory warns once per process, after a push published a tip
// filtered of removed issues, that the pushed history still holds them.
// removed <= 0 (nothing was filtered) neither warns nor uses up the warning.
func WarnExcludedHistory(removed int64) {
	if removed <= 0 {
		return
	}
	excludedHistoryWarningOnce.Do(func() {
		_, _ = fmt.Fprintf(FederationWarnWriter, "Warning: federation.exclude_types removed %d issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n", removed)
	})
}

// ResetExcludedHistoryWarningForTesting re-arms WarnExcludedHistory, so a
// test can observe the warning whatever filtered push ran before it in the
// same process.
func ResetExcludedHistoryWarningForTesting() {
	excludedHistoryWarningOnce = sync.Once{}
}
