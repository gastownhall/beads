//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
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

// TestRelateCrossPrefixUnresolvableEndpointWritesNothing (GH#4647, AC-3): when
// one endpoint resolves nowhere, bd relate names THAT endpoint and writes no
// edge or event into either store, even though the other endpoint resolved
// through a route first.
func TestRelateCrossPrefixUnresolvableEndpointWritesNothing(t *testing.T) {
	r := setupRelateRigs(t, nil)

	err := runRelate(relateCmd, []string{"gt-target1", "hq-missing"})
	if err == nil {
		t.Fatalf("bd relate gt-target1 hq-missing succeeded; want a resolution error")
	}
	if !strings.Contains(err.Error(), "failed to resolve hq-missing") {
		t.Errorf("error = %q, want it to name the unresolvable endpoint hq-missing", err)
	}

	if deps, _ := r.town.GetDependencyRecords(r.ctx, "hq-src1"); len(deps) != 0 {
		t.Errorf("town hq-src1 gained edges after a failed relate: %+v", deps)
	}
	rig := r.openRig(t)
	deps, err := rig.GetDependencyRecords(r.ctx, "gt-target1")
	if err != nil {
		t.Fatalf("rig GetDependencyRecords: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("rig gt-target1 gained edges after a failed relate: %+v", deps)
	}
	if got := countEvents(t, r.ctx, rig, "gt-target1", types.EventDependencyAdded); got != 0 {
		t.Errorf("rig dependency_added events for gt-target1 = %d, want 0", got)
	}
}

// seedRigBlocksEdge stages gt-target1 -> hq-src1 as a blocks edge in the rig:
// the cross-rig blocker a relates-to half in that direction cannot replace.
func seedRigBlocksEdge(t *testing.T) func(context.Context, *dolt.DoltStore) {
	t.Helper()
	return func(ctx context.Context, rig *dolt.DoltStore) {
		if err := rig.AddDependency(ctx, &types.Dependency{IssueID: "gt-target1", DependsOnID: "hq-src1", Type: types.DepBlocks}, "test"); err != nil {
			t.Fatalf("seed rig blocks edge: %v", err)
		}
	}
}

// assertRigKeepsOnlyBlocksEdge fails unless the rig still holds exactly the
// seeded gt-target1 -> hq-src1 blocks edge, with no dependency event of kind.
func assertRigKeepsOnlyBlocksEdge(t *testing.T, r *relateRigs, kind types.EventType) {
	t.Helper()
	rig := r.openRig(t)
	deps, err := rig.GetDependencyRecords(r.ctx, "gt-target1")
	if err != nil {
		t.Fatalf("rig GetDependencyRecords: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "hq-src1" || deps[0].Type != types.DepBlocks {
		t.Errorf("rig gt-target1 edges = %+v, want only the seeded blocks edge to hq-src1", deps)
	}
	if got := countEvents(t, r.ctx, rig, "gt-target1", kind); got != 0 {
		t.Errorf("rig %s events for gt-target1 = %d, want 0", kind, got)
	}
}

// TestRelateCrossPrefixRefusesOverAnotherEdgeType: when the rig already holds
// gt-target1 -> hq-src1 as a blocks edge, a cross-rig bd relate refuses before
// writing either half, in either argument order, names that edge, and leaves
// both stores as they were. Writing the town half first would leave a
// one-sided link that no rerun completes: the rig half is refused every time.
func TestRelateCrossPrefixRefusesOverAnotherEdgeType(t *testing.T) {
	for _, args := range [][]string{{"hq-src1", "gt-target1"}, {"gt-target1", "hq-src1"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			r := setupRelateRigs(t, seedRigBlocksEdge(t))

			err := runRelate(relateCmd, args)
			if err == nil {
				t.Fatalf("bd relate %s succeeded over a blocks edge; want a refusal", strings.Join(args, " "))
			}
			for _, want := range []string{`gt-target1 -> hq-src1 already exists with type "blocks"`, "nothing was written"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "rerun") {
				t.Errorf("error = %q advises a rerun, which can never finish this link", err)
			}

			deps, err := r.town.GetDependencyRecords(r.ctx, "hq-src1")
			if err != nil {
				t.Fatalf("town GetDependencyRecords: %v", err)
			}
			if len(deps) != 0 {
				t.Errorf("town hq-src1 gained edges after a refused relate: %+v", deps)
			}
			if got := countEvents(t, r.ctx, r.town, "hq-src1", types.EventDependencyAdded); got != 0 {
				t.Errorf("town dependency_added events for hq-src1 = %d, want 0", got)
			}
			assertRigKeepsOnlyBlocksEdge(t, r, types.EventDependencyAdded)
		})
	}
}

// TestUnrelateCrossPrefixKeepsAnotherEdgeType: bd unrelate removes only the
// relates-to halves of a cross-rig pair. With hq-src1 -> gt-target1 a
// relates-to half in the town and gt-target1 -> hq-src1 a blocks edge in the
// rig, it removes the town half, keeps the blocker, and names it on stderr,
// never on stdout, which --json owns.
func TestUnrelateCrossPrefixKeepsAnotherEdgeType(t *testing.T) {
	r := setupRelateRigs(t, seedRigBlocksEdge(t))
	if err := r.town.AddDependency(r.ctx, &types.Dependency{IssueID: "hq-src1", DependsOnID: "gt-target1", Type: types.DepRelatesTo}, "test"); err != nil {
		t.Fatalf("seed town half: %v", err)
	}

	var runErr error
	stdout, stderr := captureStdio(t, func() {
		runErr = runUnrelate(unrelateCmd, []string{"hq-src1", "gt-target1"})
	})
	if runErr != nil {
		t.Fatalf("bd unrelate hq-src1 gt-target1: %v", runErr)
	}
	if !strings.Contains(stdout, "Unlinked hq-src1 ↔ gt-target1") {
		t.Errorf("stdout = %q, want the Unlinked hq-src1 ↔ gt-target1 line", stdout)
	}
	const kept = `gt-target1 -> hq-src1 is a "blocks" edge, not relates-to; left in place`
	if !strings.Contains(stderr, kept) {
		t.Errorf("stderr = %q, want it to name the kept edge: %q", stderr, kept)
	}
	if strings.Contains(stdout, "left in place") {
		t.Errorf("stdout = %q carries the kept-edge warning; it belongs on stderr", stdout)
	}

	if hasRelatesTo(t, r.ctx, r.town, "hq-src1", "gt-target1") {
		t.Errorf("town hq-src1 -> gt-target1 relates-to half survived bd unrelate")
	}
	if got := countEvents(t, r.ctx, r.town, "hq-src1", types.EventDependencyRemoved); got != 1 {
		t.Errorf("town dependency_removed events for hq-src1 = %d, want 1", got)
	}
	assertRigKeepsOnlyBlocksEdge(t, r, types.EventDependencyRemoved)
}

// TestRelateHalfErrorAdvice: when the second cross-store half fails, the first
// is already committed. A failure a rerun can get past advises the rerun; a
// type conflict, which a rerun would meet again, advises undoing the first
// half with bd dep unrelate instead, and stays matchable with errors.As.
func TestRelateHalfErrorAdvice(t *testing.T) {
	ends := [2]relateEndpoint{{id: "hq-src1"}, {id: "gt-target1"}}
	conflict := &issueops.DependencyTypeConflictError{IssueID: "gt-target1", DependsOnID: "hq-src1", ExistingType: "blocks", RequestedType: "relates-to"}
	cases := []struct {
		name    string
		half    int
		remove  bool
		err     error
		want    []string
		notWant []string
	}{
		{
			name: "first half fails", half: 0, err: errors.New("boom"),
			want:    []string{"failed to add relates-to hq-src1 -> gt-target1: boom", "gt-target1 -> hq-src1 was not attempted"},
			notWant: []string{"rerun", "unrelate"},
		},
		{
			name: "second half fails", half: 1, err: errors.New("connection reset"),
			want: []string{
				"failed to add relates-to gt-target1 -> hq-src1: connection reset",
				"hq-src1 -> gt-target1 is already committed in hq-src1's store",
				"rerun bd dep relate hq-src1 gt-target1 to finish",
			},
			notWant: []string{"unrelate"},
		},
		{
			name: "second half refused", half: 1, err: fmt.Errorf("editor: %w", conflict),
			want: []string{
				"hq-src1 -> gt-target1 is already committed in hq-src1's store",
				"bd dep unrelate hq-src1 gt-target1 removes it",
			},
			notWant: []string{"rerun"},
		},
		{
			name: "second removal fails", half: 1, remove: true, err: errors.New("connection reset"),
			want: []string{
				"failed to remove relates-to gt-target1 -> hq-src1: connection reset",
				"hq-src1 holds no relates-to edge to gt-target1 any more",
				"rerun bd dep unrelate hq-src1 gt-target1 to finish",
			},
			notWant: []string{"already committed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := relateHalfError(ends, tc.half, tc.remove, tc.err)
			for _, want := range tc.want {
				if !strings.Contains(got.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", got, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(got.Error(), notWant) {
					t.Errorf("error = %q, want no %q", got, notWant)
				}
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("error = %q does not wrap the half's own error", got)
			}
		})
	}
	var asConflict *issueops.DependencyTypeConflictError
	if !errors.As(relateHalfError(ends, 1, false, conflict), &asConflict) {
		t.Errorf("a refused second half no longer unwraps to *DependencyTypeConflictError")
	}
}
