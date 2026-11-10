//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestShowSiblingStorePrefixFromRigSeat reproduces GH#5259: from a rig seat
// (the rig's own .beads, no routes.jsonl of its own), bd show of an id whose
// prefix names the town store on the same Dolt server reported a plain
// "no issue found matching" — a message that reads as "the issue does not
// exist" although the town store on the same server holds it and was never
// searched.
//
// The approved design (bd-jrvdq8) keeps resolution configuration-driven (no
// sibling-store discovery; a route in the resolved workspace's routes.jsonl
// is the intended mechanism) and makes the miss truthful instead: bd show
// must say the id's prefix is not this store's and that no configured route
// sent the lookup to the store that owns it, instead of the deletion/history
// hint that implies absence everywhere. The resolver's error identity is
// unchanged (the plain not-found stays what every caller sees), so this
// repro pins the explanation at the layer that owns it: bd show's miss hint.
func TestShowSiblingStorePrefixFromRigSeat(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })

	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "openclaw")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, d := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	const townID = "hq-gea4h"
	townStore := newTestStoreIsolatedDB(t, filepath.Join(townBeadsDir, "dolt"), "hq")
	if err := townStore.CreateIssue(ctx, &types.Issue{
		ID: townID, Title: "Incident record", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeBug,
	}, "test"); err != nil {
		t.Fatalf("create town issue: %v", err)
	}
	if err := townStore.Close(); err != nil {
		t.Fatalf("close town store: %v", err)
	}

	// The rig seat's own store, openclaw, which the command uses. The town's
	// routes.jsonl maps hq- to itself, but the rig's resolved workspace has no
	// routes.jsonl of its own, so nothing routes hq-gea4h anywhere.
	rigDBPath := filepath.Join(rigBeadsDir, "dolt")
	rigStore := newTestStoreIsolatedDB(t, rigDBPath, "op")

	routes := `{"prefix":"hq-","path":"."}` + "\n" + `{"prefix":"op-","path":"openclaw"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	dbPath = rigDBPath
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir rig: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	store, rootCtx, jsonOutput, proxiedServerMode = rigStore, ctx, false, false

	var runErr error
	stderr := captureStderr(t, func() { runErr = showCmd.RunE(showCmd, []string{townID}) })
	if runErr == nil {
		t.Fatalf("bd show %s from the rig seat succeeded, want the not-found exit", townID)
	}
	if !strings.Contains(stderr, "Issue "+townID+" not found") {
		t.Fatalf("bd show %s from the rig seat: stderr = %q, want the existing leading not-found line", townID, stderr)
	}
	// The truthful miss: name the requested and configured prefixes, say the
	// store that owns the prefix was not searched, and do not imply absence
	// elsewhere (the deletion/history hint's claim).
	for _, want := range []string{`prefix "hq"`, `issue prefix "op"`, "does not mean the issue is absent"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("bd show %s from the rig seat: stderr missing %q\nstderr: %s"+
				"\nthe hq- store is a sibling on the same server; a bare not-found (or the"+
				" deletion hint) asserts a nonexistence that was never checked", townID, want, stderr)
		}
	}
	if strings.Contains(stderr, "never existed") {
		t.Errorf("bd show %s from the rig seat: stderr suggests the id never existed, "+
			"want the limited-search hint; stderr: %s", townID, stderr)
	}
}
