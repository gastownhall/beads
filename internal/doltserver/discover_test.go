//go:build !windows

package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDiscoverServers_Attribution: a dolt sql-server is bd's only when the
// server directory its --config argument (or, failing that, its working
// directory) points at has a dolt-server.pid naming it. Everything else,
// Gas City cities among it, is listed as not managed by bd.
func TestDiscoverServers_Attribution(t *testing.T) {
	root := t.TempDir()
	workspace := func(name string, pid, port int) string {
		dir := filepath.Join(root, name, ".beads")
		if err := os.MkdirAll(filepath.Join(dir, "dolt"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pidPath(dir), []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(portPath(dir), []byte(fmt.Sprintf("%d\n", port)), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	autoA := workspace("proj-a", 101, 41001)
	explicitB := workspace("proj b", 102, 41002) // a path with a space
	legacyC := workspace("proj-c", 103, 41003)
	staleD := workspace("proj-d", 999, 41004) // pid file names another process
	cityDir := filepath.Join(root, "city", ".gc", "dolt")

	// proj-a was auto-started with a watcher (this test process stands in
	// for the live watcher); proj b was started with bd dolt start; proj-c
	// by a bd that predates idle stop.
	if err := ensureActivityDir(autoA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(watcherPIDPath(autoA), []byte(fmt.Sprintf("%d\n30m0s\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	recordIdleOff(explicitB, IdleOffExplicitStart)

	snapshot := fmt.Sprintf(`  101 S    /usr/local/bin/dolt sql-server --config %s
  102 S    dolt sql-server --config=%s
  103 S    dolt sql-server -H 127.0.0.1 -P 41003
  104 S    dolt sql-server --config %s
  105 S    dolt sql-server --config %s
  106 Z    dolt sql-server --config %s
  107 S    vim notes-about-dolt.txt
`,
		filepath.Join(autoA, doltServerConfigFileName),
		filepath.Join(explicitB, doltServerConfigFileName),
		filepath.Join(cityDir, "config.yaml"),
		filepath.Join(staleD, doltServerConfigFileName),
		filepath.Join(autoA, doltServerConfigFileName))
	rows := parseDoltProcessRows([]byte(snapshot))
	cwds := map[int]string{103: filepath.Join(legacyC, "dolt"), 104: cityDir}
	got := map[int]DiscoveredServer{}
	for _, row := range rows {
		got[row.PID] = attributeDoltProcess(row, func(pid int) string { return cwds[pid] })
	}
	if len(got) != 5 {
		t.Fatalf("listed PIDs %v, want the five live dolt sql-servers 101-105", got)
	}

	for _, want := range []struct {
		pid       int
		serverDir string
		port      int
		idleStop  string
	}{
		{101, autoA, 41001, "after 30m"},
		{102, explicitB, 41002, "off: started with bd dolt start"},
		{103, legacyC, 41003, "off: no watcher (started by an older bd)"},
	} {
		s := got[want.pid]
		if !s.Managed || s.ServerDir != want.serverDir || s.Port != want.port {
			t.Errorf("PID %d = %+v, want managed by bd in %s on port %d", want.pid, s, want.serverDir, want.port)
		}
		if s.IdleStop.String() != want.idleStop {
			t.Errorf("PID %d idle stop = %q, want %q", want.pid, s.IdleStop, want.idleStop)
		}
		if s.StartedAt.IsZero() || time.Since(s.StartedAt) > time.Hour {
			t.Errorf("PID %d started at %v, want the pid file's mtime", want.pid, s.StartedAt)
		}
	}
	for _, pid := range []int{104, 105} {
		if s := got[pid]; s.Managed || s.ServerDir != "" || s.Command == "" {
			t.Errorf("PID %d = %+v, want listed as not managed by bd, with its command", pid, s)
		}
	}
}

// TestAutoStart_RecordsWhyIdleStopIsOff: an auto-start that launches no
// watcher records why, so bd dolt status reports it.
func TestAutoStart_RecordsWhyIdleStopIsOff(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	t.Setenv(IdleTimeoutEnv, "0")
	stubIdleWatchSpawner(t)
	autoStartForTest(t, beadsDir)
	if got, want := ReadIdleStopState(beadsDir).String(), "off: dolt.idle-timeout is 0"; got != want {
		t.Errorf("idle stop = %q, want %q", got, want)
	}
}
