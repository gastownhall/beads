package uow

// Commit-lock tests for the proxied-server unit-of-work planes (GH#8590,
// dolthub/dolt#12258): CommitScoped's post-SQL stage-and-commit
// (stageAndCommitOnce) and Commit's DOLT_COMMIT('-Am') each hold the
// per-database named lock (versioncontrolops.CommitLockName) across their
// Dolt commit, so another front door's HEAD move cannot be merged into the
// staged root and committed as conflict artifacts.

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestCommitLockSerializesUOWCommits is AC-3: with a second session holding
// CommitLockName(db), neither a uow CommitScoped's post-SQL Dolt commit nor a
// uow Commit's DOLT_COMMIT('-Am') lands until the holder releases the lock.
func TestCommitLockSerializesUOWCommits(t *testing.T) {
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const database = "commit_lock_uow"
	opened, err := openAndInitSchema(ctx, "tcp", ep, "", database, "root", "", "", false, "", providerOptions{}, nil)
	if err != nil {
		t.Fatalf("openAndInitSchema: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close(context.Background()) })
	provider := opened.(*doltSQLProvider)

	// The probe is an independent session on the same database, serving as
	// the external lock holder and the dolt_log reader.
	dsn := buildDSN("tcp", ep, database, "root", "", "")
	probe, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open probe: %v", err)
	}
	t.Cleanup(func() { _ = probe.Close() })

	logCount := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := probe.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return n
	}
	lockName := versioncontrolops.CommitLockName(database)
	hold := func(t *testing.T) {
		t.Helper()
		var acquired sql.NullInt64
		if err := probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&acquired); err != nil {
			t.Fatalf("GET_LOCK: %v", err)
		}
		if !acquired.Valid || acquired.Int64 != 1 {
			t.Fatalf("GET_LOCK(%q) = %v, want 1", lockName, acquired)
		}
	}
	release := func(t *testing.T) {
		t.Helper()
		var released sql.NullInt64
		if err := probe.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
			t.Fatalf("RELEASE_LOCK: %v", err)
		}
		if !released.Valid || released.Int64 != 1 {
			t.Fatalf("RELEASE_LOCK(%q) = %v, want 1", lockName, released)
		}
	}
	requireLockFree := func(t *testing.T, why string) {
		t.Helper()
		var free sql.NullInt64
		if err := probe.QueryRowContext(ctx, "SELECT IS_FREE_LOCK(?)", lockName).Scan(&free); err != nil {
			t.Fatalf("IS_FREE_LOCK: %v", err)
		}
		if !free.Valid || free.Int64 != 1 {
			t.Fatalf("%s: IS_FREE_LOCK(%q) is 0 — a uow plane leaked its commit lock", why, lockName)
		}
	}

	// The workspace's issue prefix the seeded ID has to match.
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: set issue prefix", uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "uowlock")
	}); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	// Seed one issue the plane attempts below can name.
	seed := &types.Issue{ID: "uowlock-1", Title: "commit lock", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: seed, ExplicitID: seed.ID, CreateOnly: true}, "seeder")
		return "seed " + seed.ID, err
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	// assertBlockedWhileHeld runs one commit plane under the held lock: it
	// must not return (nor mint a dolt_log entry) while the lock is held, and
	// must finish cleanly shortly after the holder releases it.
	assertBlockedWhileHeld := func(t *testing.T, run func(ctx context.Context) error) {
		t.Helper()
		before := logCount(t)
		hold(t)
		done := make(chan error, 1)
		go func() { done <- run(ctx) }()
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if n := logCount(t); n != before {
				t.Fatalf("dolt_log grew from %d to %d while the commit lock was held", before, n)
			}
			select {
			case err := <-done:
				t.Fatalf("uow commit returned %v while the lock was still held", err)
			case <-time.After(50 * time.Millisecond):
			}
		}
		release(t)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("uow commit after lock release: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("uow commit did not return within 10s of the lock being released")
		}
		if after := logCount(t); after <= before {
			t.Fatalf("no dolt_log entry was minted after the lock was released (before=%d, after=%d)", before, after)
		}
		requireLockFree(t, "uow plane")
	}

	t.Run("CommitScoped", func(t *testing.T) {
		assertBlockedWhileHeld(t, func(ctx context.Context) error {
			_, err := RunTxScoped(ctx, provider, func(ctx context.Context, uw UnitOfWork) (any, []string, string, error) {
				if err := uw.LabelUseCase().AddLabel(ctx, seed.ID, "uow-lock-1", "tester"); err != nil {
					return nil, nil, "", err
				}
				return nil, []string{"labels", "events"}, "uow: label add uow-lock-1", nil
			})
			return err
		})
	})

	t.Run("CommitAm", func(t *testing.T) {
		assertBlockedWhileHeld(t, func(ctx context.Context) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				if err := uw.LabelUseCase().AddLabel(ctx, seed.ID, "uow-lock-2", "tester"); err != nil {
					return "", err
				}
				return "uow: label add uow-lock-2", nil
			})
		})
	})
}
