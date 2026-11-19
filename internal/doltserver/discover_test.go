package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeDiscoveredWorkspace gives serverDir the state files of a bd-managed
// server running as pid on port: the pid file naming the process and the
// port file naming its listener.
func writeDiscoveredWorkspace(t *testing.T, serverDir string, pid, port int) {
	t.Helper()
	if err := os.WriteFile(pidPath(serverDir), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portPath(serverDir), []byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverServers_Attribution pins how a process-table row becomes a
// listed server (GH#4282): bd's own servers are attributed by their
// --config argument or their working directory and confirmed by the pid
// file naming them; a foreign sql-server is never bd's, however it happens
// to be running.
func TestDiscoverServers_Attribution(t *testing.T) {
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "")
	origRows, origCwd := doltProcessRows, processCwd
	t.Cleanup(func() { doltProcessRows, processCwd = origRows, origCwd })

	wsConfig := t.TempDir() // attributed by its --config argument
	writeDiscoveredWorkspace(t, wsConfig, 41001, 33101)
	// A dead watcher's marker: the window is set but no watcher runs.
	if err := os.MkdirAll(activityDir(wsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(watcherPIDPath(wsConfig), []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}

	wsCwd := t.TempDir() // attributed by its working directory
	writeDiscoveredWorkspace(t, wsCwd, 41002, 33102)
	// A live watcher (this process) stands over it.
	if err := os.MkdirAll(activityDir(wsCwd), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(watcherPIDPath(wsCwd), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	cwdByPID := map[int]string{
		41002: filepath.Join(wsCwd, "dolt"),
		41003: "/opt/foreign",
		41004: filepath.Join(wsConfig, "dolt"),
	}
	processCwd = func(pid int) string { return cwdByPID[pid] }

	rows := []doltProcessRow{
		{PID: 41001, Uptime: 4*time.Minute + 12*time.Second,
			Command: "dolt sql-server --config " + filepath.Join(wsConfig, doltServerConfigFileName) + " -H 127.0.0.1"},
		{PID: 41002, Uptime: time.Hour,
			Command: "dolt sql-server -H 127.0.0.1 -P 33102"},
		// A foreign sql-server: nothing bd wrote points at it.
		{PID: 41003, Uptime: 2 * time.Hour,
			Command: "/usr/local/bin/dolt sql-server --data-dir /opt/foreign/data"},
		// A --config bd does not write, with a cwd bd does: the cwd wins
		// for attribution — but bd-managed is the pid file's call, and that
		// file names the process that actually serves the workspace, so
		// this one is listed at the workspace without being bd's.
		{PID: 41004, Uptime: time.Minute,
			Command: "dolt sql-server --config /opt/foreign/server.yaml"},
	}
	doltProcessRows = func() ([]doltProcessRow, error) { return rows, nil }

	servers, err := DiscoverServers()
	if err != nil {
		t.Fatalf("DiscoverServers: %v", err)
	}
	if len(servers) != len(rows) {
		t.Fatalf("discovered %d servers, want %d", len(servers), len(rows))
	}

	got := servers[0]
	if !got.BDManaged || got.ServerDir != wsConfig || got.Port != 33101 {
		t.Errorf("config-attributed server = %+v, want bd-managed at %s port 33101", got, wsConfig)
	}
	if want := "idle after " + DefaultIdleTimeout.String() + ", no watcher running"; got.IdleStop != want {
		t.Errorf("idle stop with a dead watcher = %q, want %q", got.IdleStop, want)
	}

	got = servers[1]
	if !got.BDManaged || got.ServerDir != wsCwd || got.Port != 33102 {
		t.Errorf("cwd-attributed server = %+v, want bd-managed at %s port 33102", got, wsCwd)
	}
	if want := "stops after " + DefaultIdleTimeout.String() + " idle"; got.IdleStop != want {
		t.Errorf("idle stop with a live watcher = %q, want %q", got.IdleStop, want)
	}

	got = servers[2]
	if got.BDManaged || got.ServerDir != "" || got.Port != 0 || got.IdleStop != "" {
		t.Errorf("foreign server = %+v, want unattributed and not bd-managed", got)
	}

	got = servers[3]
	if got.BDManaged || got.ServerDir != wsConfig {
		t.Errorf("foreign-config/cwd-attributed server = %+v, want %s with a pid file that does not name it (not bd-managed)", got, wsConfig)
	}
}

// TestParseDoltProcessRows pins the ps snapshot parser: dolt sql-server
// rows in order, zombies skipped, near-miss commands skipped, etime parsed.
func TestParseDoltProcessRows(t *testing.T) {
	snapshot := []byte("" +
		"  41001 S  1-02:03:04 /usr/bin/dolt sql-server -H 127.0.0.1\n" +
		"41002 R  02:03:04 dolt sql-server\n" +
		"41003 Z  03:04 dolt sql-server\n" +
		"41004 S  03:04 dolt sql\n" +
		"41005 S  10:04 tail -f dolt-server.log\n" +
		"\n")
	rows := parseDoltProcessRows(snapshot)
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2 (zombie and near-miss rows skipped): %+v", len(rows), rows)
	}
	if rows[0].PID != 41001 || rows[0].Uptime != 26*time.Hour+3*time.Minute+4*time.Second {
		t.Errorf("row 0 = %+v, want PID 41001 up 26h3m4s", rows[0])
	}
	if rows[1].PID != 41002 || rows[1].Uptime != 2*time.Hour+3*time.Minute+4*time.Second {
		t.Errorf("row 1 = %+v, want PID 41002 up 2h3m4s", rows[1])
	}
}

// TestParseEtime pins ps's elapsed-time spellings.
func TestParseEtime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"04", 4 * time.Second},
		{"03:04", 3*time.Minute + 4*time.Second},
		{"02:03:04", 2*time.Hour + 3*time.Minute + 4*time.Second},
		{"1-02:03:04", 26*time.Hour + 3*time.Minute + 4*time.Second},
		{"2-00:00:00", 48 * time.Hour},
		{"soon", 0},
		{"1:2:3:4", 0},
	}
	for _, tc := range cases {
		if got := parseEtime(tc.in); got != tc.want {
			t.Errorf("parseEtime(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestServerDirFromConfigArg pins the --config attribution spellings: only
// the config file bd itself writes names a workspace.
func TestServerDirFromConfigArg(t *testing.T) {
	const ws = "/w/repo/.beads"
	if got := serverDirFromConfigArg("dolt sql-server --config " + ws + "/" + doltServerConfigFileName); got != ws {
		t.Errorf("--config path: %q, want %q", got, ws)
	}
	if got := serverDirFromConfigArg("dolt sql-server --config=" + ws + "/" + doltServerConfigFileName + " -H 127.0.0.1"); got != ws {
		t.Errorf("--config=path: %q, want %q", got, ws)
	}
	if got := serverDirFromConfigArg("dolt sql-server --config /opt/foreign/server.yaml"); got != "" {
		t.Errorf("a foreign config file: %q, want unattributed", got)
	}
	if got := serverDirFromConfigArg("dolt sql-server"); got != "" {
		t.Errorf("no --config: %q, want unattributed", got)
	}
}
