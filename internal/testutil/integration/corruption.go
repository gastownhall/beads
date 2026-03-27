//go:build integration && !windows

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// StateCorruptor creates dirty server state for testing recovery scenarios.
type StateCorruptor struct {
	BeadsDir string
	t        *testing.T
}

// NewStateCorruptor creates a corruptor targeting the given .beads directory.
func NewStateCorruptor(t *testing.T, beadsDir string) *StateCorruptor {
	t.Helper()
	return &StateCorruptor{BeadsDir: beadsDir, t: t}
}

// WriteStalePID writes a PID file containing the given PID.
func (c *StateCorruptor) WriteStalePID(pid int) {
	c.t.Helper()
	path := filepath.Join(c.BeadsDir, "dolt-server.pid")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d", pid)), 0600); err != nil {
		c.t.Fatalf("WriteStalePID: %v", err)
	}
}

// WriteStalePort writes a port file containing the given port number.
func (c *StateCorruptor) WriteStalePort(port int) {
	c.t.Helper()
	path := filepath.Join(c.BeadsDir, "dolt-server.port")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d", port)), 0600); err != nil {
		c.t.Fatalf("WriteStalePort: %v", err)
	}
}

// WriteCorruptPID writes a non-numeric value to the PID file.
func (c *StateCorruptor) WriteCorruptPID() {
	c.t.Helper()
	path := filepath.Join(c.BeadsDir, "dolt-server.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0600); err != nil {
		c.t.Fatalf("WriteCorruptPID: %v", err)
	}
}

// WriteTruncatedMetadata writes partial (invalid) JSON to metadata.json.
func (c *StateCorruptor) WriteTruncatedMetadata() {
	c.t.Helper()
	path := filepath.Join(c.BeadsDir, "metadata.json")
	if err := os.WriteFile(path, []byte(`{"backend": "dolt", "dolt_da`), 0600); err != nil {
		c.t.Fatalf("WriteTruncatedMetadata: %v", err)
	}
}

// WritePortZero writes "0" to the port file (GH#2598 regression).
func (c *StateCorruptor) WritePortZero() {
	c.t.Helper()
	c.WriteStalePort(0)
}

// CreateOrphanNomsLock creates a zero-byte LOCK file at .dolt/noms/LOCK
// to simulate a power-loss scenario.
func (c *StateCorruptor) CreateOrphanNomsLock(doltDataDir string) {
	c.t.Helper()
	nomsDir := filepath.Join(doltDataDir, "noms")
	if err := os.MkdirAll(nomsDir, 0700); err != nil {
		c.t.Fatalf("CreateOrphanNomsLock: mkdir: %v", err)
	}
	lockPath := filepath.Join(nomsDir, "LOCK")
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		c.t.Fatalf("CreateOrphanNomsLock: write: %v", err)
	}
}

// CreateCombinedStaleState writes both stale PID and stale port files.
func (c *StateCorruptor) CreateCombinedStaleState(pid, port int) {
	c.t.Helper()
	c.WriteStalePID(pid)
	c.WriteStalePort(port)
}

// PIDFilePath returns the expected PID file path.
func (c *StateCorruptor) PIDFilePath() string {
	return filepath.Join(c.BeadsDir, "dolt-server.pid")
}

// PortFilePath returns the expected port file path.
func (c *StateCorruptor) PortFilePath() string {
	return filepath.Join(c.BeadsDir, "dolt-server.port")
}

// FileExists returns true if the given path exists.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
