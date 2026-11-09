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

// TestEmbeddedReadyCrossPrefixSameDatabaseBlocker pins GH#8299 (bd-6jk1y2): a
// `blocks` edge whose target has a different id prefix but lives in THIS
// database must block the dependent in `bd ready` while the target is open.
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
	sawBlocker := false
	for _, r := range ready {
		if r.ID == dependent.ID {
			t.Fatalf("%s is ready while its same-database blocker %s is open: %s",
				dependent.ID, blocker.ID, stdout.String())
		}
		if r.ID == blocker.ID {
			sawBlocker = true
		}
	}
	if !sawBlocker {
		t.Fatalf("blocker %s missing from ready output: %s", blocker.ID, stdout.String())
	}
}
