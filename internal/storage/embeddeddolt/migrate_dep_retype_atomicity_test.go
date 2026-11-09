//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Real-Dolt coverage for the 0032 retype repair's transaction boundary
// (preMigrationRepairInDriverTx, internal/storage/schema), GH#8299's AC-8.
//
// The repair's fault hook (schema.SetDepRetypeFaultHookForTest) injects a
// failure at each stage boundary — after the plan, after the conversions,
// after the recompute — which is the observable seam for every way a pass can
// die before the repair transaction's commit: an error, a canceled context,
// a killed process (Dolt rolls the open transaction back on disconnect
// exactly as the SQL layer does on error, and the pass-level kill semantics
// are migrate_selfheal_test.go's coverage; the boundary is what makes them
// equivalent). A fault anywhere before the commit must leave the store
// exactly as it was — rows external, sources unblocked, marker pending, no
// new Dolt commit, clean working set — and a plain retry must converge.
//
// The success leg pins the other half: the repair transaction commits as a
// SQL transaction while MigrateUp's suspended @@SESSION.dolt_transaction_commit
// keeps it from minting a Dolt commit, so the pass's one staged
// "schema: apply migrations" commit is the only history the upgrade writes.

// seedRetypeStore seeds a migrated store holding the minimal legacy shape:
// one issue whose misfiled external row names a local issue target. It
// returns the dataDir, a connection to it, and the row's derived id.
func seedRetypeStore(t *testing.T, ctx context.Context, source, target string) (string, *sql.Conn, func()) {
	t.Helper()
	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)
	seedIssue(t, ctx, conn, source)
	seedIssue(t, ctx, conn, target)
	seedDependency(t, ctx, conn, depid.New(source, target), source, nil, nil, strptr(target))
	commitSeed(t, ctx, conn, "seed a legacy external dep row")
	return dataDir, conn, closeConn
}

// TestEmbeddedDepRetypeFaultRollsBackAndRetriesPerStage is AC-8's rollback
// half: a fault at any of the repair's three stage boundaries leaves no
// debris of any kind, and the next open retries cleanly.
func TestEmbeddedDepRetypeFaultRollsBackAndRetriesPerStage(t *testing.T) {
	requireEmbedded(t)

	for _, stage := range []string{"planned", "converted", "recomputed"} {
		t.Run(stage, func(t *testing.T) {
			ctx := t.Context()
			const source = "hq-src"
			const target = "ops-tgt"
			rowID := depid.New(source, target)
			dataDir, conn, closeConn := seedRetypeStore(t, ctx, source, target)

			beforeLog := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
			beforeHead := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
			// The baseline the no-debris assertions compare against: whatever
			// dolt_status already holds before the pass (the embedded engine's
			// own local bookkeeping, e.g. its dolt_ignored leases rows, is
			// the store's, not the pass's).
			beforeDirty := dirtyTableNames(t, ctx, conn)

			unrecordIgnoredVersionsFrom(t, ctx, conn, 32)
			closeConn()

			conn2, _ := openDepConn(t, ctx, dataDir)
			errInjected := errors.New("injected fault at stage " + stage)
			restoreHook := schema.SetDepRetypeFaultHookForTest(func(_ context.Context, _ schema.DBConn, at string) error {
				if at == stage {
					return errInjected
				}
				return nil
			})
			_, err := schema.MigrateUp(ctx, conn2)
			restoreHook()
			if !errors.Is(err, errInjected) {
				t.Fatalf("MigrateUp error = %v, want the injected fault", err)
			}

			// No debris: the rows, the blocked state, the marker, the
			// history and the working set are all exactly as they were.
			got := indexDepRows(t, readDeps(t, ctx, conn2))
			if len(got) != 1 || got[rowID] != (depRow{id: rowID, issueID: source, dependsOnExternal: target}) {
				t.Fatalf("dependencies after the fault = %+v, want the seeded external row untouched", got)
			}
			if blocked := scalarInt(t, ctx, conn2, "SELECT is_blocked FROM issues WHERE id = ?", source); blocked != 0 {
				t.Fatalf("source %s is_blocked = %d after the fault, want 0: a rolled-back recompute must leave nothing behind", source, blocked)
			}
			if ignoredVersionRecorded(t, ctx, conn2, 32) {
				t.Fatal("marker 32 recorded despite the fault: the next open would skip the repair")
			}
			if n := scalarInt(t, ctx, conn2, "SELECT COUNT(*) FROM dolt_log"); n != beforeLog {
				t.Fatalf("dolt_log grew from %d to %d across the failed pass, want no new commit", beforeLog, n)
			}
			if head := scalarString(t, ctx, conn2, "SELECT commit_hash FROM dolt_log LIMIT 1"); head != beforeHead {
				t.Fatalf("HEAD moved across the failed pass: %s -> %s", beforeHead, head)
			}
			if dirty := dirtyTableNames(t, ctx, conn2); !slices.Equal(dirty, beforeDirty) {
				t.Fatalf("working set after the fault = %v, want the pre-pass baseline %v: the rolled-back repair must leave no debris", dirty, beforeDirty)
			}

			// The next open retries the repair and converges.
			if _, err := schema.MigrateUp(ctx, conn2); err != nil {
				t.Fatalf("retry MigrateUp: %v", err)
			}
			if !ignoredVersionRecorded(t, ctx, conn2, 32) {
				t.Fatal("retry did not record marker 32")
			}
			got = indexDepRows(t, readDeps(t, ctx, conn2))
			if len(got) != 1 || got[rowID] != (depRow{id: rowID, issueID: source, dependsOnIssueID: target}) {
				t.Fatalf("dependencies after the retry = %+v, want the row re-typed", got)
			}
			if blocked := scalarInt(t, ctx, conn2, "SELECT is_blocked FROM issues WHERE id = ?", source); blocked != 1 {
				t.Fatalf("source %s is_blocked = %d after the retry, want 1", source, blocked)
			}
		})
	}
}

