//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// waitForOutput blocks until w holds text, giving up when the process exits
// or timeout elapses.
func waitForOutput(w *watchBannerWriter, text string, timeout time.Duration, exited <-chan struct{}) bool {
	deadline := time.After(timeout)
	for !strings.Contains(w.String(), text) {
		select {
		case <-w.renders:
		case <-exited:
			return strings.Contains(w.String(), text)
		case <-deadline:
			return false
		}
	}
	return true
}

// testListWatchParentTreeLeavesCommandsFree: with `bd list --watch --parent`
// up on a 44-descendant, three-level epic, `bd show`, `bd list` and
// `bd update` run one after another each finish within 30s, and the watcher
// picks up the update within 10s. Every refresh of the tree is one
// SearchDescendants read, and the watcher waits a full interval after it.
func testListWatchParentTreeLeavesCommandsFree(t *testing.T, bd string) {
	dir, _, _ := bdInit(t, bd, "--prefix", "lp")

	type node struct {
		Key       string `json:"key"`
		Title     string `json:"title"`
		Type      string `json:"type"`
		ParentKey string `json:"parent_key,omitempty"`
	}
	nodes := []node{{Key: "epic", Title: "Watched epic", Type: "epic"}}
	for m := range 2 {
		mid := fmt.Sprintf("m%d", m)
		nodes = append(nodes, node{Key: mid, Title: "Watched " + mid, Type: "task", ParentKey: "epic"})
		for s := range 3 {
			sub := fmt.Sprintf("%s-s%d", mid, s)
			nodes = append(nodes, node{Key: sub, Title: "Watched " + sub, Type: "task", ParentKey: mid})
			for l := range 6 {
				leaf := fmt.Sprintf("%s-l%d", sub, l)
				nodes = append(nodes, node{Key: leaf, Title: "Watched " + leaf, Type: "task", ParentKey: sub})
			}
		}
	}
	plan, err := json.Marshal(map[string][]node{"nodes": nodes})
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(t.TempDir(), "tree.json")
	if err := os.WriteFile(planFile, plan, 0o600); err != nil {
		t.Fatal(err)
	}
	ids := bdCreateGraph(t, bd, dir, planFile).IDs
	if len(ids) != len(nodes) {
		t.Fatalf("graph created %d issues, want %d (1 epic + 44 descendants)", len(ids), len(nodes))
	}
	epic, leaf := ids["epic"], ids["m1-s2-l5"]

	w := startShowWatch(t, bd, dir, bdEnv(dir), "list", "--watch", "--parent", epic)
	if !waitForOutput(w.stdout, leaf, 10*time.Second, w.exited) {
		t.Fatalf("watched tree never showed leaf %s\nstdout:\n%s", leaf, w.stdout.String())
	}

	if shown := parseIssueJSON(t, []byte(bdWithin(t, bd, dir, 30*time.Second, "show", leaf, "--json"))); shown.ID != leaf {
		t.Fatalf("bd show %s --json alongside the watcher returned %s", leaf, shown.ID)
	}
	bdWithin(t, bd, dir, 30*time.Second, "list", "--json")
	const renamed = "Renamed under the watcher"
	bdWithin(t, bd, dir, 30*time.Second, "update", leaf, "--title", renamed)

	if !waitForOutput(w.stdout, renamed, 10*time.Second, w.exited) {
		t.Fatalf("watcher did not show %q within 10s of the update\nstdout:\n%s\nstderr:\n%s", renamed, w.stdout.String(), w.stderr.String())
	}
	w.interrupt(t)
}
