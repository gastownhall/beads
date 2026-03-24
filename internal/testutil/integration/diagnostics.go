//go:build integration && !windows

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Diagnostics captures state information for debugging test failures.
type Diagnostics struct {
	t        *testing.T
	beadsDir string
}

// NewDiagnostics creates a diagnostics collector for the given .beads directory.
func NewDiagnostics(t *testing.T, beadsDir string) *Diagnostics {
	return &Diagnostics{t: t, beadsDir: beadsDir}
}

// CaptureOnFailure registers a t.Cleanup that dumps diagnostic info if the
// test has failed. Call early in the test setup.
func (d *Diagnostics) CaptureOnFailure() {
	d.t.Helper()
	d.t.Cleanup(func() {
		if !d.t.Failed() {
			return
		}
		d.t.Log("=== DIAGNOSTICS (test failed) ===")
		d.dumpStateFiles()
		d.dumpProcessList()
	})
}

// dumpStateFiles logs the content of PID and port files if they exist.
func (d *Diagnostics) dumpStateFiles() {
	for _, name := range []string{"dolt-server.pid", "dolt-server.port", "dolt-server.log"} {
		path := filepath.Join(d.beadsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			d.t.Logf("  %s: not found", name)
			continue
		}
		content := strings.TrimSpace(string(data))
		if len(content) > 500 {
			content = content[:500] + "... (truncated)"
		}
		d.t.Logf("  %s: %s", name, content)
	}
}

// dumpProcessList logs any dolt processes visible via /proc or ps.
func (d *Diagnostics) dumpProcessList() {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		d.t.Log("  /proc: not available")
		return
	}
	var doltProcs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Only numeric directories (PIDs).
		pid := entry.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil {
			continue
		}
		if strings.Contains(string(cmdline), "dolt") {
			doltProcs = append(doltProcs, fmt.Sprintf("PID %s: %s", pid, strings.ReplaceAll(string(cmdline), "\x00", " ")))
		}
	}
	if len(doltProcs) == 0 {
		d.t.Log("  dolt processes: none found")
	} else {
		d.t.Logf("  dolt processes (%d):", len(doltProcs))
		for _, p := range doltProcs {
			d.t.Logf("    %s", p)
		}
	}
}
