//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedCountIncludeEphemeral covers the plane knob end to end, on the
// same fixture shape TestEmbeddedCountIncludeInfra uses.
//
// It is a SEPARATE file rather than a subtest of that function on purpose:
// TestEmbeddedCountIncludeInfra pins the existing --include-infra contract and
// is left byte-for-byte untouched, so a reader can see at a glance that this
// flag changed nothing it asserts.
//
// The distinction being pinned: --include-infra bundles four changes (see
// issueops.CountRequest.IncludeInfra) and its template exclusion silently drops
// template rows of the named type. --include-ephemeral admits the plane and
// nothing else.
func TestEmbeddedCountIncludeEphemeral(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ie")

	// 3 durable tasks (one closed), 2 no_history tasks, 1 ephemeral task.
	bdCreate(t, bd, dir, "durable task one", "--type", "task")
	bdCreate(t, bd, dir, "durable task two", "--type", "task")
	closed := bdCreate(t, bd, dir, "durable task closed", "--type", "task")
	bdClose(t, bd, dir, closed.ID)
	bdCreate(t, bd, dir, "nohistory task one", "--type", "task", "--no-history")
	bdCreate(t, bd, dir, "nohistory task two", "--type", "task", "--no-history")
	bdCreate(t, bd, dir, "ephemeral task", "--type", "task", "--ephemeral")

	countOf := func(args ...string) int {
		t.Helper()
		m := bdCountJSON(t, bd, dir, args...)
		return int(m["count"].(float64))
	}

	t.Run("default stays durable-only", func(t *testing.T) {
		if got := countOf("--type", "task"); got != 3 {
			t.Errorf("bd count --type task = %d, want 3 (the default before this change, unchanged)", got)
		}
	})

	t.Run("include-ephemeral reaches the wisps tier", func(t *testing.T) {
		if got := countOf("--type", "task", "--include-ephemeral"); got != 6 {
			t.Errorf("bd count --type task --include-ephemeral = %d, want 6 "+
				"(3 durable + 2 no_history + 1 ephemeral)", got)
		}
	})

	t.Run("count and list read the same plane", func(t *testing.T) {
		// The property is that both reach the wisps tier under the flag — NOT
		// that the two numbers are equal. A count includes template rows and a
		// listing does not, with or without this flag (see
		// issueops.CountRequest.IncludeEphemeral), and this fixture has no
		// templates only because it does not need any. Asserting raw equality
		// here would pass by accident and break the day someone adds one.
		durable := len(bdListJSON(t, bd, dir,
			"--type", "task", "--status", "all", "--limit", "0"))
		withPlane := len(bdListJSON(t, bd, dir,
			"--type", "task", "--include-ephemeral", "--status", "all", "--limit", "0"))
		if withPlane-durable != 3 {
			t.Errorf("list gained %d rows from the plane, want 3 (2 no_history + 1 ephemeral)",
				withPlane-durable)
		}
		if got := countOf("--type", "task", "--include-ephemeral") - countOf("--type", "task"); got != 3 {
			t.Errorf("count gained %d rows from the plane, want 3 — count and list "+
				"must admit the same tier even where their totals differ", got)
		}
	})
}
