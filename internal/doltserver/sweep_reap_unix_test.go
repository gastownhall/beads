//go:build darwin || linux

package doltserver

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestReapServersReportsTheLeakedDirectory pins the sweep's Info line against
// a real process. The line is the FIRST thing a reader sees when a suite
// leaks, and until it carried the reaped server's working directory it named
// only a PID that had already been killed — nothing to grep, nothing to fix
// (wy-j2zc8q). The cwd is the temp tree the server was serving, which Go
// names after the test that created it.
func TestReapServersReportsTheLeakedDirectory(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stand-in server: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	server := SweptServer{PID: cmd.Process.Pid, Cwd: dir}
	var killed []SweptServer
	out := captureStderr(t, func() {
		// The identity probe is stubbed: this stand-in is a shell, not a
		// dolt binary, and the probe's own behavior is covered elsewhere.
		killed = reapServers([]SweptServer{server}, func(int) bool { return true })
	})

	if want := []SweptServer{server}; !reflect.DeepEqual(killed, want) {
		t.Fatalf("reapServers() = %v, want %v", killed, want)
	}
	if !strings.Contains(out, "Info: swept 1 orphaned test dolt sql-server process(es)") {
		t.Errorf("stderr = %q, want the Info line", out)
	}
	if want := "cwd=" + dir; !strings.Contains(out, want) {
		t.Errorf("stderr = %q, want it to name the leaked directory %q", out, want)
	}

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("reapServers left the stand-in server running")
	}
}
