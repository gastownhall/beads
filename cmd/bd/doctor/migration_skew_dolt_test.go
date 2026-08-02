//go:build cgo

package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestCheckMigrationContentSkew_RealDolt runs the skew check end-to-end against
// a real Dolt server. The sqlmock tests never parse SQL, which is how the
// original check shipped with an AS OF query Dolt rejects on every call
// (`unbound variable "v1" in query`, bd-6dnrw.27) while staying green — this
// test exists so the production query shape is actually executed by Dolt.
//
// One database create/drop cycle covers all four states in sequence:
// no remote -> remote without a cached tracking ref -> pushed and matching ->
// locally tampered hash (skew warning).
func TestCheckMigrationContentSkew_RealDolt(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dbName := "skewtest_" + hex.EncodeToString(buf)

	store, err := dolt.New(ctx, &dolt.Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("dolt.New: %v", err)
	}
	db := store.DB()
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// 1. No remote configured: the check skips.
	got := checkMigrationContentSkew(ctx, db, "origin")
	if got.Status != StatusOK || !strings.Contains(got.Message, "not configured") {
		t.Fatalf("no-remote: status=%q message=%q, want OK 'not configured'", got.Status, got.Message)
	}

	// 2. Remote registered but never pushed: no cached tracking ref to read.
	// The file:// URL points inside the Dolt server's filesystem; Dolt creates
	// the directory on first push.
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file:///tmp/"+dbName+"-remote"); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	got = checkMigrationContentSkew(ctx, db, "origin")
	if got.Status != StatusOK || !strings.Contains(got.Message, "No cached remote ref") {
		t.Fatalf("no-ref: status=%q message=%q, want OK 'No cached remote ref'", got.Status, got.Message)
	}

	// 3. Push main: the AS OF read of remotes/origin/main must now succeed and
	// match. This is the line the broken bind-param query could never reach.
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('--allow-empty', '-m', 'test: schema for skew check')"); err != nil {
		t.Fatalf("DOLT_COMMIT: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('origin', 'main')"); err != nil {
		t.Fatalf("DOLT_PUSH: %v", err)
	}
	got = checkMigrationContentSkew(ctx, db, "origin")
	if got.Status != StatusOK || !strings.Contains(got.Message, "match remote") {
		t.Fatalf("pushed: status=%q message=%q, want OK 'match remote'", got.Status, got.Message)
	}

	// Same fix, same fixture: server-mode SyncStatus used the identical broken
	// AS OF bind-param query and always reported ahead/behind as -1/-1. With a
	// cached tracking ref it must now return real counts (0/0 right after push).
	syncStatus, err := store.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if syncStatus.LocalAhead != 0 || syncStatus.LocalBehind != 0 {
		t.Fatalf("SyncStatus ahead/behind = %d/%d, want 0/0 right after push",
			syncStatus.LocalAhead, syncStatus.LocalBehind)
	}

	// 4. Tamper one local content hash: the check must warn and name the version.
	var maxVersion int
	if err := db.QueryRowContext(ctx,
		"SELECT MAX(version) FROM schema_migrations WHERE content_hash IS NOT NULL AND content_hash <> ''").
		Scan(&maxVersion); err != nil {
		t.Fatalf("max version: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE schema_migrations SET content_hash = 'skew-test-divergent' WHERE version = ?", maxVersion); err != nil {
		t.Fatalf("tamper hash: %v", err)
	}
	got = checkMigrationContentSkew(ctx, db, "origin")
	if got.Status != StatusWarning {
		t.Fatalf("tampered: status=%q message=%q, want warning", got.Status, got.Message)
	}
	if want := fmt.Sprintf("%04d", maxVersion); !strings.Contains(got.Message, want) {
		t.Fatalf("tampered: message=%q, want it to name migration %s", got.Message, want)
	}
}
