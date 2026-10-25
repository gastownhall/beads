package dolt

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

// A memory write commits its own kv.* rows, and with them any other user kv.*
// rows already dirty: those are this clone's data, the same rows the pre-pull
// auto-commit would commit.
func TestCommitConfigUserKVOnlyCommitsUserKVRows(t *testing.T) {
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

	if err := store.SetConfig(ctx, "kv.memory.gh4078-a", "memory a"); err != nil {
		t.Fatalf("SetConfig memory: %v", err)
	}
	if err := store.SetConfig(ctx, "kv.gh4078-flag", "user kv"); err != nil {
		t.Fatalf("SetConfig kv: %v", err)
	}

	if err := store.CommitConfigUserKVOnly(ctx, "test: memory commit"); err != nil {
		t.Fatalf("CommitConfigUserKVOnly: %v", err)
	}

	after, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get commit after CommitConfigUserKVOnly: %v", err)
	}
	if after == before {
		t.Fatalf("CommitConfigUserKVOnly created no commit: HEAD still %s", before)
	}
	if dirtyTables(t, store)["config"] {
		t.Fatal("config table still dirty after CommitConfigUserKVOnly")
	}
}

// A dirty internal config key is not a memory write's to commit: the scoped
// commit refuses, names the key, and leaves HEAD and the working set alone.
func TestCommitConfigUserKVOnlyRefusesInternalConfigKeys(t *testing.T) {
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

	if err := store.SetConfig(ctx, "kv.memory.gh4078-b", "memory b"); err != nil {
		t.Fatalf("SetConfig memory: %v", err)
	}
	if err := store.SetConfig(ctx, "test_gh4078_internal", "concurrent writer"); err != nil {
		t.Fatalf("SetConfig internal: %v", err)
	}

	err = store.CommitConfigUserKVOnly(ctx, "test: memory commit")
	if err == nil {
		t.Fatal("CommitConfigUserKVOnly committed over a dirty internal config key")
	}
	var refused *storage.ConfigCommitRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want a *storage.ConfigCommitRefusedError", err)
	}
	if !slices.Equal(refused.Keys, []string{"test_gh4078_internal"}) {
		t.Fatalf("refused keys = %v, want [test_gh4078_internal]", refused.Keys)
	}
	if !strings.Contains(err.Error(), "test_gh4078_internal") {
		t.Fatalf("error = %q, want it to name the internal key", err)
	}

	assertRefusedCommitLeftNoTrace(t, store, before)
}

// assertRefusedCommitLeftNoTrace checks a refused CommitConfigUserKVOnly's
// postconditions: HEAD is still before, nothing is staged for the next commit
// on the branch, and config is still dirty with the rows the refusal kept.
func assertRefusedCommitLeftNoTrace(t *testing.T, store *DoltStore, before string) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()

	after, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get commit after refusal: %v", err)
	}
	if after != before {
		t.Fatalf("HEAD moved from %s to %s on a refused commit", before, after)
	}
	staged, err := issueops.HasStagedChanges(ctx, store.db)
	if err != nil {
		t.Fatalf("check staged set after refusal: %v", err)
	}
	if staged {
		t.Fatal("a refused commit left changes staged, so the next commit on the branch would publish them")
	}
	if !dirtyTables(t, store)["config"] {
		t.Fatal("config table no longer dirty after a refused commit")
	}
}

// Sessions on one branch share its working set, so screening on the pinned
// connection that stages does not keep other sessions out: a key another
// session writes after the working-set screen is staged by DOLT_ADD all the
// same. The staged-set screen must refuse it and unstage config again.
func TestCommitConfigUserKVOnlyRefusesAKeyDirtiedAfterTheScreen(t *testing.T) {
	// The second session writes while the commit holds its pinned
	// connection, which setupTestStore's single connection would deadlock
	// on. Not parallel: the test swaps the package-level seam.
	store, cleanup := setupConcurrentTestStore(t)
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
	if err := store.SetConfig(ctx, "kv.memory.gh4078-race", "memory"); err != nil {
		t.Fatalf("SetConfig memory: %v", err)
	}

	var raceErr error
	restore := afterStageCommitScreen
	t.Cleanup(func() { afterStageCommitScreen = restore })
	afterStageCommitScreen = func() {
		_, raceErr = store.db.ExecContext(ctx,
			"INSERT INTO config (`key`, value) VALUES ('test_gh4078_race', 'concurrent writer')")
	}

	err = store.CommitConfigUserKVOnly(ctx, "test: memory commit")
	if raceErr != nil {
		t.Fatalf("concurrent config write: %v", raceErr)
	}
	var refused *storage.ConfigCommitRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("CommitConfigUserKVOnly = %v, want a *storage.ConfigCommitRefusedError for the key written after the screen", err)
	}
	if !slices.Equal(refused.Keys, []string{"test_gh4078_race"}) {
		t.Fatalf("refused keys = %v, want [test_gh4078_race]", refused.Keys)
	}

	assertRefusedCommitLeftNoTrace(t, store, before)
	var kept int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM config WHERE `key` IN ('kv.memory.gh4078-race', 'test_gh4078_race')").Scan(&kept); err != nil {
		t.Fatalf("count working-set config rows: %v", err)
	}
	if kept != 2 {
		t.Fatalf("%d of the 2 config rows are still in the working set after the refusal, want both", kept)
	}
}

