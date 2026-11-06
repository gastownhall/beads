//go:build cgo

package embeddeddolt_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin AC-7 of gastownhall/beads#7037: the interruption contract
// around repairMixedIsBlockedFlags0032's owed branch. The fault seam
// (schema.SetIsBlockedRepairFaultHookForTest) aborts the repair at its
// durability boundaries; every interruption must leave the store in a state
// the contract names — still owing the whole repair, or having already
// published it durably — and the next open must converge without ever
// silently folding mid-pass debris into a repair commit. The operator's half
// of the contract is exercised with the scoped DOLT_ADD/DOLT_COMMIT the
// refusal text prescribes (bd dolt commit), scoped by table so nothing else
// rides the reconcile.

// TestEmbeddedMigrate0075RefusalThenReconcileCompletesRepair walks the
// contract's front door: a pre-existing user edit to issues refuses the owed
// repair typed, the operator reconciles the edit, and the retry completes the
// whole repair — flags converged, obligation consumed, both cursors recorded,
// the user's edit and the repaired rows' historical updated_at intact.
func TestEmbeddedMigrate0075RefusalThenReconcileCompletesRepair(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedOversetCrossingStore(t)

	// Open 1: the user's uncommitted issues edit meets an owed repair — the
	// typed refusal fires before any repair write. The edit guards
	// updated_at the way every fixture here does, so the stamp assertion in
	// open 2 pins the repair's own behavior, not the edit's stamping.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "UPDATE issues SET title = 'user edit', updated_at = updated_at WHERE id = 'rf-related'")
		_, err := schema.MigrateUp(ctx, conn)
		var dirtyErr *schema.DirtyTablesError
		if !errors.As(err, &dirtyErr) {
			t.Fatalf("MigrateUp with a dirty issues table = %v, want a *schema.DirtyTablesError", err)
		}
		if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != "issues" {
			t.Errorf("refusal names %v, want exactly [issues]", dirtyErr.Tables)
		}
	})

	// The operator reconciles the way the refusal's recovery text says
	// (bd dolt commit), scoped to the edit's table so nothing else rides the
	// commit.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('issues')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: reconcile the user edit')")
	})

	// Open 2: the retry completes the whole repair.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp after the reconcile: %v", err)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related still blocked after the completed repair")
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-blocked"); !got {
			t.Errorf("rf-blocked lost its genuine blocked flag in the repair")
		}
		if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
			t.Fatalf("counting is_blocked inconsistencies: %v", err)
		} else if corrected != 0 {
			t.Errorf("full recompute still corrects %d row(s) after the repair", corrected)
		}
		requireKeyConsumedAtHead(t, ctx, conn)
		if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 75 {
			t.Errorf("main cursor = %d after the repair, want 75", got)
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Errorf("ignored cursor row 32 missing after the repair")
		}
		// The user's edit survived the refusal, the reconcile and the repair,
		// and the repaired row kept its historical updated_at.
		var title string
		if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = 'rf-related'").Scan(&title); err != nil {
			t.Fatalf("read rf-related title: %v", err)
		}
		if title != "user edit" {
			t.Errorf("user edit lost across refusal, reconcile and repair (title = %q)", title)
		}
		requireStamp(t, ctx, conn, "issues", "rf-related", historicalStamp(3))
	})
}

