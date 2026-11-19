package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestBackupSyncAfterMissingTableFileRecoversOnlyAfterMoveAside is the
// server-mode guard for the recovery GH#4070 (bd-5mwx39) documents: a backup
// directory whose manifest names a table file that is not in the directory —
// the state an interrupted sync left behind — can never be synced to again.
// DOLT_BACKUP('sync') has no re-seed option, so the design deliberately does
// NOT self-heal by re-sending the missing chunks: beads only records and
// reports the failure (see cmd/bd), and the recovery is the move-aside step
// the status hint and docs spell out. This pins both halves of that promise
// against a real Dolt server:
//
//   - every later sync fails with Dolt's own error, naming the destination
//     directory and the missing table file, until the directory is moved
//     aside;
//   - after the move (the old directory is kept, not deleted) a fresh
//     full-history backup is written at the same path, and a restore from
//     that fresh backup yields every row, including the one committed while
//     the destination was broken.
//
// It adopts the fixture of the triage repro (b51f14f8e5,
// TestBackupSyncRecoversFromManifestAheadOfTableFiles), which asserted the
// raw BackupSync heals itself — behavior neither the pinned Dolt nor this
// design provides.
func TestBackupSyncAfterMissingTableFileRecoversOnlyAfterMoveAside(t *testing.T) {
	port := testutil.StartIsolatedDoltContainerHandle(t).Port
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	name := fmt.Sprintf("backup_%d", time.Now().UnixNano())
	root, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer root.Close()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?parseTime=true", port, name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	exec := func(queries ...string) {
		t.Helper()
		for _, q := range queries {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	exec(
		"CREATE TABLE t (id INT PRIMARY KEY, v VARCHAR(32))",
		"INSERT INTO t VALUES (1, 'one')",
		"CALL DOLT_COMMIT('-Am', 'first')",
	)

	backupDir := filepath.Join(t.TempDir(), "backup")
	if err := BackupAdd(ctx, conn, ExportBackupName, "file://"+filepath.ToSlash(backupDir)); err != nil {
		t.Fatal(err)
	}
	if err := BackupSync(ctx, conn, ExportBackupName); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Leave the backup in the reported state: the manifest names a table
	// file that never reached the directory.
	manifest, err := os.ReadFile(filepath.Join(backupDir, "manifest"))
	if err != nil {
		t.Fatalf("read backup manifest: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(string(manifest)), ":")
	var removed string
	for i := 5; i+1 < len(parts) && removed == ""; i += 2 {
		for _, cand := range []string{parts[i], parts[i] + ".darc"} {
			p := filepath.Join(backupDir, cand)
			if _, err := os.Stat(p); err == nil {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				removed = cand
				break
			}
		}
	}
	if removed == "" {
		t.Fatalf("no table file named by the manifest is in %s; manifest = %q", backupDir, manifest)
	}

	exec(
		"INSERT INTO t VALUES (2, 'two')",
		"CALL DOLT_COMMIT('-Am', 'second')",
	)

	// A damaged destination is not healed by retrying: both later syncs
	// fail with Dolt's error, which names the directory and the missing
	// table file. This is what `bd backup sync` now surfaces (it used to
	// misreport the damage as "no backup destination configured").
	for i := 1; i <= 2; i++ {
		err := BackupSync(ctx, conn, ExportBackupName)
		if err == nil {
			t.Fatalf("sync %d healed the destination after it lost table file %s (named by its manifest)", i, removed)
		}
		if !strings.Contains(err.Error(), "table file not found") {
			t.Errorf("sync %d error does not carry Dolt's missing-table-file text: %v", i, err)
		}
		if !strings.Contains(err.Error(), backupDir) {
			t.Errorf("sync %d error does not name the destination %s: %v", i, backupDir, err)
		}
	}

	// The recovery the hint and docs promise: move the damaged directory
	// aside (kept, not deleted) and sync again — the same path now holds a
	// fresh full-history backup.
	broken := backupDir + ".broken"
	if err := os.Rename(backupDir, broken); err != nil {
		t.Fatalf("move the damaged backup dir aside: %v", err)
	}
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := BackupToDir(ctx, conn, conn, backupDir); err != nil {
		t.Fatalf("sync after the damaged destination was moved aside: %v", err)
	}

	// The fresh backup restores every row, including the one committed
	// while the destination was broken.
	restored := name + "_restored"
	if err := BackupRestore(ctx, conn, "file://"+filepath.ToSlash(backupDir), restored, false); err != nil {
		t.Fatalf("restore from the fresh backup: %v", err)
	}
	var count int
	if err := root.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+restored+".t").Scan(&count); err != nil {
		t.Fatalf("count rows in the restored database: %v", err)
	}
	if count != 2 {
		t.Errorf("the restored database holds %d rows, want the 2 committed before the fresh backup", count)
	}
}