// The refusal paths on the SQL they run: a key dirty before staging refuses
// before DOLT_ADD; one that only the staged set holds is unstaged with
// DOLT_RESET, and a failed unstage is reported alongside the refusal. None of
// them reaches DOLT_COMMIT.
func TestCommitConfigUserKVOnlyRefusalSQL(t *testing.T) {
	const (
		workingDiff = "dolt_diff('HEAD', 'WORKING', 'config')"
		stagedDiff  = "dolt_diff('HEAD', 'STAGED', 'config')"
	)
	resetFailure := errors.New("reset failed")
	stageAndRefuse := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(workingDiff)).
			WillReturnRows(sqlmock.NewRows([]string{"key"}).AddRow("kv.memory.a"))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
			WithArgs("config").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(regexp.QuoteMeta(stagedDiff)).
			WillReturnRows(sqlmock.NewRows([]string{"key"}).AddRow("kv.memory.a").AddRow("issue_prefix"))
	}
	for _, tc := range []struct {
		name         string
		setup        func(sqlmock.Sqlmock)
		wantResetErr bool
	}{
		{
			name: "dirty before staging",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(workingDiff)).
					WillReturnRows(sqlmock.NewRows([]string{"key"}).AddRow("kv.memory.a").AddRow("issue_prefix"))
			},
		},
		{
			name: "staged only",
			setup: func(mock sqlmock.Sqlmock) {
				stageAndRefuse(mock)
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET(?)")).
					WithArgs("config").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
			},
		},
		{
			name: "staged only, unstage fails",
			setup: func(mock sqlmock.Sqlmock) {
				stageAndRefuse(mock)
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET(?)")).
					WithArgs("config").
					WillReturnError(resetFailure)
			},
			wantResetErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			tc.setup(mock)

			store := &DoltStore{db: db}
			err = store.CommitConfigUserKVOnly(context.Background(), "bd: remember")

			var refused *storage.ConfigCommitRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("CommitConfigUserKVOnly = %v, want a *storage.ConfigCommitRefusedError", err)
			}
			if !slices.Equal(refused.Keys, []string{"issue_prefix"}) {
				t.Fatalf("refused keys = %v, want [issue_prefix]", refused.Keys)
			}
			if got := errors.Is(err, resetFailure); got != tc.wantResetErr {
				t.Fatalf("error %q carries the reset failure = %v, want %v", err, got, tc.wantResetErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

// With versioned history active, every other fixed-list commit also stages the
// history tables, issues among them. A config write mints no version rows, so
// the scoped config commits stage only their own tables: widening them would
// sweep a concurrent issue mutation into a config or memory commit (GH#2455).
func TestScopedConfigCommitsSkipVersionedHistoryTables(t *testing.T) {
	for _, tc := range []struct {
		name   string
		commit func(*DoltStore, context.Context, string) error
	}{
		{name: "CommitConfigOnly", commit: (*DoltStore).CommitConfigOnly},
		{name: "CommitConfigUserKVOnly", commit: (*DoltStore).CommitConfigUserKVOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			if err := store.CommitWithConfig(ctx, "test: baseline"); err != nil {
				t.Fatalf("baseline commit: %v", err)
			}
			store.SetVersionedHistoryEnabled(true)

			if _, err := store.db.ExecContext(ctx,
				"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)"+
					" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
				"testdb-gh4078-history", "concurrent dirty row", "", "", "", "", "open", 2, "task"); err != nil {
				t.Fatalf("dirty issues table: %v", err)
			}
			if err := store.SetConfig(ctx, "kv.gh4078-history", "user kv"); err != nil {
				t.Fatalf("SetConfig: %v", err)
			}
			pre := dirtyTables(t, store)
			if !pre["config"] || !pre["issues"] {
				t.Fatalf("precondition failed: want config+issues dirty, got %v", pre)
			}

			if err := tc.commit(store, ctx, "test: scoped config commit"); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			post := dirtyTables(t, store)
			if post["config"] {
				t.Fatalf("config table still dirty after %s", tc.name)
			}
			if !post["issues"] {
				t.Fatalf("%s swept the dirty issues table with versioned history on — GH#2455 regression", tc.name)
			}
		})
	}
}

// SetConfig writes status.custom and types.custom into their lookup tables in
// the same transaction as the config row, so the scoped commit must take those
// tables with it: a commit holding the row without its projection would serve
// the old lookup set to every clone that pulls it.
func TestCommitConfigOnlyCommitsConfigProjections(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CommitWithConfig(ctx, "test: baseline"); err != nil {
		t.Fatalf("baseline commit: %v", err)
	}

	if err := store.SetConfig(ctx, "status.custom", "gh4078review:active"); err != nil {
		t.Fatalf("SetConfig status.custom: %v", err)
	}
	if err := store.SetConfig(ctx, "types.custom", "gh4078kind"); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	pre := dirtyTables(t, store)
	if !pre["config"] || !pre["custom_statuses"] || !pre["custom_types"] {
		t.Fatalf("precondition failed: want config+custom_statuses+custom_types dirty, got %v", pre)
	}

	if err := store.CommitConfigOnly(ctx, "test: scoped config commit"); err != nil {
		t.Fatalf("CommitConfigOnly: %v", err)
	}

	post := dirtyTables(t, store)
	for _, table := range []string{"config", "custom_statuses", "custom_types"} {
		if post[table] {
			t.Fatalf("%s still dirty after CommitConfigOnly: got %v", table, post)
		}
	}
}
