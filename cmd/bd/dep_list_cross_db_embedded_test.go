//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedDepListSingleIDRendersResolvableForeignEdge is the evolved
// regression guard for bd-mtla and GH#5784 §224: `bd link` across two
// databases writes the dependency correctly (into depends_on_external), but
// the single-id `bd dep list <id>` a caller naturally runs right after showed
// nothing for it — indistinguishable from no dependency ever being created.
// bd-mtla closed the gap as a disclosure: stderr named the edge Relations
// could not resolve. GH#5784 AC-6 goes further for the case that has an
// answer: a bare cross-prefix target the anchor's routes can resolve renders
// as a row again — real issue fields, its edge type, an (external) marker in
// text and an external: true view flag in --json — and the dropped-edge
// warning is reserved for targets no route can answer (pinned alongside the
// rendering contract in crossrig_display_embedded_test.go). Batch mode keeps
// showing the raw edge records for every target, answerable or not.
func TestEmbeddedDepListSingleIDRendersResolvableForeignEdge(t *testing.T) {
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
	if !strings.Contains(stdout.String(), external.ID) {
		t.Errorf("stdout must render the resolvable foreign dep %s:\n%s", external.ID, stdout.String())
	}
	if !strings.Contains(stdout.String(), "(external)") {
		t.Errorf("stdout must mark the foreign dep (external):\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), external.ID+" via related") {
		t.Errorf("a resolvable foreign edge is shown, not dropped; no warning for it:\n%s", stderr.String())
	}

	// --json: the foreign row rides along with the external view flag.
	jsonCmd := exec.Command(bd, "dep", "list", source.ID, "--json")
	jsonCmd.Dir = dir
	jsonCmd.Env = bdEnv(dir)
	jsonStdout, _, err := runCommandBuffers(t, jsonCmd)
	if err != nil {
		t.Fatalf("bd dep list --json: %v", err)
	}
	if !strings.Contains(jsonStdout.String(), external.ID) {
		t.Errorf("--json output must list the resolvable foreign dep %s:\n%s", external.ID, jsonStdout.String())
	}
	if !strings.Contains(jsonStdout.String(), `"external": true`) {
		t.Errorf("--json output must carry the external view flag on the foreign row:\n%s", jsonStdout.String())
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
}
