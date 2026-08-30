//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCheckBeadGateCrossRigPrefixRoute proves the full evaluator seam: the
// historical rig:id value is reduced to the target bead ID, the current
// routes.jsonl prefix router opens the foreign store read-only, and target
// lifecycle state determines whether the gate resolves.
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestCheckBeadGateCrossRigPrefixRoute(t *testing.T) {
	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(townRoot, "rig", ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")
	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	for _, issue := range []*types.Issue{
		{ID: "gt-closed", Title: "Closed routed target", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		{ID: "gt-open", Title: "Open routed target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := rigStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create routed issue %s: %v", issue.ID, err)
		}
	}
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDBPath })
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("change to town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	getter := routedBeadGateGetter{localStore: townStore}
	resolved, reason := checkBeadGate(ctx, getter, "rig:gt-closed")
	if !resolved {
		t.Fatalf("closed cross-rig target did not resolve gate: %s", reason)
	}

	resolved, reason = checkBeadGate(ctx, getter, "rig:gt-open")
	if resolved {
		t.Fatalf("open cross-rig target unexpectedly resolved gate: %s", reason)
	}
	if !gateTestContainsIgnoreCase(reason, "open") {
		t.Fatalf("pending reason %q does not report target status", reason)
	}
}
