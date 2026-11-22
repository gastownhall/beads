package uow

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestCommitLockSerializesUOWCommits covers AC-3 of GH#8590 (bd-72i8dp): a
// uow proxied-server session's Dolt commits, CommitScoped's post-COMMIT
// DOLT_ADD + DOLT_COMMIT and Commit's DOLT_COMMIT('-Am') inside the open
// transaction, wait for versioncontrolops.CommitLockName(db) held by another
// session, and land once it is released. So do the Dolt commit of the
// post-commit blocked-state recheck and the maintenance commit that the
// proxied `bd dolt commit` and pre-backup commit make
// (versioncontrolops.CommitAllSerialized on a RunNonTx session).
func TestCommitLockSerializesUOWCommits(t *testing.T) {
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const database = "uow_commit_lock"
	opened, err := openAndInitSchema(ctx, "tcp", ep, "", database, "root", "", "", false, "", providerOptions{}, nil)
	if err != nil {
		t.Fatalf("openAndInitSchema: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close(context.Background()) })
	provider := opened.(*doltSQLProvider)

	// The holder is another client of the same database: a pinned session
	// for the lock and a pool of its own for reads.
	holderDB, err := sql.Open("mysql", buildDSN("tcp", ep, database, "root", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holderDB.Close() })
	holder, err := holderDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	name := versioncontrolops.CommitLockName(database)

	lockQuery := func(t *testing.T, query string) {
		t.Helper()
		var got sql.NullInt64
		if err := holder.QueryRowContext(ctx, query, name).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if !got.Valid || got.Int64 != 1 {
			t.Fatalf("%s = %v, want 1", query, got)
		}
	}
	logCount := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := holderDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return n
	}
	logHas := func(t *testing.T, message string) bool {
		t.Helper()
		var n int
		if err := holderDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", message).Scan(&n); err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		return n > 0
	}
	// waitsForLock runs commit while the holder has the lock and requires
	// that it neither returns nor lands a Dolt commit before the release, then
	// that it returns nil within 5s of the release and has landed message.
	waitsForLock := func(t *testing.T, label, message string, commit func() error) {
		t.Helper()
		lockQuery(t, "SELECT GET_LOCK(?, 0)")
		before := logCount(t)
		done := make(chan error, 1)
		go func() { done <- commit() }()

		select {
		case err := <-done:
			landed := logHas(t, message)
			lockQuery(t, "SELECT RELEASE_LOCK(?)")
			if !landed {
				t.Fatalf("%s returned (err=%v) without a Dolt commit while the lock was held", label, err)
			}
			t.Fatalf("%s landed its Dolt commit (err=%v) while another session held %q (GH#8590)", label, err, name)
		case <-time.After(500 * time.Millisecond):
		}
		if after := logCount(t); after != before {
			lockQuery(t, "SELECT RELEASE_LOCK(?)")
			t.Fatalf("dolt_log grew from %d to %d while another session held %q: the %s Dolt commit landed unlocked (GH#8590)", before, after, name, label)
		}

		lockQuery(t, "SELECT RELEASE_LOCK(?)")
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s after the lock release: %v", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not return within 5s of the lock release", label)
		}
		if !logHas(t, message) {
			t.Fatalf("dolt_log has no commit %q after the lock release", message)
		}
	}

	cases := []struct {
		name   string
		commit func(ctx context.Context, tx *doltServerTx, message string) error
	}{
		{name: "CommitScoped", commit: func(ctx context.Context, tx *doltServerTx, message string) error {
			return tx.CommitScoped(ctx, []string{"config"}, message)
		}},
		{name: "Commit -Am", commit: func(ctx context.Context, tx *doltServerTx, message string) error {
			return tx.Commit(ctx, message)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := "commit-lock uow " + tc.name
			begun, err := provider.BeginTx(ctx)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			tx := begun.(*doltServerTx)
			defer tx.RollbackUnlessCommitted(context.Background())
			if _, err := tx.Runner().ExecContext(ctx,
				"REPLACE INTO config (`key`, value) VALUES (?, ?)", "commit_lock."+tc.name, "v"); err != nil {
				t.Fatalf("write config: %v", err)
			}
			waitsForLock(t, tc.name, message, func() error { return tc.commit(ctx, tx, message) })
		})
	}

	// DOLT_COMMIT ends the unit of work's transaction but not the
	// explicit-transaction mode START TRANSACTION set, so the RELEASE_LOCK
	// after it would begin a transaction that never commits, and the session
	// would go back to the pool reading a snapshot from before every later
	// write. After a locked commit the session must read what another session
	// wrote since.
	t.Run("session reads fresh after a locked commit", func(t *testing.T) {
		begun, err := provider.BeginTx(ctx)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		tx := begun.(*doltServerTx)
		defer tx.releaseConn()
		if _, err := tx.Runner().ExecContext(ctx,
			"REPLACE INTO config (`key`, value) VALUES ('commit_lock.fresh', 'v')"); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if err := tx.execCommitStatement(ctx, "CALL DOLT_COMMIT('-Am', ?);", []interface{}{"commit-lock uow fresh session"}); err != nil {
			t.Fatalf("locked DOLT_COMMIT: %v", err)
		}
		tx.done = true
		if _, err := holderDB.ExecContext(ctx,
			"REPLACE INTO config (`key`, value) VALUES ('commit_lock.after', 'v')"); err != nil {
			t.Fatalf("write from another session: %v", err)
		}
		var n int
		if err := tx.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM config WHERE `key` = 'commit_lock.after'").Scan(&n); err != nil {
			t.Fatalf("read on the committed session: %v", err)
		}
		if n != 1 {
			t.Fatalf("the session cannot see a row another session wrote after its locked DOLT_COMMIT: the lock release left a transaction open on it")
		}
		if tx.discardSession {
			t.Fatalf("a clean locked commit marked the session for discard")
		}
	})

	// The post-commit blocked-state recheck (#6716) makes a Dolt commit only
	// when its recompute changes an issues row, which in production means a
	// concurrent writer made the in-transaction recompute stale. Here the
	// dependent's committed flag is stale outright: blocked, with no blocker.
	t.Run("blocked recheck", func(t *testing.T) {
		const prefix, id = "ucl", "ucl-stale"
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			return "bd: set issue prefix", uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", prefix)
		}); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}
		if err := newUOWRoleFixtureKit(provider, prefix).CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2,
		}, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if _, err := holder.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", id); err != nil {
			t.Fatalf("make %s stale: %v", id, err)
		}
		if _, err := holder.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit-lock uow: stale is_blocked')"); err != nil {
			t.Fatalf("commit the stale flag: %v", err)
		}

		begun, err := provider.BeginTx(ctx)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		tx := begun.(*doltServerTx)
		defer tx.releaseConn()
		// Commit's state when it runs the recheck: the unit of work's
		// transaction has committed on the pinned session.
		if _, err := tx.conn.ExecContext(ctx, "COMMIT;"); err != nil {
			t.Fatalf("end the unit of work's transaction: %v", err)
		}
		tx.done = true

		pending := issueops.BlockedRecheck{IssueIDs: []string{id}, Sources: []string{"close of ucl-blocker"}, SourceCount: 1, Actor: "racer"}
		waitsForLock(t, "blocked recheck", pending.CommitMessage(), func() error {
			if !tx.recheckBlockedAfterCommit(ctx, pending) {
				return errors.New("the recheck left a transaction open on the pinned session")
			}
			return nil
		})
		var blocked bool
		if err := holderDB.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&blocked); err != nil {
			t.Fatalf("read is_blocked: %v", err)
		}
		if blocked {
			t.Fatalf("%s is still is_blocked=1 after the recheck committed", id)
		}
	})

	// The proxied `bd dolt commit` and pre-backup commit: a working-set
	// commit on a RunNonTx maintenance session, keyed on that session's own
	// database (CommitLockConfig.Database is empty).
	t.Run("maintenance commit", func(t *testing.T) {
		if _, err := holderDB.ExecContext(ctx,
			"REPLACE INTO config (`key`, value) VALUES ('commit_lock.maintenance', 'v')"); err != nil {
			t.Fatalf("dirty the working set: %v", err)
		}
		const message = "commit-lock uow maintenance commit"
		waitsForLock(t, "maintenance commit", message, func() error {
			return provider.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
				committed, err := versioncontrolops.CommitAllSerialized(ctx, conn, versioncontrolops.CommitLockConfig{}, message, nil)
				if err == nil && !committed {
					return errors.New("CommitAllSerialized made no commit")
				}
				return err
			})
		})
	})
}
