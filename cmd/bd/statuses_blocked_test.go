package main

import (
	"strings"
	"testing"
)

// TestStatusesExplainsBlockedStatus pins how bd statuses describes 'blocked'
// (bd-e5d8fd): the row says the status is set by hand rather than by a
// dependency, and the help separates it from dependency blocking and names
// the reviewed release and the doctor checks that list held issues.
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
	if !strings.HasSuffix(row, "]  Held on purpose; set by hand, not by dependencies") {
		t.Errorf("bd statuses prints the blocked row %q, want it to say the status is set by hand, not by dependencies", row)
	}

	for _, want := range []string{
		"'bd blocked'",
		"leaves the status as it is",
		"bd update <id> --status open",
		"'bd doctor' (Blocked Status)",
		"'bd doctor --check=conventions' (conventions.blocked-status)",
	} {
		if !strings.Contains(statusesCmd.Long, want) {
			t.Errorf("bd statuses --help does not name %q:\n%s", want, statusesCmd.Long)
		}
	}
}