// TestEmbeddedDepRetypeCommitMintsNoDoltCommit is AC-8's success half: the
// repair's SQL Commit lands the conversions without minting a Dolt commit —
// the pass's suspended dolt_transaction_commit owns that — so history gains
// exactly the one staged "schema: apply migrations" commit, the working set
// settles clean, and the pinned session's original setting is restored.
func TestEmbeddedDepRetypeCommitMintsNoDoltCommit(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	const source = "hq-src"
	const target = "ops-tgt"
	rowID := depid.New(source, target)
	dataDir, conn, closeConn := seedRetypeStore(t, ctx, source, target)

	beforeLog := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
	beforeDirty := dirtyTableNames(t, ctx, conn)
	unrecordIgnoredVersionsFrom(t, ctx, conn, 32)
	beforeSessionCommit := sessionTransactionCommit(t, ctx, conn)
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp with ignored 0032 pending: %v", err)
	}
	if !ignoredVersionRecorded(t, ctx, conn2, 32) {
		t.Fatal("ignored 0032 was not recorded")
	}
	if row := indexDepRows(t, readDeps(t, ctx, conn2))[rowID]; row != (depRow{id: rowID, issueID: source, dependsOnIssueID: target}) {
		t.Fatalf("dependencies row %s = %+v, want the external target re-typed", rowID, row)
	}
	if blocked := scalarInt(t, ctx, conn2, "SELECT is_blocked FROM issues WHERE id = ?", source); blocked != 1 {
		t.Fatalf("source %s is_blocked = %d, want 1", source, blocked)
	}
	// Exactly one new commit — the pass's staged one. If the repair's SQL
	// Commit had minted a Dolt commit, this would be two (and the retry
	// order the pass documents would break).
	if n := scalarInt(t, ctx, conn2, "SELECT COUNT(*) FROM dolt_log"); n != beforeLog+1 {
		t.Fatalf("dolt_log grew from %d to %d, want exactly the pass's one staged commit", beforeLog, n)
	}
	if msg := scalarString(t, ctx, conn2, "SELECT message FROM dolt_log LIMIT 1"); msg != "schema: apply migrations" {
		t.Fatalf("HEAD commit message = %q, want the pass's staged \"schema: apply migrations\" commit", msg)
	}
	if dirty := dirtyTableNames(t, ctx, conn2); !slices.Equal(dirty, beforeDirty) {
		t.Fatalf("working set after the pass = %v, want the pre-pass baseline %v", dirty, beforeDirty)
	}
	if got := sessionTransactionCommit(t, ctx, conn2); got != beforeSessionCommit {
		t.Fatalf("@@SESSION.dolt_transaction_commit after MigrateUp = %d, want the original %d", got, beforeSessionCommit)
	}
}
