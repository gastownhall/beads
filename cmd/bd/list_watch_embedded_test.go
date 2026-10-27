//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Embedded `bd list --watch` against other bd commands in the same workspace
// (GH#3415). These run as subtests of TestEmbeddedListConcurrent, so they
// ride its shard manifest entry.

// bdWithin runs `bd <args>` in dir and fails unless it exits 0 within limit,
// returning its stdout.
func bdWithin(t *testing.T, bd, dir string, limit time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.WaitDelay = 5 * time.Second
	started := time.Now()
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if ctx.Err() != nil {
		t.Fatalf("bd %s did not exit within %s alongside the watcher\nstderr:\n%s", strings.Join(args, " "), limit, stderr.String())
	}
	if err != nil {
		t.Fatalf("bd %s failed after %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), time.Since(started), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// testListWatchLeavesListAndShowFree: with `bd list --watch` up and past at
// least one poll interval, `bd list --json` and `bd show --json` in the same
// workspace each finish and see the seeded issue.
func testListWatchLeavesListAndShowFree(t *testing.T, bd string) {
	dir, _, _ := bdInit(t, bd, "--prefix", "lw")
	id := bdCreateSilent(t, bd, dir, "watched")

	w := startShowWatch(t, bd, dir, bdEnv(dir), "list", "--watch")
	// The watcher polls every 2s; outlast one interval so it has refreshed.
	select {
	case <-w.exited:
		t.Fatalf("bd list --watch exited on its own: %v\nstderr:\n%s", w.waitErr, w.stderr.String())
	case <-time.After(3 * time.Second):
	}

	out := bdWithin(t, bd, dir, 30*time.Second, "list", "--json")
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("bd list --json returned no JSON array:\n%s", out)
	}
	var listed []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(out[start:]), &listed); err != nil {
		t.Fatalf("decode bd list --json: %v\n%s", err, out)
	}
	if !containsID(listed, id) {
		t.Fatalf("bd list --json alongside the watcher = %v, want %s", listIssueIDs(listed), id)
	}

	if shown := parseIssueJSON(t, []byte(bdWithin(t, bd, dir, 30*time.Second, "show", id, "--json"))); shown.ID != id {
		t.Fatalf("bd show %s --json alongside the watcher returned %s", id, shown.ID)
	}
	w.interrupt(t)
}
