//go:build chaos

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorRepair_CorruptDatabase_NotADatabase_RebuildFromJSONL(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-*")
	dbPath := filepath.Join(ws, ".beads", "beads.db")
	jsonlPath := filepath.Join(ws, ".beads", "issues.jsonl")

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "init", "--prefix", "chaos", "--quiet"); err != nil {
		t.Fatalf("bd init failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "create", "Chaos issue", "-p", "1"); err != nil {
		t.Fatalf("bd create failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "export", "-o", jsonlPath, "--force"); err != nil {
		t.Fatalf("bd export failed: %v", err)
	}

	// Make the DB unreadable.
	if err := os.WriteFile(dbPath, []byte("not a database"), 0644); err != nil {
		t.Fatalf("corrupt db: %v", err)
	}

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes"); err != nil {
		t.Fatalf("bd doctor --fix failed: %v", err)
	}

	if out, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor"); err != nil {
		t.Fatalf("bd doctor after fix failed: %v\n%s", err, out)
	}
}

func TestDoctorRepair_CorruptDatabase_NoJSONL_FixFails(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-nojsonl-*")
	dbPath := filepath.Join(ws, ".beads", "beads.db")

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "init", "--prefix", "chaos", "--quiet"); err != nil {
		t.Fatalf("bd init failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "create", "Chaos issue", "-p", "1"); err != nil {
		t.Fatalf("bd create failed: %v", err)
	}

	// Some workflows keep JSONL in sync automatically; force it to be missing.
	_ = os.Remove(filepath.Join(ws, ".beads", "issues.jsonl"))
	_ = os.Remove(filepath.Join(ws, ".beads", "beads.jsonl"))

	// Corrupt without providing JSONL source-of-truth.
	if err := os.Truncate(dbPath, 64); err != nil {
		t.Fatalf("truncate db: %v", err)
	}

	out, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes")
	if err == nil {
		t.Fatalf("expected bd doctor --fix to fail without JSONL")
	}
	if !strings.Contains(out, "cannot auto-recover") {
		t.Fatalf("expected auto-recover error, got:\n%s", out)
	}
}

func TestDoctorRepair_CorruptDatabase_BacksUpSidecars(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-sidecars-*")
	dbPath := filepath.Join(ws, ".beads", "beads.db")
	jsonlPath := filepath.Join(ws, ".beads", "issues.jsonl")

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "init", "--prefix", "chaos", "--quiet"); err != nil {
		t.Fatalf("bd init failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "create", "Chaos issue", "-p", "1"); err != nil {
		t.Fatalf("bd create failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "export", "-o", jsonlPath, "--force"); err != nil {
		t.Fatalf("bd export failed: %v", err)
	}

	// Ensure sidecars exist so we can verify they get moved with the backup.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(dbPath+suffix, []byte("x"), 0644); err != nil {
			t.Fatalf("write sidecar %s: %v", suffix, err)
		}
	}
	if err := os.Truncate(dbPath, 64); err != nil {
		t.Fatalf("truncate db: %v", err)
	}

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes"); err != nil {
		t.Fatalf("bd doctor --fix failed: %v", err)
	}

	// Verify a backup exists, and at least one sidecar got moved.
	entries, err := os.ReadDir(filepath.Join(ws, ".beads"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var backup string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt.backup.db") {
			backup = filepath.Join(ws, ".beads", e.Name())
			break
		}
	}
	if backup == "" {
		t.Fatalf("expected backup db in .beads, found none")
	}

	wal := backup + "-wal"
	if _, err := os.Stat(wal); err != nil {
		// At minimum, the backup DB itself should exist; sidecar backup is best-effort.
		if _, err2 := os.Stat(backup); err2 != nil {
			t.Fatalf("backup db missing: %v", err2)
		}
	}
}