// TestEmbeddedMigrate0075KeyDeletionFaultReconcilesDurableRepair pins the
// sharpest interruption: a crash after the obligation key's deletion but
// before the scoped repair commit. The flips and the deletion sit uncommitted
// while HEAD still owes the whole repair, so the next open must refuse typed
// over the debris (naming the graph tables it dirtied: this pass's dependency
// rekey output, the uncommitted flips, the uncommitted deletion) instead of
// silently completing, and the operator's reconcile publishes the finished
// repair durably — after which the retry runs 0032's debt-free wisp-only pass
// and mints no repair commit of its own.
func TestEmbeddedMigrate0075KeyDeletionFaultReconcilesDurableRepair(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedOversetCrossingStore(t)
	errInterrupt := errors.New("simulated crash between the key deletion and the repair commit")

	// Open 1: interrupted after the key deletion.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		schema.SetIsBlockedRepairFaultHookForTest(func(window string) error {
			if window == "after-key-deletion" {
				return errInterrupt
			}
			return nil
		})
		defer schema.SetIsBlockedRepairFaultHookForTest(nil)

		_, err := schema.MigrateUp(ctx, conn)
		if !errors.Is(err, errInterrupt) {
			t.Fatalf("MigrateUp = %v, want the injected after-key-deletion fault", err)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = 'is_blocked_repair_owed_0075'"); got != 1 {
			t.Fatalf("obligation key at HEAD = %d after the fault, want 1 (HEAD still owes the whole repair)", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075'"); got != 0 {
			t.Errorf("working set holds %d obligation key row(s); the deletion ran before the fault", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'rf-related' AND is_blocked"); got != 1 {
			t.Errorf("rf-related over-set at HEAD = %d after the fault, want 1 (the flips are uncommitted)", got)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related flips missing from the working set; the recompute ran before the fault")
		}
		if ignoredCursorHas(t, ctx, conn, 32) {
			t.Errorf("ignored cursor row 32 recorded despite the fault")
		}
	})

	// Open 2: the debris refuses — typed, naming exactly the three graph
	// tables the interrupted repair left dirty.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		_, err := schema.MigrateUp(ctx, conn)
		var dirtyErr *schema.DirtyTablesError
		if !errors.As(err, &dirtyErr) {
			t.Fatalf("MigrateUp over the interrupted repair's debris = %v, want a *schema.DirtyTablesError", err)
		}
		want := []string{"dependencies", "issues", "metadata"}
		if len(dirtyErr.Tables) != len(want) {
			t.Fatalf("refusal names %v, want %v", dirtyErr.Tables, want)
		}
		for i, table := range want {
			if dirtyErr.Tables[i] != table {
				t.Fatalf("refusal names %v, want %v", dirtyErr.Tables, want)
			}
		}
	})

	// The operator reconciles: publishing the working set makes the finished
	// repair durable at HEAD.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('dependencies', 'issues', 'metadata')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: reconcile the interrupted repair')")
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = 'is_blocked_repair_owed_0075'"); got != 0 {
			t.Fatalf("obligation key at HEAD = %d after the reconcile, want 0 (the repair is now durable)", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'rf-related' AND is_blocked"); got != 0 {
			t.Errorf("rf-related still over-set at HEAD after the reconcile; the flips must be durable", got)
		}
	})

	// Open 3: nothing owed at HEAD any more — the pending 0032 runs its
	// debt-free wisp-only pass, records its cursor, and mints no commit: the
	// durable publish came from the operator's reconcile, not a second
	// repair.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		commits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp after the reconcile: %v", err)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != commits {
			t.Errorf("debt-free retry minted %d commit(s) (dolt_log %d -> %d); the wisp-only pass writes no tracked table", got-commits, commits, got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = '"+repairCommitMessage+"'"); got != 0 {
			t.Errorf("repair commit minted %d time(s); the reconcile already published the repair", got)
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Errorf("ignored cursor row 32 missing after the debt-free retry")
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related blocked again after the retry; the durable repair must hold")
		}
		requireKeyConsumedAtHead(t, ctx, conn)
	})
}

// TestEmbeddedMigrate0075PostCommitFaultRetryIsWispOnly pins the interruption
// AFTER the scoped repair commit: the repair itself is durable at HEAD (key
// consumed, flags published) while 0032's cursor row never recorded. The
// retry owes nothing tracked, so it runs 0032's debt-free wisp-only pass and
// records the cursor without minting a second repair commit, and a third open
// is a pure no-op.
func TestEmbeddedMigrate0075PostCommitFaultRetryIsWispOnly(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedOversetCrossingStore(t)
	errInterrupt := errors.New("simulated crash after the repair commit")

	// Open 1: the scoped repair commit is durable; the pass dies before the
	// version-32 cursor row records.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		schema.SetIsBlockedRepairFaultHookForTest(func(window string) error {
			if window == "after-commit" {
				return errInterrupt
			}
			return nil
		})
		defer schema.SetIsBlockedRepairFaultHookForTest(nil)

		_, err := schema.MigrateUp(ctx, conn)
		if !errors.Is(err, errInterrupt) {
			t.Fatalf("MigrateUp = %v, want the injected after-commit fault", err)
		}
		requireKeyConsumedAtHead(t, ctx, conn)
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'rf-related' AND is_blocked"); got != 0 {
			t.Errorf("rf-related still over-set at HEAD after the fault; the repair commit is durable", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = '"+repairCommitMessage+"'"); got != 1 {
			t.Errorf("exactly one repair commit expected at the fault, found %d", got)
		}
		if ignoredCursorHas(t, ctx, conn, 32) {
			t.Errorf("ignored cursor row 32 recorded despite the fault")
		}
	})

	// Open 2: the retry is wisp-only — cursor 32 records, no second repair
	// commit, no other commit either.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		commits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp retry after the after-commit fault: %v", err)
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Errorf("ignored cursor row 32 missing after the retry")
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = '"+repairCommitMessage+"'"); got != 1 {
			t.Errorf("retry minted a second repair commit (%d total)", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != commits {
			t.Errorf("wisp-only retry minted %d commit(s) (dolt_log %d -> %d)", got-commits, commits, got)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related blocked again after the retry")
		}
		if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
			t.Fatalf("counting is_blocked inconsistencies: %v", err)
		} else if corrected != 0 {
			t.Errorf("full recompute still corrects %d row(s) after the retry", corrected)
		}
	})

	// Open 3: converged — no work, no commit.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		commits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
		applied, err := schema.MigrateUp(ctx, conn)
		if err != nil {
			t.Fatalf("MigrateUp on the converged store: %v", err)
		}
		if applied != 0 {
			t.Errorf("converged store applied %d migration(s), want 0", applied)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != commits {
			t.Errorf("converged open minted %d commit(s) (dolt_log %d -> %d)", got-commits, commits, got)
		}
	})
}

