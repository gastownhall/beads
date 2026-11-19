package dolt

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependenciesWithUnresolvableServerParity is the server-store half of
// the GH#5850 combined-read parity contract: the fixture plants durable and
// wisp plane fixtures — including one far end sitting in BOTH planes, the
// post-merge/promotion state a pull can land — and requires the combined
// outgoing read to answer with one result per far-end ID, durable precedence
// for the duplicate, and the old metadata reader equal to the resolved
// portion. The embedded store runs the same fixture
// (TestGetDependenciesWithUnresolvableEmbeddedParity); the two must agree on
// membership because both now delegate to the same issueops body.
func TestGetDependenciesWithUnresolvableServerParity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const anchor = "ux-anchor"
	createPerm(t, ctx, store, anchor)
	createPerm(t, ctx, store, "ux-local")
	createWisp(t, ctx, store, "ux-wt") // a wisp target: hydrates from the wisps plane

	add := func(target string, depType types.DependencyType) {
		t.Helper()
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: anchor, DependsOnID: target, Type: depType,
		}, "ux-parity"); err != nil {
			t.Fatalf("add dependency %s -> %s: %v", anchor, target, err)
		}
	}
	add("ux-local", types.DepBlocks)                    // fully local, durable target
	add("ux-wt", types.DepTracks)                       // local wisp target
	add("liveop-miss", types.DepBlocks)                 // cross-repo prefix: stored, never hydrates
	add("external:liveop:ext", types.DepWaitsFor)       // explicit external reference
	add("external:liveop:dup", types.DepDiscoveredFrom) // the durable half of the duplicate

	// A dangling wisp target: the edge is stored in the durable table with a
	// depends_on_wisp_id that names no wisp row (the target column carries no
	// foreign key). This is the far end that made the old reader silently
	// drop a real edge.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_by) VALUES (?, ?, ?, ?, ?)",
		depid.New(anchor, "ux-dw"), anchor, "ux-dw", types.DepBlocks, "ux-parity"); err != nil {
		t.Fatalf("seed dangling wisp edge: %v", err)
	}

	// The duplicate far end: the same edge in BOTH planes, the state a pull or
	// merge lands when another clone promotes a wisp next to the local copy.
	// FK checks are relaxed for the wisp-plane row because the anchor is
	// durable — mirrors the dependents_records_test.go collision fixture.
	if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable FK checks: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_external, type, created_by) VALUES (?, ?, ?, ?, ?)",
		depid.New(anchor, "external:liveop:dup"), anchor, "external:liveop:dup", types.DepDiscoveredFrom, "ux-wisp-copy"); err != nil {
		t.Fatalf("seed duplicate wisp-plane edge: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("re-enable FK checks: %v", err)
	}

	resolved, unresolvable, err := store.GetDependenciesWithUnresolvable(ctx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvable: %v", err)
	}

	// Resolved: exactly the two local targets, one entry each.
	resolvedIDs := make([]string, 0, len(resolved))
	for _, r := range resolved {
		resolvedIDs = append(resolvedIDs, r.ID+"|"+string(r.DependencyType))
	}
	sort.Strings(resolvedIDs)
	if want := []string{"ux-local|blocks", "ux-wt|tracks"}; !equalStrings(resolvedIDs, want) {
		t.Fatalf("resolved = %v, want %v", resolvedIDs, want)
	}

	// Unresolvable: one record per far-end ID, sorted, exact stored records.
	wantFar := []string{"external:liveop:dup", "external:liveop:ext", "liveop-miss", "ux-dw"}
	if len(unresolvable) != len(wantFar) {
		t.Fatalf("unresolvable = %+v, want %d records", unresolvable, len(wantFar))
	}
	for i, rec := range unresolvable {
		if rec.DependsOnID != wantFar[i] {
			t.Fatalf("unresolvable[%d] far id = %q, want %q (sorted, one per far-end id)", i, rec.DependsOnID, wantFar[i])
		}
		if rec.IssueID != anchor {
			t.Fatalf("unresolvable[%d] issue id = %q, want the anchor %q", i, rec.IssueID, anchor)
		}
	}
	// Durable precedence: the duplicate's stamps come from the durable row
	// ('ux-parity' via AddDependency), never from the wisp-plane copy.
	for _, rec := range unresolvable {
		if rec.DependsOnID == "external:liveop:dup" && rec.CreatedBy != "ux-parity" {
			t.Fatalf("duplicate far end resolved to created_by %q, want the durable row's %q", rec.CreatedBy, "ux-parity")
		}
	}

	// The old metadata reader equals the combined read's resolved portion:
	// relationship consumers still see real issue rows only.
	old, err := store.GetDependenciesWithMetadata(ctx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata: %v", err)
	}
	if len(old) != len(resolved) {
		t.Fatalf("GetDependenciesWithMetadata returned %d rows, want the combined read's %d resolved rows", len(old), len(resolved))
	}
	oldIDs := make([]string, 0, len(old))
	for _, r := range old {
		oldIDs = append(oldIDs, r.ID+"|"+string(r.DependencyType))
	}
	sort.Strings(oldIDs)
	if !equalStrings(oldIDs, []string{"ux-local|blocks", "ux-wt|tracks"}) {
		t.Fatalf("GetDependenciesWithMetadata = %v, want the resolved portion", oldIDs)
	}
}
