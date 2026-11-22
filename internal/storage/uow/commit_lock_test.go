package uow

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestCommitLockSerializesUOWCommits covers AC-3 of GH#8590 (bd-72i8dp): a
// uow proxied-server session's Dolt commits, CommitScoped's post-COMMIT
// DOLT_ADD + DOLT_COMMIT and Commit's DOLT_COMMIT('-Am') inside the open
// transaction, wait for versioncontrolops.CommitLockName(db) held by another
// session, and land once it is released.
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

	lockQuery := func(query string) {
		t.Helper()
		var got sql.NullInt64
		if err := holder.QueryRowContext(ctx, query, name).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if !got.Valid || got.Int64 != 1 {
			t.Fatalf("%s = %v, want 1", query, got)
		}
	}
	logCount := func() int {
		t.Helper()
		var n int
		if err := holderDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return n
	}
	logHas := func(message string) bool {
		t.Helper()
		var n int
		if err := holderDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", message).Scan(&n); err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		return n > 0
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

			lockQuery("SELECT GET_LOCK(?, 0)")
			before := logCount()
			done := make(chan error, 1)
			go func() { done <- tc.commit(ctx, tx, message) }()

			select {
			case err := <-done:
				if !logHas(message) {
					lockQuery("SELECT RELEASE_LOCK(?)")
					t.Fatalf("%s returned (err=%v) without a Dolt commit while the lock was held", tc.name, err)
				}
				lockQuery("SELECT RELEASE_LOCK(?)")
				t.Fatalf("%s landed its Dolt commit (err=%v) while another session held %q (GH#8590)", tc.name, err, name)
			case <-time.After(500 * time.Millisecond):
			}
			if after := logCount(); after != before {
				lockQuery("SELECT RELEASE_LOCK(?)")
				t.Fatalf("dolt_log grew from %d to %d while another session held %q: the uow Dolt commit landed unlocked (GH#8590)", before, after, name)
			}

			lockQuery("SELECT RELEASE_LOCK(?)")
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s after the lock release: %v", tc.name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not return within 5s of the lock release", tc.name)
			}
			if !logHas(message) {
				t.Fatalf("dolt_log has no commit %q after the lock release", message)
			}
		})
	}
}
