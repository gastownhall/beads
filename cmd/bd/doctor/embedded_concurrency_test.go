package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestCheckEmbeddedModeConcurrency_ExplicitServerPortWithoutDoltMode(t *testing.T) {
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	writeEmbeddedConcurrencyConfig(t, beadsDir, &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltServerPort: 3308,
	})
	writeEmbeddedConcurrencyLock(t, beadsDir)

	check := CheckEmbeddedModeConcurrency(tmpDir)
	if check.Status != StatusOK {
		t.Fatalf("expected StatusOK for explicit server-backed config, got %s: %s", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "Using server mode") {
		t.Fatalf("expected server mode message, got %q", check.Message)
	}
}

func TestCheckEmbeddedModeConcurrency_ServerRunningWithoutDoltMode(t *testing.T) {
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	writeEmbeddedConcurrencyConfig(t, beadsDir, &configfile.Config{
		Backend: configfile.BackendDolt,
	})
	writeEmbeddedConcurrencyLock(t, beadsDir)

	pid := startFakeDoltSQLServer(t)
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.pid"), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("13508"), 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	check := CheckEmbeddedModeConcurrency(tmpDir)
	if check.Status != StatusOK {
		t.Fatalf("expected StatusOK for running server-backed runtime, got %s: %s", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "Using server mode") {
		t.Fatalf("expected server mode message, got %q", check.Message)
	}
}

func TestCheckEmbeddedModeConcurrency_WarnsForEmbeddedLocks(t *testing.T) {
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	writeEmbeddedConcurrencyConfig(t, beadsDir, &configfile.Config{
		Backend: configfile.BackendDolt,
	})
	writeEmbeddedConcurrencyLock(t, beadsDir)

	check := CheckEmbeddedModeConcurrency(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for embedded lock indicators, got %s: %s", check.Status, check.Message)
	}
	if !strings.Contains(check.Detail, "noms LOCK") {
		t.Fatalf("expected detail to mention noms LOCK, got %q", check.Detail)
	}
}

func writeEmbeddedConcurrencyConfig(t *testing.T, beadsDir string, cfg *configfile.Config) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

func writeEmbeddedConcurrencyLock(t *testing.T, beadsDir string) {
	t.Helper()
	lockDir := filepath.Join(beadsDir, "dolt", ".dolt", "noms")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "LOCK"), []byte(""), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
}

func startFakeDoltSQLServer(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake dolt process helper is unix-only")
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	cmd := exec.Command(bash, "-lc", `exec -a "dolt sql-server" sleep 60`)
	if err := cmd.Start(); err != nil {
		t.Skipf("start fake dolt process: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
		if err == nil {
			cmdline := strings.TrimSpace(string(out))
			if strings.Contains(cmdline, "dolt") && strings.Contains(cmdline, "sql-server") {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Skip("fake dolt process did not expose the expected command line")
	return 0
}
