//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// relateRigs is a town workspace (prefix hq) whose routes.jsonl routes gt- to a
// rig workspace (prefix gt), each with its own database on the shared test
// server: the GH#4647 topology. The town store is the command's store; the rig
// is reached only through routing.
type relateRigs struct {
	ctx       context.Context
	town      *dolt.DoltStore
	rigDBPath string
}

// setupRelateRigs builds the two rigs with hq-src1 in the town and gt-target1
// in the rig. seedRig runs against the rig store before it is closed, so a test
// can stage rig-side rows routing will later find. The command globals and the
// working directory point at the town for the rest of the test.
//
// NOTE: uses os.Chdir and package globals; it cannot run in parallel.
func setupRelateRigs(t *testing.T, seedRig func(context.Context, *dolt.DoltStore)) *relateRigs {
	t.Helper()
	ctx := context.Background()

	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	town := newTestStoreIsolatedDB(t, townDBPath, "hq")
	seedIssue(t, ctx, town, "hq-src1")

	rigDBPath := filepath.Join(rigBeadsDir, "dolt")
	rig := newTestStoreIsolatedDB(t, rigDBPath, "gt")
	seedIssue(t, ctx, rig, "gt-target1")
	if seedRig != nil {
		seedRig(ctx, rig)
	}
	// Release the rig store before routing reopens it.
	rig.Close()

	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDbPath, oldStore, oldRootCtx, oldJSON, oldActor := dbPath, store, rootCtx, jsonOutput, actor
	dbPath, store, rootCtx, jsonOutput, actor = townDBPath, town, ctx, false, "test-actor"
	t.Cleanup(func() { dbPath, store, rootCtx, jsonOutput, actor = oldDbPath, oldStore, oldRootCtx, oldJSON, oldActor })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	return &relateRigs{ctx: ctx, town: town, rigDBPath: rigDBPath}
}

// openRig reopens the rig store, so what a test reads there is what the
// command made durable rather than what a shared handle still holds.
func (r *relateRigs) openRig(t *testing.T) *dolt.DoltStore {
	t.Helper()
	rig, err := openExistingTestDB(t, r.rigDBPath)
	if err != nil {
		t.Fatalf("reopen rig store: %v", err)
	}
	t.Cleanup(func() { rig.Close() })
	return rig
}

type relateEdgeReader interface {
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)
}

func hasRelatesTo(t *testing.T, ctx context.Context, s relateEdgeReader, from, to string) bool {
	t.Helper()
	deps, err := s.GetDependencyRecords(ctx, from)
	if err != nil {
		t.Fatalf("GetDependencyRecords(%s): %v", from, err)
	}
	for _, d := range deps {
		if d.DependsOnID == to && d.Type == types.DepRelatesTo {
			return true
		}
	}
	return false
}

func countEvents(t *testing.T, ctx context.Context, s relateEdgeReader, issueID string, kind types.EventType) int {
	t.Helper()
	events, err := s.GetEvents(ctx, issueID, 100)
	if err != nil {
		t.Fatalf("GetEvents(%s): %v", issueID, err)
	}
	n := 0
	for _, e := range events {
		if e.EventType == kind {
			n++
		}
	}
	return n
}

// TestRelateCrossPrefixWritesEachHalfInItsSourceRig (GH#4647, AC-1): bd relate
// across two routed rigs writes each relates-to half, with its
// dependency_added event, into the store of that half's source issue, and
// prints the usual success line.
func TestRelateCrossPrefixWritesEachHalfInItsSourceRig(t *testing.T) {
	r := setupRelateRigs(t, nil)

	var runErr error
	out := captureStdout(t, func() error {
		runErr = runRelate(relateCmd, []string{"hq-src1", "gt-target1"})
		return nil
	})
	if runErr != nil {
		t.Fatalf("bd relate hq-src1 gt-target1: %v", runErr)
	}
	if !strings.Contains(out, "Linked hq-src1 ↔ gt-target1") {
		t.Errorf("success output = %q, want the Linked hq-src1 ↔ gt-target1 line", out)
	}

	if !hasRelatesTo(t, r.ctx, r.town, "hq-src1", "gt-target1") {
		t.Errorf("town store has no hq-src1 -> gt-target1 relates-to edge")
	}
	if got := countEvents(t, r.ctx, r.town, "hq-src1", types.EventDependencyAdded); got != 1 {
		t.Errorf("town dependency_added events for hq-src1 = %d, want 1", got)
	}
	if hasRelatesTo(t, r.ctx, r.town, "gt-target1", "hq-src1") {
		t.Errorf("town store holds the rig's gt-target1 -> hq-src1 half; it belongs in the rig store")
	}

	rig := r.openRig(t)
	if !hasRelatesTo(t, r.ctx, rig, "gt-target1", "hq-src1") {
		t.Errorf("rig store has no gt-target1 -> hq-src1 relates-to edge")
	}
	if got := countEvents(t, r.ctx, rig, "gt-target1", types.EventDependencyAdded); got != 1 {
		t.Errorf("rig dependency_added events for gt-target1 = %d, want 1", got)
	}
}

// TestUnrelateCrossPrefixRemovesBothHalves (GH#4647, AC-2): bd unrelate on a
// cross-rig pair removes each half from its source's store and records a
// dependency_removed event in each.
func TestUnrelateCrossPrefixRemovesBothHalves(t *testing.T) {
	r := setupRelateRigs(t, func(ctx context.Context, rig *dolt.DoltStore) {
		if err := rig.AddDependency(ctx, &types.Dependency{IssueID: "gt-target1", DependsOnID: "hq-src1", Type: types.DepRelatesTo}, "test"); err != nil {
			t.Fatalf("seed rig half: %v", err)
		}
	})
	if err := r.town.AddDependency(r.ctx, &types.Dependency{IssueID: "hq-src1", DependsOnID: "gt-target1", Type: types.DepRelatesTo}, "test"); err != nil {
		t.Fatalf("seed town half: %v", err)
	}

	var runErr error
	out := captureStdout(t, func() error {
		runErr = runUnrelate(unrelateCmd, []string{"hq-src1", "gt-target1"})
		return nil
	})
	if runErr != nil {
		t.Fatalf("bd unrelate hq-src1 gt-target1: %v", runErr)
	}
	if !strings.Contains(out, "Unlinked hq-src1 ↔ gt-target1") {
		t.Errorf("success output = %q, want the Unlinked hq-src1 ↔ gt-target1 line", out)
	}

	if hasRelatesTo(t, r.ctx, r.town, "hq-src1", "gt-target1") {
		t.Errorf("town hq-src1 -> gt-target1 relates-to edge survived bd unrelate")
	}
	if got := countEvents(t, r.ctx, r.town, "hq-src1", types.EventDependencyRemoved); got != 1 {
		t.Errorf("town dependency_removed events for hq-src1 = %d, want 1", got)
	}
	rig := r.openRig(t)
	if hasRelatesTo(t, r.ctx, rig, "gt-target1", "hq-src1") {
		t.Errorf("rig gt-target1 -> hq-src1 relates-to edge survived bd unrelate")
	}
	if got := countEvents(t, r.ctx, rig, "gt-target1", types.EventDependencyRemoved); got != 1 {
		t.Errorf("rig dependency_removed events for gt-target1 = %d, want 1", got)
	}
}
