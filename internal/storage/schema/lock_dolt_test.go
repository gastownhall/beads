package schema

import (
	"context"
	"errors"
	"testing"
)

// TestMigrateUpWithLockSentinelGatesFastPathOnRealServer proves the
// pass-completion sentinel's contract against a real dolt sql-server, on the
// exact database state a concurrent prober observes while another process's
// migration pass tail (dependency rekey, staging, final commit) is still in
// flight: every legacy probe input current, sentinel absent.
//
//  1. A fully migrated database fast-paths (probe current, no lock needed).
//  2. With only the sentinel row removed — everything else still current — the
//     probe refuses the fast path and MigrateUpWithLock queues on GET_LOCK:
//     while another session holds the lock, the open times out with
//     ErrMigrationLockUnavailable instead of sneaking through lock-free.
//  3. Once the lock frees, the locked pass re-proves currency, restamps the
//     sentinel, and subsequent opens fast-path again.
func TestMigrateUpWithLockSentinelGatesFastPathOnRealServer(t *testing.T) {
	db := startBenchDoltServer(t)
	ctx := context.Background()

	current, err := migrationStateCurrent(ctx, db)
	if err != nil || !current {
		t.Fatalf("migrationStateCurrent() after full MigrateUp = %v, %v; want true, nil", current, err)
	}

	if _, err := db.ExecContext(ctx,
		"DELETE FROM local_metadata WHERE `key` = ?", migrationPassCompleteKey); err != nil {
		t.Fatalf("remove pass sentinel: %v", err)
	}

	current, err = migrationStateCurrent(ctx, db)
	if err != nil {
		t.Fatalf("migrationStateCurrent() without sentinel: %v", err)
	}
	if current {
		t.Fatal("migrationStateCurrent() = true with the pass sentinel absent; the probe must refuse the fast path (this is the state a prober sees mid-pass)")
	}

	// Hold the migration lock from another session: a sentinel-less open must
	// queue on it (and here, time out) — not proceed lock-free.
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin lock-holder connection: %v", err)
	}
	defer holder.Close()
	lockName := MigrationLockName("benchdb")
	if err := AcquireMigrationLock(ctx, holder, lockName); err != nil {
		t.Fatalf("acquire lock on holder session: %v", err)
	}

	opener, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin opener connection: %v", err)
	}
	defer opener.Close()
	if _, err := MigrateUpWithLock(ctx, opener, "benchdb"); !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("MigrateUpWithLock() with sentinel absent and lock held = %v; want ErrMigrationLockUnavailable (must take the locked path)", err)
	}

	if err := ReleaseMigrationLock(holder, lockName); err != nil {
		t.Fatalf("release holder lock: %v", err)
	}

	// Locked pass converges: no work, sentinel restamped, fast path restored.
	if _, err := MigrateUpWithLock(ctx, opener, "benchdb"); err != nil {
		t.Fatalf("MigrateUpWithLock() after lock freed: %v", err)
	}
	current, err = migrationStateCurrent(ctx, db)
	if err != nil || !current {
		t.Fatalf("migrationStateCurrent() after locked pass = %v, %v; want true, nil (sentinel restamped)", current, err)
	}
}

// TestMigrationProbeRefusesFastPathMidPassOnRealServer is the live
// interleaving repro the review asked for: it runs a real upgrade-shaped
// migration pass (main cursor one behind, ignored source already at latest —
// the common release shape) and, from a second session at the exact moment the
// last numbered migration's per-step commit has landed but the pass tail
// (dependency rekey, staging, final commit) has not run, asserts:
//
//   - the legacy probe inputs (seeds + no migration work) are ALL satisfied —
//     i.e. without the sentinel this prober would have gone lock-free into
//     another process's half-finished pass, and
//   - migrationStateCurrent still reports NOT current, because the running
//     pass revoked the sentinel at pass start.
func TestMigrationProbeRefusesFastPathMidPassOnRealServer(t *testing.T) {
	db := startBenchDoltServer(t)
	ctx := context.Background()

	// Rewind the main cursor one step. Migration files are idempotent against
	// an already-applied schema (guarded ALTERs), so re-running the last one
	// is a safe way to drive a real pass whose only pending work is one main
	// migration — ignored source stays at latest, which is exactly the shape
	// that exposes the pass tail.
	latest := LatestVersion()
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", latest); err != nil {
		t.Fatalf("rewind main cursor: %v", err)
	}

	prober, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin prober connection: %v", err)
	}
	defer prober.Close()

	hookRan := false
	var hookErr error
	restore := SetMigrateStepFaultHookForTest(func(hctx context.Context, _ DBConn, version int) error {
		if version != latest {
			return nil
		}
		hookRan = true
		// The last step's commit has landed; the tail has not run. Capture
		// assertions as errors so the pass itself still completes.
		seeded, err := doltIgnoreSeedCurrent(hctx, prober)
		if err != nil {
			hookErr = err
			return nil
		}
		needed, err := migrationWorkNeeded(hctx, prober)
		if err != nil {
			hookErr = err
			return nil
		}
		if !seeded || needed {
			hookErr = errors.New("mid-pass window not reproduced: legacy probe inputs not all current at the hook (seeds=" +
				boolStr(seeded) + ", workNeeded=" + boolStr(needed) + ")")
			return nil
		}
		current, err := migrationStateCurrent(hctx, prober)
		if err != nil {
			hookErr = err
			return nil
		}
		if current {
			hookErr = errors.New("migrationStateCurrent() = true mid-pass: a concurrent opener would fast-path into this half-finished pass")
		}
		return nil
	})
	defer restore()

	if _, err := MigrateUp(ctx, db); err != nil {
		t.Fatalf("MigrateUp() upgrade pass: %v", err)
	}
	if !hookRan {
		t.Fatal("fault hook never fired for the rewound migration; the pass did not re-apply it")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}

	// The pass completed: the sentinel is restamped and the fast path returns.
	current, err := migrationStateCurrent(ctx, db)
	if err != nil || !current {
		t.Fatalf("migrationStateCurrent() after pass = %v, %v; want true, nil", current, err)
	}
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
