package main

import (
	"strings"
	"testing"
)

// TestAuditHelpDescribesSidecarCoverage: GH#6146 — bd audit's help says what
// the sidecar records on its own and where a replaying consumer should look
// instead, so nobody reads interactions.jsonl as a complete history.
func TestAuditHelpDescribesSidecarCoverage(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		`"field_change" (extra: field, old_value, new_value`,
		`"claimed" (extra: status, assignee)`,
		"records only the claimed issue's status and assignee after the claim",
		"Consumers should skip kinds they do not know",
		"best-effort",
		"bd create,\nbd comment",
		"not a replayable history",
		"bd config set events-journal true",
		"bd events tail",
		"bd history <id> --events",
	} {
		if !strings.Contains(auditCmd.Long, want) {
			t.Errorf("bd audit --help is missing %q:\n%s", want, auditCmd.Long)
		}
	}
}
