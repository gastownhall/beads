//go:build cgo && unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProxiedServerDirectLocalDoltStartAdoptsTrackedServer pins the GH#6585
// claim about the workspace bd does own: a second `bd dolt start` on a
// direct-local server bd started and still tracks adopts that server. It
// exits 0, leaves .beads/dolt-server.pid as it was, and reports the recorded
// PID, rather than launching a second server and repointing the pid file.
//
// It builds the capability matrix's direct-local topology, and is named
// TestProxiedServer* so the proxied-server tier (which runs that matrix)
// discovers it; it needs only the dolt binary, not a proxy.
func TestProxiedServerDirectLocalDoltStartAdoptsTrackedServer(t *testing.T) {
	bd := buildEmbeddedBD(t)
	w := directLocalServerTopology().make(t, bd)

	pidFile := filepath.Join(w.beadsDir, "dolt-server.pid")
	before, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("bd init --server left no pid file: %v", err)
	}
	recorded := strings.TrimSpace(string(before))

	stdout, stderr, code := w.run(t, bd, "dolt", "start")
	if code != 0 {
		t.Fatalf("second bd dolt start exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	after, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("pid file gone after the second start: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("second bd dolt start rewrote the pid file: %q -> %q", before, after)
	}
	m := regexp.MustCompile(`PID (\d+)`).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("second bd dolt start printed no PID:\n%s", stdout)
	}
	if m[1] != recorded {
		t.Errorf("second bd dolt start reported PID %s, want the recorded %s:\n%s", m[1], recorded, stdout)
	}
}
