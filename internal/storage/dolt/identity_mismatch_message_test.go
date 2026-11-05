package dolt

import (
	"strings"
	"testing"
)

// TestProjectIdentityMismatchError_InitAdvice pins GH#5558: when the identity
// check fires inside bd init (Config.OpenedByInit), the refusal must not tell
// the user not to run the command they are running, and must name remedies that
// work from there. Every other open keeps its original advice — including a
// CreateIfMissing open, which is not by itself bd init.
func TestProjectIdentityMismatchError_InitAdvice(t *testing.T) {
	const localID, dbID, database = "project-AAAA", "project-BBBB", "shared_db"

	t.Run("opened-by-init", func(t *testing.T) {
		msg := projectIdentityMismatchError(localID, dbID, database, true).Error()
		if strings.Contains(msg, "Do NOT run 'bd init'") {
			t.Errorf("init-time refusal tells the user not to run bd init:\n%s", msg)
		}
		for _, want := range []string{
			"PROJECT IDENTITY MISMATCH",
			localID,
			dbID,
			`"` + database + `"`,
			"bd dolt status",
			"bd init --database <other-name>",
			"bd init --server-host <host> --server-port <port>",
			"bd doctor --fix",
			"bd bootstrap",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("init-time refusal missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("normal open", func(t *testing.T) {
		msg := projectIdentityMismatchError(localID, dbID, database, false).Error()
		for _, want := range []string{
			"PROJECT IDENTITY MISMATCH — refusing to connect",
			localID,
			dbID,
			"bd dolt status",
			"Do NOT run 'bd init' — your data likely exists, just on a different server.",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("normal-open refusal missing %q:\n%s", want, msg)
			}
		}
	})
}

// TestGlobalProjectIdentityMismatchError_Labels pins the beads_global refusal:
// each ID sits on its own labeled line, and neither is part of the format. The
// message once concatenated the metadata.json ID onto its format string, so it
// printed the database's ID on the metadata.json line, %!s(MISSING) on the
// database line, and the metadata.json ID unlabeled at the end.
func TestGlobalProjectIdentityMismatchError_Labels(t *testing.T) {
	for _, tc := range []struct{ name, expectedID, dbID string }{
		{"plain IDs", "global-AAAA", "global-BBBB"},
		// An ID that reached the format would have its '%' read as a verb.
		{"IDs holding verbs", "global-%s-AAAA", "global-%d-BBBB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := globalProjectIdentityMismatchError(tc.expectedID, tc.dbID).Error()
			assertGlobalMismatchLines(t, msg, tc.expectedID, tc.dbID)
		})
	}
}

// assertGlobalMismatchLines checks that msg is a GLOBAL PROJECT IDENTITY
// MISMATCH refusal with expectedID on its metadata.json line, dbID on its
// database line, and no fmt error verb (%!) anywhere.
func assertGlobalMismatchLines(t *testing.T, msg, expectedID, dbID string) {
	t.Helper()
	if !strings.Contains(msg, "GLOBAL PROJECT IDENTITY MISMATCH — refusing to connect") {
		t.Errorf("not a global identity refusal:\n%s", msg)
	}
	for _, line := range []struct{ label, want string }{
		{"Expected global project ID (metadata.json):", expectedID},
		{"Database project ID:", dbID},
	} {
		if got := labeledValue(msg, line.label); got != line.want {
			t.Errorf("%q line holds %q, want %q:\n%s", line.label, got, line.want, msg)
		}
	}
	if strings.Contains(msg, "%!") {
		t.Errorf("refusal holds a fmt error verb:\n%s", msg)
	}
}

// labeledValue returns the trimmed text after label on the first line of msg
// that starts with it once indentation is trimmed, or "<no such line>".
func labeledValue(msg, label string) string {
	for _, line := range strings.Split(msg, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), label); ok {
			return strings.TrimSpace(value)
		}
	}
	return "<no such line>"
}
