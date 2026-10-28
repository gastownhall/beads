//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedDepListSingleIDWarnsOnDroppedExternalEdge is the regression
// guard for bd-mtla: `bd link` across two databases writes the dependency
// correctly (into depends_on_external), but the single-id `bd dep list <id>`
// a caller naturally runs right after silently showed nothing for it —
// indistinguishable from no dependency ever being created. This verifies the
// fix: stdout/--json stay byte-for-byte what they were before the fix for the
// local-only case (no schema/shape change), while stderr now names the edge
// Relations could not resolve locally.
func TestEmbeddedDepListSingleIDWarnsOnDroppedExternalEdge(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt dep tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Primary repo ("gt"), with routes.jsonl pointing its "hq-" prefix at a
	// sibling repo, mirroring a town/rig layout.
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

	local := bdCreate(t, bd, dir, "local target")
	external := bdCreate(t, bd, targetDir, "external target")
	source := bdCreate(t, bd, dir, "source issue")

	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, local.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (local): %v\n%s", err, out)
	}
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, external.ID, "--type", "related"); err != nil {
		t.Fatalf("bd link (external): %v\n%s", err, out)
	}

	cmd := exec.Command(bd, "dep", "list", source.ID)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dep list: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), local.ID) {
		t.Errorf("stdout missing local dep %s:\n%s", local.ID, stdout.String())
	}
	if strings.Contains(stdout.String(), external.ID) {
		t.Errorf("stdout unexpectedly contains external target %s (shape must stay unchanged):\n%s", external.ID, stdout.String())
	}
	if !strings.Contains(stderr.String(), external.ID+" via related") {
		t.Errorf("stderr should warn about dropped external target %s with its type, got:\n%s", external.ID, stderr.String())
	}

	// --json: must stay exactly what it was before the fix for the local-only case.
	jsonCmd := exec.Command(bd, "dep", "list", source.ID, "--json")
	jsonCmd.Dir = dir
	jsonCmd.Env = bdEnv(dir)
	jsonStdout, _, err := runCommandBuffers(t, jsonCmd)
	if err != nil {
		t.Fatalf("bd dep list --json: %v", err)
	}
	if strings.Contains(jsonStdout.String(), external.ID) {
		t.Errorf("--json output unexpectedly contains external target %s:\n%s", external.ID, jsonStdout.String())
	}
	if !strings.Contains(jsonStdout.String(), local.ID) {
		t.Errorf("--json output missing local dep %s:\n%s", local.ID, jsonStdout.String())
	}

	// Batch mode (the already-correct path) must still show both.
	batchCmd := exec.Command(bd, "dep", "list", source.ID, source.ID)
	batchCmd.Dir = dir
	batchCmd.Env = bdEnv(dir)
	batchStdout, _, err := runCommandBuffers(t, batchCmd)
	if err != nil {
		t.Fatalf("bd dep list (batch): %v", err)
	}
	if !strings.Contains(batchStdout.String(), external.ID) || !strings.Contains(batchStdout.String(), local.ID) {
		t.Errorf("batch mode should show both deps, got:\n%s", batchStdout.String())
	}

	// #6557: an issue whose ONLY dependency is cross-repo must not be
	// reported as having "no dependencies" on stdout — the edge is stored and
	// the stderr warning just named it.
	lonely := bdCreate(t, bd, dir, "lonely issue")
	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", lonely.ID, external.ID, "--type", "related"); err != nil {
		t.Fatalf("bd link (lonely external): %v\n%s", err, out)
	}
	lonelyCmd := exec.Command(bd, "dep", "list", lonely.ID)
	lonelyCmd.Dir = dir
	lonelyCmd.Env = bdEnv(dir)
	lonelyStdout, lonelyStderr, err := runCommandBuffers(t, lonelyCmd)
	if err != nil {
		t.Fatalf("bd dep list (lonely): %v\nstdout:\n%s\nstderr:\n%s", err, lonelyStdout.String(), lonelyStderr.String())
	}
	if strings.Contains(lonelyStdout.String(), "has no dependencies") {
		t.Errorf("stdout claims no dependencies although a cross-repo edge is stored:\n%s", lonelyStdout.String())
	}
	if strings.Contains(lonelyStdout.String(), external.ID) {
		t.Errorf("stdout unexpectedly contains external target %s:\n%s", external.ID, lonelyStdout.String())
	}
	if want := lonely.ID + " has no local dependencies (1 cross-repo/external edge(s) listed on stderr)"; !strings.Contains(lonelyStdout.String(), want) {
		t.Errorf("stdout missing %q:\n%s", want, lonelyStdout.String())
	}
	if !strings.Contains(lonelyStderr.String(), external.ID+" via related") {
		t.Errorf("stderr should still name the dropped edge %s via related, got:\n%s", external.ID, lonelyStderr.String())
	}

	// With --type blocks the related edge drops out of both reads: no stored
	// edge matches, so the old line stays and nothing is warned about.
	blocksCmd := exec.Command(bd, "dep", "list", lonely.ID, "--type", "blocks")
	blocksCmd.Dir = dir
	blocksCmd.Env = bdEnv(dir)
	blocksStdout, blocksStderr, err := runCommandBuffers(t, blocksCmd)
	if err != nil {
		t.Fatalf("bd dep list --type blocks (lonely): %v\nstdout:\n%s\nstderr:\n%s", err, blocksStdout.String(), blocksStderr.String())
	}
	if want := "\n" + lonely.ID + " has no dependencies\n"; !strings.Contains(blocksStdout.String(), want) {
		t.Errorf("stdout missing the unchanged %q:\n%s", want, blocksStdout.String())
	}
	if strings.Contains(blocksStderr.String(), "additional dependency edge(s)") {
		t.Errorf("stderr warns although no stored edge matches --type blocks:\n%s", blocksStderr.String())
	}
}
