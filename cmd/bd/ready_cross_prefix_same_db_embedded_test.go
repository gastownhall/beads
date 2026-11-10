//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedReadyCrossPrefixSameDatabaseBlocker is the GH#8299 report: a
// database initialized with prefix b also holds a-x1 (forced id), and
// `bd dep add a-x1 <b blocker>` must gate a-x1 out of ready work. Before the
// fix the edge was filed in depends_on_external because the prefixes differ,
// readiness never joined it, and a-x1 stayed ready beside its open blocker.
func TestEmbeddedReadyCrossPrefixSameDatabaseBlocker(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "b")
	blocker := bdCreate(t, bd, dir, "Open blocker", "--type", "task")
	dependent := bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", "a-x1", "--force")
	bdDep(t, bd, dir, "add", dependent.ID, blocker.ID)

	ready := bdReadyIDs(t, bd, dir)
	if ready[dependent.ID] {
		t.Errorf("%s is ready while its local blocker %s is open", dependent.ID, blocker.ID)
	}
	if !ready[blocker.ID] {
		t.Errorf("blocker %s missing from ready work", blocker.ID)
	}
}

// TestEmbeddedDepAddCrossPrefixLocalBlockerGatesReady carries the report
// through the whole lifecycle: the dependent is explained as blocked by that
// exact local target while it is open, and closing the blocker makes the
// dependent ready again.
func TestEmbeddedDepAddCrossPrefixLocalBlockerGatesReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "b")
	blocker := bdCreate(t, bd, dir, "Open blocker", "--type", "task")
	dependent := bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", "a-x1", "--force")
	bdDep(t, bd, dir, "add", dependent.ID, blocker.ID)

	if bdReadyIDs(t, bd, dir)[dependent.ID] {
		t.Fatalf("%s is ready while its local blocker %s is open", dependent.ID, blocker.ID)
	}

	stdout, _ := bdRunSplit(t, bd, dir, "ready", "--explain", "--json")
	var explanation types.ReadyExplanation
	if err := json.Unmarshal(bytes.TrimSpace([]byte(stdout)), &explanation); err != nil {
		t.Fatalf("parse ready --explain JSON: %v\n%s", err, stdout)
	}
	var blockedBy []string
	explained := false
	for _, item := range explanation.Blocked {
		if item.ID != dependent.ID {
			continue
		}
		explained = true
		for _, b := range item.BlockedBy {
			blockedBy = append(blockedBy, b.ID)
		}
	}
	if !explained || len(blockedBy) != 1 || blockedBy[0] != blocker.ID {
		t.Errorf("ready --explain: %s blocked=%v by %v, want blocked by exactly [%s]\n%s",
			dependent.ID, explained, blockedBy, blocker.ID, stdout)
	}

	bdClose(t, bd, dir, blocker.ID)
	if !bdReadyIDs(t, bd, dir)[dependent.ID] {
		t.Errorf("%s is not ready after its only blocker %s closed", dependent.ID, blocker.ID)
	}
}

// bdReadyIDs runs `bd ready --json` and returns the ids it lists.
func bdReadyIDs(t *testing.T, bd, dir string) map[string]bool {
	t.Helper()
	cmd := exec.Command(bd, "ready", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var ready []types.IssueWithCounts
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &ready); err != nil {
		t.Fatalf("parse ready JSON: %v\n%s", err, stdout.String())
	}
	ids := make(map[string]bool, len(ready))
	for _, issue := range ready {
		ids[issue.ID] = true
	}
	return ids
}
