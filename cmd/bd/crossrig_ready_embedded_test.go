//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossRigReadyReleasesAndWithholdsOnForeignStateChange pins
// GH#5784's cross-rig readiness contract end to end (AC-2): a source in one
// rig blocked by a bare cross-prefix edge it stored against another rig's
// issue is withheld from `bd ready` while the target is open, RELEASED by the
// next query after the target closes in its own rig — with no write into the
// source's store and no sync command in between — and withheld again once the
// target reopens. The foreign state is read live through the source
// workspace's own routes.jsonl, in the target rig's own database, exactly as a
// worker running `bd ready` would observe it.
func TestEmbeddedCrossRigReadyReleasesAndWithholdsOnForeignStateChange(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig ready tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Source rig ("gt"), with routes.jsonl pointing its "hq-" prefix at a
	// sibling rig, mirroring a town/rig layout.
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gt")

	targetDir := filepath.Join(dir, "hq-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "hq")

	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"hq-","path":"hq-repo"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	foreign := bdCreate(t, bd, targetDir, "foreign blocker")
	source := bdCreate(t, bd, dir, "source with cross-rig blocker")
	unblocked := bdCreate(t, bd, dir, "locally free source")

	// Cross-prefix `bd link` stores the bare foreign ID (add now, route
	// later): the shape GH#5784 reported as silently non-blocking.
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, foreign.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (cross-prefix): %v\n%s", err, out)
	}

	ready := func() string {
		t.Helper()
		out, err := bdRunWithFlockRetry(t, bd, dir, "ready")
		if err != nil {
			t.Fatalf("bd ready: %v\n%s", err, out)
		}
		return string(out)
	}

	// Open foreign target: the blocked source is withheld, the locally free
	// one is offered. A ready query answers exit 0 either way.
	out := ready()
	if strings.Contains(out, source.ID) {
		t.Errorf("bd ready offered %s while its cross-rig blocker %s is open:\n%s", source.ID, foreign.ID, out)
	}
	if !strings.Contains(out, unblocked.ID) {
		t.Errorf("bd ready withheld locally free %s:\n%s", unblocked.ID, out)
	}

	// The target closes IN ITS OWN RIG. No source-store write, no sync
	// command: the next ready query must observe the close through the route.
	if out, err := bdRunWithFlockRetry(t, bd, targetDir, "close", foreign.ID); err != nil {
		t.Fatalf("bd close (foreign rig): %v\n%s", err, out)
	}
	out = ready()
	if !strings.Contains(out, source.ID) {
		t.Errorf("bd ready withheld %s after its cross-rig blocker %s closed:\n%s", source.ID, foreign.ID, out)
	}
	if !strings.Contains(out, unblocked.ID) {
		t.Errorf("bd ready lost locally free %s after the foreign close:\n%s", unblocked.ID, out)
	}

	// Back to open: withheld again, on the next query, the same way.
	if out, err := bdRunWithFlockRetry(t, bd, targetDir, "reopen", foreign.ID); err != nil {
		t.Fatalf("bd reopen (foreign rig): %v\n%s", err, out)
	}
	out = ready()
	if strings.Contains(out, source.ID) {
		t.Errorf("bd ready offered %s after its cross-rig blocker %s reopened:\n%s", source.ID, foreign.ID, out)
	}
	if !strings.Contains(out, unblocked.ID) {
		t.Errorf("bd ready lost locally free %s after the foreign reopen:\n%s", unblocked.ID, out)
	}
}
