package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDoltWorkspace(t *testing.T, workspaceDir string) (beadsDir string, doltDir string) {
	t.Helper()
	beadsDir = filepath.Join(workspaceDir, ".beads")
	doltDir = filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	metadata := `{
  "database": "dolt",
  "backend": "dolt"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	return beadsDir, doltDir
}

func TestDoltSingleProcess_ShouldAutoStartDaemonFalse(t *testing.T) {
	oldDBPath := dbPath
	t.Cleanup(func() { dbPath = oldDBPath })
	dbPath = ""

	ws := t.TempDir()
	beadsDir, _ := writeDoltWorkspace(t, ws)

	t.Setenv("BEADS_DIR", beadsDir)
	// Ensure the finder sees a workspace root (and not the repo running tests).
	oldWD, _ := os.Getwd()
	_ = os.Chdir(ws)
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if shouldAutoStartDaemon() {
		t.Fatalf("expected shouldAutoStartDaemon() to be false for dolt backend")
	}
}

func TestDoltSingleProcess_TryAutoStartDoesNotCreateStartlock(t *testing.T) {
	oldDBPath := dbPath
	t.Cleanup(func() { dbPath = oldDBPath })
	dbPath = ""

	ws := t.TempDir()
	beadsDir, _ := writeDoltWorkspace(t, ws)
	t.Setenv("BEADS_DIR", beadsDir)

	socketPath := filepath.Join(ws, "bd.sock")
	lockPath := socketPath + ".startlock"

	ok := tryAutoStartDaemon(socketPath)
	if ok {
		t.Fatalf("expected tryAutoStartDaemon() to return false for dolt backend")
	}
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatalf("expected startlock not to be created for dolt backend: %s", lockPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat startlock: %v", err)
	}
}

func TestDoltSingleProcess_DaemonGuardBlocksCommands(t *testing.T) {
	oldDBPath := dbPath
	t.Cleanup(func() { dbPath = oldDBPath })
	dbPath = ""

	ws := t.TempDir()
	beadsDir, _ := writeDoltWorkspace(t, ws)
	t.Setenv("BEADS_DIR", beadsDir)

	// Ensure help flag exists (cobra adds it during execution; for unit testing we add it explicitly).
	cmd := daemonCmd
	cmd.Flags().Bool("help", false, "help")
	err := guardDaemonUnsupportedForDolt(cmd, nil)
	if err == nil {
		t.Fatalf("expected daemon guard error for dolt backend")
	}
	if !strings.Contains(err.Error(), "single-process") {
		t.Fatalf("expected error to mention single-process, got: %v", err)
	}
}

// This test uses a helper subprocess because startDaemon calls os.Exit on failure.
func TestDoltSingleProcess_StartDaemonGuardrailExitsNonZero(t *testing.T) {
	if os.Getenv("BD_TEST_HELPER_STARTDAEMON") == "1" {
		// Helper mode: set up environment and invoke startDaemon (should os.Exit(1)).
		ws := os.Getenv("BD_TEST_WORKSPACE")
		_, doltDir := writeDoltWorkspace(t, ws)
		// Ensure FindDatabasePath can resolve.
		_ = os.Chdir(ws)
		_ = os.Setenv("BEADS_DB", doltDir)
		dbPath = ""

		pidFile := filepath.Join(ws, ".beads", "daemon.pid")
		startDaemon(5*time.Second, false, false, false, false, false, "", pidFile, "info", false)
		return
	}

	ws := t.TempDir()
	// Pre-create workspace structure so helper can just use it.
	_, doltDir := writeDoltWorkspace(t, ws)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	cmd := exec.Command(exe, "-test.run", "^TestDoltSingleProcess_StartDaemonGuardrailExitsNonZero$", "-test.v")
	cmd.Env = append(os.Environ(),
		"BD_TEST_HELPER_STARTDAEMON=1",
		"BD_TEST_WORKSPACE="+ws,
		"BEADS_DB="+doltDir,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output:\n%s", string(out))
	}
	if !strings.Contains(string(out), "daemon mode is not supported") {
		t.Fatalf("expected output to mention daemon unsupported; got:\n%s", string(out))
	}
}

