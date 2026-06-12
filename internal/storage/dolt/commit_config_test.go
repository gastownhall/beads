package dolt

import (
	"testing"
)

// dirtyTables returns the set of table names currently in dolt_status.
func dirtyTables(t *testing.T, store *DoltStore) map[string]bool {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()

	rows, err := store.db.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_status: %v", err)
	}
	return tables
}

// GH#4078: a config-only working set must be committable. Generic Commit()
// excludes config by design (GH#2455), so intentional config writes
// (bd remember, bd config set) need a scoped commit that stages ONLY the
// config table and creates a real Dolt commit.
func TestCommitConfigCommitsConfigOnlyWorkingSet(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Start from a clean working set so the only dirty table is config.
	if err := store.CommitWithConfig(ctx, "test: baseline"); err != nil {
		t.Fatalf("baseline commit: %v", err)
	}
	before, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get baseline commit: %v", err)
	}

	if err := store.SetConfig(ctx, "test_gh4078_marker", "config-only write"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if !dirtyTables(t, store)["config"] {
		t.Fatal("precondition failed: SetConfig did not leave config dirty")
	}

	if err := store.CommitConfigOnly(ctx, "test: scoped config commit"); err != nil {
		t.Fatalf("CommitConfigOnly: %v", err)
	}

	after, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get commit after CommitConfigOnly: %v", err)
	}
	if after == before {
		t.Fatalf("CommitConfigOnly created no commit: HEAD still %s", before)
	}
	if dirtyTables(t, store)["config"] {
		t.Fatal("config table still dirty after CommitConfigOnly")
	}
}

// GH#2455 must survive GH#4078: the scoped commit stages ONLY config.
// Dirty rows in other tables (e.g. a concurrent operation's half-written
// issue) must remain in the working set, not get swept into the commit.
func TestCommitConfigDoesNotSweepOtherDirtyTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CommitWithConfig(ctx, "test: baseline"); err != nil {
		t.Fatalf("baseline commit: %v", err)
	}

	// Dirty the issues table directly, bypassing any commit machinery —
	// simulates a concurrent operation's uncommitted write.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		"testdb-gh4078", "concurrent dirty row", "", "", "", "", "open", 2, "task"); err != nil {
		t.Fatalf("dirty issues table: %v", err)
	}
	if err := store.SetConfig(ctx, "test_gh4078_scoped", "scoped"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	pre := dirtyTables(t, store)
	if !pre["config"] || !pre["issues"] {
		t.Fatalf("precondition failed: want config+issues dirty, got %v", pre)
	}

	if err := store.CommitConfigOnly(ctx, "test: scoped config commit"); err != nil {
		t.Fatalf("CommitConfigOnly: %v", err)
	}

	post := dirtyTables(t, store)
	if post["config"] {
		t.Fatal("config table still dirty after CommitConfigOnly")
	}
	if !post["issues"] {
		t.Fatal("CommitConfigOnly swept the dirty issues table — GH#2455 regression")
	}
}

// Pin the GH#2455 contract that motivates CommitConfigOnly's existence: the
// generic Commit() must keep EXCLUDING config. If this ever changes, the
// scoped-commit call sites should be revisited.
func TestCommitExcludesConfigOnlyWorkingSet(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CommitWithConfig(ctx, "test: baseline"); err != nil {
		t.Fatalf("baseline commit: %v", err)
	}
	before, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get baseline commit: %v", err)
	}

	if err := store.SetConfig(ctx, "test_gh2455_excluded", "stays dirty"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	if err := store.Commit(ctx, "test: generic commit must skip config"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	after, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get commit after Commit: %v", err)
	}
	if after != before {
		t.Fatal("generic Commit() committed a config-only working set — GH#2455 regression")
	}
	if !dirtyTables(t, store)["config"] {
		t.Fatal("config table no longer dirty after generic Commit()")
	}
}