// TestEmbeddedMigrate0075ReceivedKeyCompletesOnOrdinaryOpen pins the
// received-obligation completion: a clone whose cursors both claim current
// (main 75, ignored 32) but whose HEAD carries the committed obligation key —
// the state a crashed origin pushed — repairs on its next ordinary open. The
// pass-level re-arm deletes the planted cursor 32, the repair runs owed, and
// the cursor re-records with the real content hash of the 0032 marker file;
// the reopen after it is idle.
func TestEmbeddedMigrate0075ReceivedKeyCompletesOnOrdinaryOpen(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedReceivedKeyStore(t)

	sqlText, err := schema.IgnoredMigrationSQL("0032_recompute_mixed_is_blocked_dolt11886.up.sql")
	if err != nil {
		t.Fatalf("read 0032 marker: %v", err)
	}
	sum := sha256.Sum256([]byte(sqlText))
	wantHash := hex.EncodeToString(sum[:])

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp with a received obligation: %v", err)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related still blocked after the received-key repair")
		}
		if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
			t.Fatalf("counting is_blocked inconsistencies: %v", err)
		} else if corrected != 0 {
			t.Errorf("full recompute still corrects %d row(s) after the received-key repair", corrected)
		}
		requireKeyConsumedAtHead(t, ctx, conn)
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = '"+repairCommitMessage+"'"); got != 1 {
			t.Errorf("repair commit minted %d time(s), want exactly 1", got)
		}
		// The re-arm made 0032 pending again and the runner re-recorded it
		// with the marker file's real content hash.
		var hash string
		if err := conn.QueryRowContext(ctx,
			"SELECT content_hash FROM ignored_schema_migrations WHERE version = 32").Scan(&hash); err != nil {
			t.Fatalf("read the re-recorded cursor row 32: %v", err)
		}
		if hash != wantHash {
			t.Errorf("cursor row 32 content_hash = %q, want the sha256 of the 0032 marker file (%q)", hash, wantHash)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 75 {
			t.Errorf("main cursor = %d after the received-key repair, want 75", got)
		}
	})

	// The reopen is idle: no work, no commit.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		commits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
		applied, err := schema.MigrateUp(ctx, conn)
		if err != nil {
			t.Fatalf("MigrateUp on the converged store: %v", err)
		}
		if applied != 0 {
			t.Errorf("converged reopen applied %d migration(s), want 0", applied)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != commits {
			t.Errorf("converged reopen minted %d commit(s) (dolt_log %d -> %d)", got-commits, commits, got)
		}
	})
}

// TestEmbeddedMigrate0075ReceivedKeyNeedsNoFreshConsent pins the
// consent-continuation half of the contract (migrate_consent.go): the
// obligation key main 0075 commits is part of the upgrade the operator
// already consented to, so finishing it on an ordinary open — reads
// included, no fresh prompt, even with both cursors already current — is
// continuation, not a new migration decision. The crossing half (pending
// main + no consent refuses before the key is minted) is pinned by
// TestEmbeddedMigrate0075ConsentRefusalPrecedesRepairWrites.
func TestEmbeddedMigrate0075ReceivedKeyNeedsNoFreshConsent(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedReceivedKeyStore(t)

	// Package-wide consent is on (TestMain); this test must see the gate.
	schema.SetLocalMigrateConsent(false)
	t.Cleanup(func() { schema.SetLocalMigrateConsent(true) })
	t.Setenv(schema.AllowMigrateEnv, "")

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp without consent = %v, want the received-key repair to run as continuation of the consented upgrade", err)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related still blocked after the repair")
		}
		requireKeyConsumedAtHead(t, ctx, conn)
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Errorf("ignored cursor row 32 missing after the repair")
		}
	})
}
