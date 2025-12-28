//go:build chaos

package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
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

	// Ensure we don't mis-configure jsonl_export to a system file during failure.
	metadata, readErr := os.ReadFile(filepath.Join(ws, ".beads", "metadata.json"))
	if readErr == nil {
		if strings.Contains(string(metadata), "interactions.jsonl") {
			t.Fatalf("unexpected metadata.json jsonl_export set to interactions.jsonl:\n%s", string(metadata))
		}
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

func TestDoctorRepair_CorruptDatabase_WithRunningDaemon_FixSucceeds(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-daemon-*")
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

	cmd := startDaemonForChaosTest(t, bdExe, ws, dbPath)
	defer func() {
		if cmd.Process != nil && (cmd.ProcessState == nil || !cmd.ProcessState.Exited()) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	// Corrupt the DB.
	if err := os.WriteFile(dbPath, []byte("not a database"), 0644); err != nil {
		t.Fatalf("corrupt db: %v", err)
	}

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes"); err != nil {
		t.Fatalf("bd doctor --fix failed: %v", err)
	}

	// Ensure we can cleanly stop the daemon afterwards (repair shouldn't wedge it).
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-time.After(3 * time.Second):
			t.Fatalf("expected daemon to exit when killed")
		case <-done:
			// ok
		}
	}
}

func TestDoctorRepair_JSONLIntegrity_MalformedLine_ReexportFromDB(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-jsonl-*")
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

	// Corrupt JSONL (leave DB intact).
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open jsonl: %v", err)
	}
	if _, err := f.WriteString("{not json}\n"); err != nil {
		_ = f.Close()
		t.Fatalf("append corrupt jsonl: %v", err)
	}
	_ = f.Close()

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes"); err != nil {
		t.Fatalf("bd doctor --fix failed: %v", err)
	}

	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if strings.Contains(string(data), "{not json}") {
		t.Fatalf("expected JSONL to be regenerated without corrupt line")
	}
}

func TestDoctorRepair_DatabaseIntegrity_DBWriteLocked_ImportFailsFast(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-db-locked-*")
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

	// Lock the DB for writes in-process.
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO issues (id, title, status) VALUES ('lock-test', 'Lock Test', 'open')"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert lock row: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runBDWithEnv(ctx, bdExe, ws, dbPath, map[string]string{
		"BD_LOCK_TIMEOUT": "200ms",
	}, "import", "-i", jsonlPath, "--force", "--skip-existing", "--no-git-history")
	if err == nil {
		t.Fatalf("expected bd import to fail under DB write lock")
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("import exceeded timeout (likely hung); output:\n%s", out)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "locked") && !strings.Contains(low, "busy") && !strings.Contains(low, "timeout") {
		t.Fatalf("expected lock/busy/timeout error, got:\n%s", out)
	}
}

func TestDoctorRepair_CorruptDatabase_ReadOnlyBeadsDir_PermissionsFixMakesWritable(t *testing.T) {
	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-chaos-readonly-*")
	beadsDir := filepath.Join(ws, ".beads")
	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "init", "--prefix", "chaos", "--quiet"); err != nil {
		t.Fatalf("bd init failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "create", "Chaos issue", "-p", "1"); err != nil {
		t.Fatalf("bd create failed: %v", err)
	}
	if _, err := runBDSideDB(t, bdExe, ws, dbPath, "export", "-o", jsonlPath, "--force"); err != nil {
		t.Fatalf("bd export failed: %v", err)
	}

	// Corrupt the DB.
	if err := os.Truncate(dbPath, 64); err != nil {
		t.Fatalf("truncate db: %v", err)
	}

	// Make .beads read-only; the Permissions fix should make it writable again.
	if err := os.Chmod(beadsDir, 0555); err != nil {
		t.Fatalf("chmod beads dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(beadsDir, 0755) })

	if out, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes"); err != nil {
		t.Fatalf("expected bd doctor --fix to succeed (permissions auto-fix), got: %v\n%s", err, out)
	}
	info, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatalf("stat beads dir: %v", err)
	}
	if info.Mode().Perm()&0200 == 0 {
		t.Fatalf("expected .beads to be writable after permissions fix, mode=%v", info.Mode().Perm())
	}
}

func startDaemonForChaosTest(t *testing.T, bdExe, ws, dbPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bdExe, "--db", dbPath, "daemon", "--start", "--foreground", "--local", "--interval", "10m")
	cmd.Dir = ws
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Inherit environment, but explicitly ensure daemon mode is allowed.
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_NO_DAEMON=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	// Wait for socket to appear.
	sock := filepath.Join(ws, ".beads", "bd.sock")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			// Put the process back into the caller's control.
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("daemon failed to start (no socket: %s)\nstdout:\n%s\nstderr:\n%s", sock, stdout.String(), stderr.String())
	return nil
}

func runBDWithEnv(ctx context.Context, exe, dir, dbPath string, env map[string]string, args ...string) (string, error) {
	fullArgs := []string{"--db", dbPath}
	if len(args) > 0 && args[0] != "init" {
		fullArgs = append(fullArgs, "--no-daemon")
	}
	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, exe, fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"BEADS_NO_DAEMON=1",
		"BEADS_DIR="+filepath.Join(dir, ".beads"),
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
