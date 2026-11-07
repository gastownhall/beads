package main

import (
	"strings"
	"testing"
)

// TestStatusesExplainsBlockedStatus pins how bd statuses describes 'blocked'
// (bd-e5d8fd): the row says the status is an explicit hold, set by hand or a
// tracker, that bd close does not clear, and the help separates it from
// dependency blocking and names the reviewed release and the doctor checks
// that list held issues.
func TestStatusesExplainsBlockedStatus(t *testing.T) {
	initConfigForTest(t)
	previousJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = previousJSON })

	out := captureStdout(t, func() error { return renderStatuses(nil) })
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " blocked ") {
			row = line
		}
	}
	if !strings.HasSuffix(row, "]  An explicit hold, set by hand or a tracker; bd close does not clear it") {
		t.Errorf("bd statuses prints the blocked row %q, want it to say the status is an explicit hold bd close does not clear", row)
	}

	long := strings.Join(strings.Fields(statusesCmd.Long), " ")
	for _, want := range []string{
		"'bd blocked'",
		"or by a tracker sync",
		"clears its dependency blocking but leaves the status as it is",
		"stored dependency-blocked flag",
		"bd update <id> --status open",
		"'bd doctor' (Blocked Status)",
		"'bd doctor --check=conventions' (conventions.blocked-status)",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("bd statuses --help does not name %q:\n%s", want, statusesCmd.Long)
		}
	}
}
