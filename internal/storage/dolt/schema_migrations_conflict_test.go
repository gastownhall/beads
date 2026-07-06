package dolt

import (
	"database/sql"
	"strings"
	"testing"
)

// setupSchemaMigrationsMergeConflict seeds a synthetic cursor row for the same
// (future) migration version on the current branch and on a peer branch with
// the given per-branch content hashes ("" inserts NULL, the pre-#4270 vintage),
// leaving the peer merge for the caller. Mirrors setupDependencyMergeConflict.
func setupSchemaMigrationsMergeConflict(t *testing.T, ourHash, theirHash string) (*DoltStore, string, int) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	db := store.db
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	// A version far above LatestVersion so the synthetic row can never collide
	// with real migration rows on the shared test database.
	const version = 99990

	insert := func(hash string) {
		var err error
		if hash == "" {
			_, err = db.ExecContext(ctx,
				"INSERT INTO schema_migrations (version) VALUES (?)", version)
		} else {
			_, err = db.ExecContext(ctx,
				"INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", version, hash)
		}
		if err != nil {
			t.Fatalf("insert cursor row (hash=%q): %v", hash, err)
		}
	}

	// Anchor commit so the peer branch can fork from the shared ancestor.
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-Am', 'seed ancestor')"); err != nil {
		t.Fatalf("commit ancestor: %v", err)
	}

	insert(ourHash)
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'cursor row on current')"); err != nil {
		t.Fatalf("commit cursor row on current: %v", err)
	}

	peerBranch := currentBranch + "_peer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer branch: %v", err)
	}
	insert(theirHash)
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'cursor row on peer')"); err != nil {
		t.Fatalf("commit cursor row on peer: %v", err)
	}

	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}
	return store, peerBranch, version
}

func mergeAndResolveSchemaMigrations(t *testing.T, store *DoltStore, peerBranch string) bool {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		// Some Dolt versions report the conflict as a merge error; the resolver
		// inspects dolt_conflicts regardless.
		t.Logf("merge returned: %v", err)
	}

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("resolver error: %v", err)
	}
	if !resolved {
		_ = tx.Rollback()
		return false
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after resolve: %v", err)
	}
	return true
}

func readCursorHash(t *testing.T, store *DoltStore, version int) string {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	var hash sql.NullString
	if err := store.db.QueryRowContext(ctx,
		"SELECT content_hash FROM schema_migrations WHERE version = ?", version).Scan(&hash); err != nil {
		t.Fatalf("read cursor row: %v", err)
	}
	return hash.String
}

// TestTryAutoResolveMergeConflicts_SchemaMigrationsTheirHashWins verifies the
// mixed-vintage cursor-row conflict (bd-6dnrw.29): ours recorded (version,
// NULL) with a pre-#4270 binary, theirs recorded (version, sha256). The
// conflict resolves automatically and keeps the recorded hash.
func TestTryAutoResolveMergeConflicts_SchemaMigrationsTheirHashWins(t *testing.T) {
	theirHash := strings.Repeat("b", 64)
	store, peerBranch, version := setupSchemaMigrationsMergeConflict(t, "", theirHash)

	if !mergeAndResolveSchemaMigrations(t, store, peerBranch) {
		t.Fatal("expected NULL-vs-hash schema_migrations conflict to be auto-resolved")
	}
	if got := readCursorHash(t, store, version); got != theirHash {
		t.Errorf("content_hash after resolve = %q, want their hash %q", got, theirHash)
	}
}

// TestTryAutoResolveMergeConflicts_SchemaMigrationsOurHashWins is the mirror
// case: ours has the hash, theirs is the pre-#4270 NULL row.
func TestTryAutoResolveMergeConflicts_SchemaMigrationsOurHashWins(t *testing.T) {
	ourHash := strings.Repeat("a", 64)
	store, peerBranch, version := setupSchemaMigrationsMergeConflict(t, ourHash, "")

	if !mergeAndResolveSchemaMigrations(t, store, peerBranch) {
		t.Fatal("expected hash-vs-NULL schema_migrations conflict to be auto-resolved")
	}
	if got := readCursorHash(t, store, version); got != ourHash {
		t.Errorf("content_hash after resolve = %q, want our hash %q", got, ourHash)
	}
}

// TestTryAutoResolveMergeConflicts_SchemaMigrationsRealSkewLeftAlone verifies
// that two DIFFERENT non-empty hashes — the actual #4259 schema fork — are NOT
// auto-resolved.
func TestTryAutoResolveMergeConflicts_SchemaMigrationsRealSkewLeftAlone(t *testing.T) {
	store, peerBranch, _ := setupSchemaMigrationsMergeConflict(t,
		strings.Repeat("a", 64), strings.Repeat("b", 64))

	if mergeAndResolveSchemaMigrations(t, store, peerBranch) {
		t.Fatal("real content skew (two different hashes) must NOT be auto-resolved")
	}
}
