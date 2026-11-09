//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedDepAddUnverifiedCrossPrefixLifecycle pins GH#8299's preservation
// half end to end. Presence-first classification changed which cross-prefix
// targets are LOCAL, and the bare foreign-prefix target this database does not
// hold must keep exactly the behavior it always had: `bd dep add` stores it
// unverified in depends_on_external with the GH#6556 warning on stderr (and
// stdout silent, for scripts), the edge creates no local blocker — the source
// stays in ready work — and `bd dep remove` deletes precisely the stored row.
func TestEmbeddedDepAddUnverifiedCrossPrefixLifecycle(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ud7")

	source := bdCreate(t, bd, dir, "Source with an unverified cross-repo edge", "--type", "task")
	const target = "liveop-zqx9"

	// dep add accepts the bare foreign-prefix target nothing found, and says
	// so: the warning goes to stderr, and stdout stays byte-identical.
	stdout, stderr := bdRunSplit(t, bd, dir, "dep", "add", source.ID, target, "--type", "blocks")
	if !strings.Contains(stderr, "warning: dependency target "+target) ||
		!strings.Contains(stderr, "stored unverified as a cross-repo reference") {
		t.Errorf("dep add stderr did not carry the unverified-target warning:\nstderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "warning:") {
		t.Errorf("the warning reached stdout, which must stay unchanged for scripts:\n%s", stdout)
	}
	if !strings.Contains(stderr, "bd dep remove "+source.ID+" "+target) {
		t.Errorf("the warning's remedy line does not name the exact remove command:\n%s", stderr)
	}

	// The edge is stored and counted but resolves to no local row (be-lpi's
	// shape), which is the storage-side proof it filed external.
	details := bdShowDetails(t, bd, dir, source.ID)
	if depCount, ok := details["dependency_count"].(float64); !ok || depCount != 1 {
		t.Fatalf("dependency_count = %v, want 1 — the edge must be stored and counted",
			details["dependency_count"])
	}
	if unresolvable, ok := details["unresolvable_dependencies"].(float64); !ok || unresolvable != 1 {
		t.Fatalf("unresolvable_dependencies = %v, want 1 — the target has no row in this database",
			details["unresolvable_dependencies"])
	}

	// No local blocker: a source whose only edge points at nothing this
	// database holds stays ready work.
	readyOut, _ := bdRunSplit(t, bd, dir, "ready", "--json")
	if !strings.Contains(readyOut, source.ID) {
		t.Errorf("%s missing from ready work while its only dependency names a target "+
			"no local table holds:\n%s", source.ID, readyOut)
	}

	// And dep remove deletes exactly the stored row.
	bdDep(t, bd, dir, "remove", source.ID, target)
	details = bdShowDetails(t, bd, dir, source.ID)
	if depCount, ok := details["dependency_count"].(float64); ok && depCount != 0 {
		t.Errorf("dependency_count = %v after the removal, want 0", depCount)
	}
	if unresolvable, present := details["unresolvable_dependencies"]; present {
		t.Errorf("unresolvable_dependencies = %v after the removal, want absent", unresolvable)
	}
}
