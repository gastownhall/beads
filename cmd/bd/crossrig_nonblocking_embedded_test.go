//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossRigNonBlockingEdgesStayReadyAndOpenNoForeignStore pins
// GH#5784 AC-9: readiness is gated only by bare cross-prefix blocks and
// conditional-blocks edges. A source whose bare foreign edges are tracks or
// related stays offered even when the target rig is UNREACHABLE — the same
// broken route that withholds a blocked source and warns about it must leave
// the non-blocked source offered and silent, because readiness opens no
// foreign store for an edge that cannot gate.
func TestEmbeddedCrossRigNonBlockingEdgesStayReadyAndOpenNoForeignStore(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig ready tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gt")

	targetDir := filepath.Join(dir, "hq-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "hq")

	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	writeRoutes := func(path string) {
		t.Helper()
		if err := os.WriteFile(routesPath, []byte(`{"prefix":"hq-","path":"`+path+`"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeRoutes("hq-repo")

	tracked := bdCreate(t, bd, targetDir, "foreign tracks target")
	related := bdCreate(t, bd, targetDir, "foreign related target")
	blockedTarget := bdCreate(t, bd, targetDir, "foreign blocking target")
	tracksSource := bdCreate(t, bd, dir, "source with cross-rig tracks edge")
	relatedSource := bdCreate(t, bd, dir, "source with cross-rig related edge")
	blockedSource := bdCreate(t, bd, dir, "source with cross-rig blocking edge")

	for _, link := range []struct {
		source  string
		target  string
		depType string
	}{
		{tracksSource.ID, tracked.ID, "tracks"},
		{relatedSource.ID, related.ID, "related"},
		{blockedSource.ID, blockedTarget.ID, "blocks"},
	} {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "link", link.source, link.target, "--type", link.depType); err != nil {
			t.Fatalf("bd link %s -> %s (%s): %v\n%s", link.source, link.target, link.depType, err, out)
		}
	}

	ready := func() (stdout, stderr string) {
		t.Helper()
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "ready")
		if err != nil {
			t.Fatalf("bd ready: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		return stdout, stderr
	}

	// Route intact: the blocked source is withheld, the tracks/related sources
	// are offered, and nothing warns — non-blocking edges resolved nothing
	// because there was nothing to resolve.
	stdout, stderr := ready()
	if strings.Contains(stdout, blockedSource.ID) {
		t.Errorf("bd ready offered %s while its cross-rig blocker %s is open:\n%s", blockedSource.ID, blockedTarget.ID, stdout)
	}
	for _, offered := range []string{tracksSource.ID, relatedSource.ID} {
		if !strings.Contains(stdout, offered) {
			t.Errorf("bd ready withheld %s, whose foreign edges are non-blocking:\n%s", offered, stdout)
		}
	}
	if strings.Contains(stderr, "cross-rig") {
		t.Errorf("bd ready warned about cross-rig targets with an intact route:\nstderr:\n%s", stderr)
	}

	// The route breaks. The blocked source stays withheld and now warns; the
	// tracks/related sources must not notice at all — offered, no warning —
	// because readiness never opens the foreign store for their edges.
	writeRoutes("gone-rig")
	stdout, stderr = ready()
	if strings.Contains(stdout, blockedSource.ID) {
		t.Errorf("bd ready offered %s after its route broke:\n%s", blockedSource.ID, stdout)
	}
	if !strings.Contains(stderr, blockedTarget.ID) {
		t.Errorf("bd ready must warn about the unreadable blocking target %s (control for the broken route):\nstderr:\n%s", blockedTarget.ID, stderr)
	}
	for _, offered := range []string{tracksSource.ID, relatedSource.ID} {
		if !strings.Contains(stdout, offered) {
			t.Errorf("bd ready withheld %s after the route broke, though its %s edge cannot gate readiness:\n%s", offered, map[string]string{tracksSource.ID: "tracks", relatedSource.ID: "related"}[offered], stdout)
		}
	}
	if strings.Contains(stderr, tracked.ID) || strings.Contains(stderr, related.ID) {
		t.Errorf("bd ready resolved a non-blocking foreign target, opening a foreign store for an edge that cannot gate:\nstderr:\n%s", stderr)
	}
}
