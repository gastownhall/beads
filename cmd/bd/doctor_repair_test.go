package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildBDForTest(t *testing.T) string {
	t.Helper()
	exeName := "bd"
	if runtime.GOOS == "windows" {
		exeName = "bd.exe"
	}

	binDir := t.TempDir()
	exe := filepath.Join(binDir, exeName)
	cmd := exec.Command("go", "build", "-o", exe, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
	return exe
}

func mkTmpDirInTmp(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		// Fallback for platforms without /tmp (e.g. Windows).
		dir, err = os.MkdirTemp("", prefix)
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runBDSideDB(t *testing.T, exe, dir, dbPath string, args ...string) (string, error) {
	t.Helper()
	fullArgs := []string{"--db", dbPath}
	if len(args) > 0 && args[0] != "init" {
		fullArgs = append(fullArgs, "--no-daemon")
	}
	fullArgs = append(fullArgs, args...)

	cmd := exec.Command(exe, fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"BEADS_NO_DAEMON=1",
		"BEADS_DIR="+filepath.Join(dir, ".beads"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDoctorRepair_CorruptDatabase_RebuildFromJSONL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow repair test in short mode")
	}

	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-repair-*")
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

	// Corrupt the SQLite file (truncate) and verify doctor reports an integrity error.
	if err := os.Truncate(dbPath, 128); err != nil {
		t.Fatalf("truncate db: %v", err)
	}

	out, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--json")
	if err == nil {
		t.Fatalf("expected bd doctor to fail on corrupt db")
	}
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("doctor output missing JSON: %s", out)
	}
	var before doctorResult
	if err := json.Unmarshal([]byte(out[jsonStart:]), &before); err != nil {
		t.Fatalf("unmarshal doctor json: %v\n%s", err, out)
	}
	var foundIntegrity bool
	for _, c := range before.Checks {
		if c.Name == "Database Integrity" {
			foundIntegrity = true
			if c.Status != statusError {
				t.Fatalf("Database Integrity status=%q want %q", c.Status, statusError)
			}
		}
	}
	if !foundIntegrity {
		t.Fatalf("Database Integrity check not found")
	}

	// Attempt auto-repair.
	out, err = runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes")
	if err != nil {
		t.Fatalf("bd doctor --fix failed: %v\n%s", err, out)
	}

	// Doctor should now pass.
	out, err = runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--json")
	if err != nil {
		t.Fatalf("bd doctor after fix failed: %v\n%s", err, out)
	}
	jsonStart = strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("doctor output missing JSON: %s", out)
	}
	var after doctorResult
	if err := json.Unmarshal([]byte(out[jsonStart:]), &after); err != nil {
		t.Fatalf("unmarshal doctor json: %v\n%s", err, out)
	}
	if !after.OverallOK {
		t.Fatalf("expected overall_ok=true after repair")
	}

	// Data should still be present.
	out, err = runBDSideDB(t, bdExe, ws, dbPath, "list", "--json")
	if err != nil {
		t.Fatalf("bd list failed after repair: %v\n%s", err, out)
	}
	jsonStart = strings.Index(out, "[")
	if jsonStart < 0 {
		t.Fatalf("list output missing JSON array: %s", out)
	}
	var issues []map[string]any
	if err := json.Unmarshal([]byte(out[jsonStart:]), &issues); err != nil {
		t.Fatalf("unmarshal list json: %v\n%s", err, out)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue after repair, got %d", len(issues))
	}
}
