//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestEmbeddedCrossRigReadyFollowsTheTownRoutes is GH#5784's own layout: an
// orchestrator writes routes.jsonl only at its town root, the rigs are
// siblings under it with no routes of their own, the edge is written from the
// town root, and `bd ready` runs in the rig. The rig follows the town's routes
// ($GT_ROOT/.beads/routes.jsonl, paths from the town root): the source is
// held while the sibling rig's target is open and released once it closes.
// Without GT_ROOT the rig has no routes to follow, so the source stays held
// and the warning names the rig's missing routes.jsonl.
func TestEmbeddedCrossRigReadyFollowsTheTownRoutes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig ready tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	town, townBeads, _ := bdInit(t, bd, "--prefix", "hq")
	rig := func(name, prefix string) string {
		t.Helper()
		dir := filepath.Join(town, name)
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, dir)
		runBDInit(t, bd, dir, "--prefix", prefix)
		return dir
	}
	rigA, rigB := rig("aaa", "aa"), rig("bbb", "bb")
	routes := `{"prefix":"hq-","path":"."}` + "\n" + `{"prefix":"aa-","path":"aaa"}` + "\n" + `{"prefix":"bb-","path":"bbb"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rigA, ".beads", "routes.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("rig aaa has a routes.jsonl of its own (stat: %v); the layout under test has none", err)
	}

	foreign := bdCreate(t, bd, rigB, "foreign blocker")
	source := bdCreate(t, bd, rigA, "source with cross-rig blocker")
	free := bdCreate(t, bd, rigA, "locally free source")

	// Written at the town root, which routes the source to rig aaa.
	if out, err := bdRunWithFlockRetry(t, bd, town, "dep", "add", source.ID, foreign.ID); err != nil {
		t.Fatalf("bd dep add (from the town root): %v\n%s", err, out)
	}

	ready := func(env ...string) (string, string) {
		t.Helper()
		var stdout, stderr string
		var err error
		for attempt := 0; attempt < 10; attempt++ {
			cmd := exec.Command(bd, "ready")
			cmd.Dir = rigA
			cmd.Env = append(envWithout(bdEnv(rigA), "GT_ROOT"), env...)
			var outBuf, errBuf bytes.Buffer
			cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
			err = cmd.Run()
			stdout, stderr = outBuf.String(), errBuf.String()
			if err == nil || !isEmbeddedLockOutput(stdout+stderr) {
				break
			}
			time.Sleep(time.Duration(500*(1<<min(attempt, 4))) * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("bd ready in the rig: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		return stdout, stderr
	}

	underTown := "GT_ROOT=" + town
	out, _ := ready(underTown)
	if strings.Contains(out, source.ID) || !strings.Contains(out, free.ID) {
		t.Fatalf("bd ready in the rig while %s is open: want %s held and %s offered:\n%s", foreign.ID, source.ID, free.ID, out)
	}
	if out, err := bdRunWithFlockRetry(t, bd, rigB, "close", foreign.ID); err != nil {
		t.Fatalf("bd close (sibling rig): %v\n%s", err, out)
	}
	out, _ = ready(underTown)
	if !strings.Contains(out, source.ID) {
		t.Fatalf("bd ready in the rig withheld %s after its cross-rig blocker %s closed in its own rig:\n%s", source.ID, foreign.ID, out)
	}

	out, stderr := ready()
	if strings.Contains(out, source.ID) {
		t.Fatalf("bd ready without GT_ROOT offered %s, whose blocker it has no route to read:\n%s", source.ID, out)
	}
	if !strings.Contains(stderr, foreign.ID) || !strings.Contains(stderr, "no routes.jsonl") {
		t.Fatalf("bd ready without GT_ROOT: want the warning to name %s and the missing routes.jsonl, stderr:\n%s", foreign.ID, stderr)
	}
}
