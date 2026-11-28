//go:build cgo

package main

import (
	"encoding/json"
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

	// A mixed batch. Batch mode settles every exact local id in one read and
	// resolves only the rest one at a time, so each kind of argument has to
	// come out as it did when all of them were resolved one at a time: an
	// exact local id with no edges, a bare hash that resolves to the source,
	// an id routed to the sibling database, and an id that names nothing.
	bareSource := strings.TrimPrefix(source.ID, "gt-")
	ghost := "gt-nosuchissue"
	mixedCmd := exec.Command(bd, "dep", "list", local.ID, bareSource, external.ID, ghost, "--json")
	mixedCmd.Dir = dir
	mixedCmd.Env = bdEnv(dir)
	mixedStdout, mixedStderr, err := runCommandBuffers(t, mixedCmd)
	if err != nil {
		t.Fatalf("bd dep list (mixed batch): %v\nstdout:\n%s\nstderr:\n%s", err, mixedStdout.String(), mixedStderr.String())
	}
	var records []struct {
		IssueID     string `json:"issue_id"`
		DependsOnID string `json:"depends_on_id"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal(mixedStdout.Bytes(), &records); err != nil {
		t.Fatalf("mixed batch --json is not an array of dependency records: %v\n%s", err, mixedStdout.String())
	}
	got := map[string]string{}
	for _, record := range records {
		if record.IssueID != source.ID {
			t.Errorf("mixed batch record has source %s, want only %s (the one argument with edges)", record.IssueID, source.ID)
		}
		got[record.DependsOnID] = record.Type
	}
	if len(records) != 2 || got[local.ID] != "blocks" || got[external.ID] != "related" {
		t.Errorf("mixed batch records = %+v, want %s blocks %s and %s related %s", records, source.ID, local.ID, source.ID, external.ID)
	}
	if !strings.Contains(mixedStderr.String(), ghost) || !strings.Contains(mixedStderr.String(), "(skipped)") {
		t.Errorf("mixed batch stderr should report %s as skipped, got:\n%s", ghost, mixedStderr.String())
	}
	for _, resolvable := range []string{local.ID, external.ID} {
		if strings.Contains(mixedStderr.String(), "resolving "+resolvable) || strings.Contains(mixedStderr.String(), "no issue found: "+resolvable) {
			t.Errorf("mixed batch stderr reports %s as unresolved, got:\n%s", resolvable, mixedStderr.String())
		}
	}
}
