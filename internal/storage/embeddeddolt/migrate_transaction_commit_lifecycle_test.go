//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// errFault_4470 is the sentinel the fault hook below injects at migration
// 0040, the first main-source migration whose frozen body carries an
// in-body CALL DOLT_COMMIT under a suspended dolt_transaction_commit.
var errFault_4470 = errors.New("injected fault at migration 40")

// TestEmbeddedMigrateRestoresTransactionCommitAfterFault covers the
// restoration half of the GH#4470 fix (AC-2): a migration pass that suspends
// the session's dolt_transaction_commit must put the original value back on
// EVERY exit path — including a pass aborted mid-flight by an injected
// failure whose context was canceled — while the pass's own error stays the
// observable cause and the connection stays usable.
func TestEmbeddedMigrateRestoresTransactionCommitAfterFault(t *testing.T) {
	requireEmbedded(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := conn.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	// The hook fires at the end of migration 0040's step (after its per-step
	// commit). Inside the migration interval the suspension must be in
	// effect: the setting reads 0. It then cancels the pass's context —
	// emulating a killed/aborted caller — and injects the fault, so the
	// pass unwinds on an error under a context the deferred restoration
	// cannot use.
	var inIntervalSetting int64
	restoreHook := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 40 {
			return nil
		}
		if err := db.QueryRowContext(ctx,
			"SELECT @@SESSION.dolt_transaction_commit").Scan(&inIntervalSetting); err != nil {
			return fmt.Errorf("reading dolt_transaction_commit in fault hook: %w", err)
		}
		cancel()
		return errFault_4470
	})
	defer restoreHook()

	if _, err := schema.MigrateUp(ctx, conn); !errors.Is(err, errFault_4470) {
		t.Fatalf("MigrateUp err = %v, want the injected fault", err)
	}
	if inIntervalSetting != 0 {
		t.Fatalf("dolt_transaction_commit inside the migration interval = %d, want 0 (suspended)",
			inIntervalSetting)
	}
	// AC-2: the SAME usable connection reads 1 again, and the original
	// migration error remains the observable cause (errors.Is above).
	if got := sessionTransactionCommit(t, conn); got != 1 {
		t.Fatalf("session dolt_transaction_commit after failed pass = %d, want 1 (restored)", got)
	}
	if err := conn.PingContext(context.Background()); err != nil {
		t.Fatalf("connection unusable after pass: %v", err)
	}
}

// TestEmbeddedMigrateUnderTransactionCommitLeavesNoDebris pins the residency
// of a failed pass's own debris: the fault test above aborts at 0040's step
// boundary, so this exhaustively documents what the aborted pass left behind
// by simply re-running MigrateUp on the same connection — the retry must
// converge to latest with a clean working set under the still-enabled
// setting.
func TestEmbeddedMigrateRetryAfterFaultConverges(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := conn.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	restoreHook := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == 40 {
			return errFault_4470
		}
		return nil
	})
	_, _ = schema.MigrateUp(ctx, conn)
	restoreHook()

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("retry MigrateUp: %v", err)
	}
	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("schema version after retry = %d, want %d", got, want)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set dirty after retry: %v, want clean", dirty)
	}
	if got := sessionTransactionCommit(t, conn); got != 1 {
		t.Fatalf("session dolt_transaction_commit after retry = %d, want 1 (restored)", got)
	}
}
