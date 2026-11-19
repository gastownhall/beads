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

// TestBackupSyncAfterMissingTableFileRecoversOnlyAfterMoveAside pins the
// GH#4070 (bd-5mwx39) recovery that `bd backup status`, the auto-backup
// warning and docs/reference/configuration.md promise. A backup directory
// whose manifest names a table file that is not in it fails every later
// DOLT_BACKUP('sync'): Dolt has no re-seed option and beads does not repair
// the destination. Moving the directory aside and syncing to a fresh one at
// the same path writes a complete backup that restores every row.
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
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := BackupToDir(ctx, conn, conn, backupDir); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Leave the backup in the reported state: the manifest names a table
	// file that is not in the directory.
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

	// (a) The damage is permanent: every later sync fails, naming the
	// missing file under the backup directory.
	for i := 1; i <= 2; i++ {
		err := BackupToDir(ctx, conn, conn, backupDir)
		if err == nil {
			t.Fatalf("sync %d after the backup lost table file %s succeeded; the recovery docs describe a failure that no longer happens", i, removed)
		}
		if !strings.Contains(err.Error(), "table file not found") || !strings.Contains(err.Error(), backupDir) {
			t.Fatalf("sync %d error = %v, want Dolt's \"table file not found\" naming %s", i, err, backupDir)
		}
	}

	// (b) Moving the directory aside lets a sync to the same path write a
	// fresh, complete backup.
	if err := os.Rename(backupDir, backupDir+".broken"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := BackupToDir(ctx, conn, conn, backupDir); err != nil {
		t.Fatalf("sync to the moved-aside path: %v", err)
	}

	// (c) The fresh backup restores both rows into a new database.
	url, err := RestoreSourceURL(backupDir)
	if err != nil {
		t.Fatalf("restore source: %v", err)
	}
	restored := name + "_restored"
	if err := BackupRestore(ctx, conn, url, restored, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var rows int
	if err := root.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+restored+"`.t").Scan(&rows); err != nil {
		t.Fatalf("count restored rows: %v", err)
	}
	if rows != 2 {
		t.Fatalf("restored database holds %d rows, want 2", rows)
	}
}
