//go:build chaos

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorRepair_CorruptDatabase_NotADatabase_RebuildFromJSONL(t *testing.T) {
	t.Skip("SQLite file corruption chaos test; not applicable to Dolt backend (bd-o0u)")
}

func TestDoctorRepair_CorruptDatabase_NoJSONL_FixFails(t *testing.T) {
	t.Skip("SQLite file corruption chaos test; not applicable to Dolt backend (bd-o0u)")
}

func TestDoctorRepair_CorruptDatabase_BacksUpSidecars(t *testing.T) {
	t.Skip("SQLite sidecar (-wal/-shm/-journal) backup test; Dolt has no sidecars (bd-o0u)")
}

func TestDoctorRepair_CorruptDatabase_WithRunningDaemon_FixSucceeds(t *testing.T) {
	t.Skip("SQLite file corruption with daemon test; not applicable to Dolt backend (bd-o0u)")
}

func TestDoctorRepair_JSONLIntegrity_MalformedLine_ReexportFromDB(t *testing.T) {
	t.Skip("SQLite JSONL re-export chaos test; not applicable to Dolt backend (bd-o0u)")
}

func TestDoctorRepair_DatabaseIntegrity_DBWriteLocked_ImportFailsFast(t *testing.T) {
	t.Skip("SQLite write-lock chaos test; Dolt uses server connections, not file locks (bd-o0u)")
}

func TestDoctorRepair_CorruptDatabase_ReadOnlyBeadsDir_PermissionsFixMakesWritable(t *testing.T) {
	t.Skip("SQLite file corruption + read-only dir chaos test; not applicable to Dolt backend (bd-o0u)")
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
