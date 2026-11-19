//go:build cgo

package embeddeddolt_test

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependenciesWithUnresolvableEmbeddedParity is the embedded-store half
// of the GH#5850 combined-read parity contract. It plants the same durable and
// wisp plane fixtures as the server-store half
// (internal/storage/dolt.TestGetDependenciesWithUnresolvableServerParity) —
// including one far end sitting in BOTH planes, the post-merge/promotion state
// a pull can land — and requires the same membership: one result per far-end
// ID, durable precedence for the duplicate, and the old metadata reader equal
// to the resolved portion.
//
// The embedded engine is also the MaxOpenConns=1 configuration this read must
// survive: every nested fetch a reader performs without closing its rows first
// deadlocks this test against its bounded context instead of hanging forever.
func TestGetDependenciesWithUnresolvableEmbeddedParity(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	te := newTestEnv(t, "ux")
	store := te.store

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const anchor = "ux-anchor"
	createEmbeddedIssue(t, ctx, store, anchor, false)
	createEmbeddedIssue(t, ctx, store, "ux-local", false)
	createEmbeddedIssue(t, ctx, store, "ux-wt", true) // a wisp target: hydrates from the wisps plane

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

	// A dangling wisp target: the edge is stored with a depends_on_wisp_id
	// that names no wisp row (the target column carries no foreign key).
	te.exec(t, ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_by) VALUES (?, ?, ?, ?, ?)",
		depid.New(anchor, "ux-dw"), anchor, "ux-dw", types.DepBlocks, "ux-parity")

	// The duplicate far end in BOTH planes. FK checks are relaxed for the
	// wisp-plane row because the anchor is durable — the same
	// post-merge/promotion state the server test seeds. SET FOREIGN_KEY_CHECKS
	// is session-scoped, so the pair runs inside one transaction: a *sql.Tx
	// pins a single connection, where separate ExecContext calls could each
	// draw a fresh one from the pool and drop the flag. The seed connection is
	// closed before the reads below: an open raw handle keeps the embedded
	// database locked, and the store's next call fails to load it.
	db, closeDB, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	seedTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	if _, err := seedTx.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable FK checks: %v", err)
	}
	if _, err := seedTx.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_external, type, created_by) VALUES (?, ?, ?, ?, ?)",
		depid.New(anchor, "external:liveop:dup"), anchor, "external:liveop:dup", types.DepDiscoveredFrom, "ux-wisp-copy"); err != nil {
		t.Fatalf("seed duplicate wisp-plane edge: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
	if err := closeDB(); err != nil {
		t.Fatalf("close seed connection: %v", err)
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
	if want := []string{"ux-local|blocks", "ux-wt|tracks"}; !equalEmbeddedStrings(resolvedIDs, want) {
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
	oldIDs := make([]string, 0, len(old))
	for _, r := range old {
		oldIDs = append(oldIDs, r.ID+"|"+string(r.DependencyType))
	}
	sort.Strings(oldIDs)
	if !equalEmbeddedStrings(oldIDs, []string{"ux-local|blocks", "ux-wt|tracks"}) {
		t.Fatalf("GetDependenciesWithMetadata = %v, want the resolved portion", oldIDs)
	}
}

func createEmbeddedIssue(t *testing.T, ctx context.Context, store *embeddeddolt.EmbeddedDoltStore, id string, ephemeral bool) {
	t.Helper()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        id,
		Title:     "issue " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}, "tester"); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func equalEmbeddedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
