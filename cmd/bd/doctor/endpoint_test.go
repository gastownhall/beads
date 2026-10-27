package doctor

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// doctorEndpointWorkspace is a server-mode workspace with no local data
// directory and every Dolt endpoint setting cleared, plus its repo path.
func doctorEndpointWorkspace(t *testing.T) (repo, beadsDir string) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_PASSWORD", "BEADS_DOLT_AUTO_START", "BEADS_DOLT_SERVER_USER",
		"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DIR",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(home, "no-credentials"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	repo = t.TempDir()
	beadsDir = filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "doctor_endpoint"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	// A tracked issues.jsonl makes the Fresh Clone check look for the
	// database, on the server in server mode.
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"t-1","title":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, beadsDir
}

func closedLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// doctorEndpointReports is what each bd doctor entry point that reaches the
// Dolt server reports for the workspace at repo.
func doctorEndpointReports(t *testing.T, repo, beadsDir string) map[string]string {
	t.Helper()
	reports := map[string]string{}
	if _, _, err := openDoltDB(beadsDir); err != nil {
		reports["Dolt Connection (openDoltDB)"] = err.Error()
	} else {
		reports["Dolt Connection (openDoltDB)"] = "connected"
	}
	for _, c := range RunDoltHealthChecks(repo) {
		if c.Name == "Dolt Connection" {
			reports["bd doctor Dolt Connection"] = c.Message + "\n" + c.Detail + "\n" + c.Fix
		}
	}
	fresh := CheckFreshClone(repo)
	reports["bd doctor Fresh Clone"] = fresh.Message + "\n" + fresh.Detail + "\n" + fresh.Fix
	var server strings.Builder
	for _, c := range RunServerHealthChecks(repo).Checks {
		server.WriteString(string(c.Status) + " " + c.Name + ": " + c.Message + "\n" + c.Detail + "\n" + c.Fix + "\n")
	}
	reports["bd doctor --server"] = server.String()
	return reports
}

// TestDoctorChecksDialTheEndpointTheStoreDials pins the invariant at bd
// doctor's entry points: with only the legacy BEADS_DOLT_PORT naming the
// server, every check that reaches it names the port the store dials. Plain
// bd doctor used to report "no Dolt server port configured and no server
// running; run any bd command to auto-start" and its Fresh Clone check
// probed 127.0.0.1:0, while bd doctor --server and bd list reached the
// server (#7803 review).
func TestDoctorChecksDialTheEndpointTheStoreDials(t *testing.T) {
	repo, beadsDir := doctorEndpointWorkspace(t)
	port := strconv.Itoa(closedLocalPort(t))
	t.Setenv("BEADS_DOLT_PORT", port)
	for name, report := range doctorEndpointReports(t, repo, beadsDir) {
		if !strings.Contains(report, "127.0.0.1:"+port) {
			t.Errorf("%s does not name 127.0.0.1:%s, the endpoint the store dials:\n%s", name, port, report)
		}
		for _, banned := range []string{"127.0.0.1:0", "no Dolt server port", "auto-start"} {
			if strings.Contains(report, banned) {
				t.Errorf("%s contains %q:\n%s", name, banned, report)
			}
		}
	}
}

// TestDoctorChecksNeverDialAnUnresolvedPort pins the invariant at bd
// doctor's entry points for a fresh clone of a `bd init --server` project
// before any server runs: no port is named, and no check dials 127.0.0.1:0.
// When the next bd command starts the workspace's own server, the Fresh Clone
// check reports the fresh clone and points at bd bootstrap, which clones into
// that server's data directory; otherwise it gives the store's advice, which
// bd bootstrap and every store open refuse with too. The check used to probe
// 127.0.0.1:0 and report "Dolt server unreachable at 127.0.0.1:0 ... this is
// not a fresh clone — it's a connectivity or auth problem" (#7841 review).
func TestDoctorChecksNeverDialAnUnresolvedPort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoStart string
		wantFresh string
	}{
		{name: "bd starts its own server", wantFresh: "Run 'bd bootstrap'"},
		{name: "auto-start off", autoStart: "0", wantFresh: "dolt.auto-start is off, so bd does not start this workspace's Dolt server."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, beadsDir := doctorEndpointWorkspace(t)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStart)
			reports := doctorEndpointReports(t, repo, beadsDir)
			if fresh := reports["bd doctor Fresh Clone"]; !strings.Contains(fresh, tc.wantFresh) {
				t.Errorf("bd doctor Fresh Clone does not say %q:\n%s", tc.wantFresh, fresh)
			}
			for name, report := range reports {
				for _, banned := range []string{"127.0.0.1:0", "connection refused", "connectivity or auth problem"} {
					if strings.Contains(report, banned) {
						t.Errorf("%s contains %q: it dialed a port nothing resolved:\n%s", name, banned, report)
					}
				}
			}
		})
	}
}

// TestDoctorChecksDialAConfiguredSocket pins doctor's Fresh Clone check to the
// transport the store dials for a server-mode workspace whose server only a
// unix socket names: the socket. Nothing listens on it here, so the report
// names the socket it dialed; it probed 127.0.0.1:0 (#7841 review).
//
// Plain doctor's connection (openDoltDB) still does not dial the socket, and
// that is pinned here too: its failure keeps the schema fix gate withholding
// the DB fixes for a socket-only server. Admitting them there waits on a
// separate data-safety fix to the Database fix.
func TestDoctorChecksDialAConfiguredSocket(t *testing.T) {
	repo, beadsDir := doctorEndpointWorkspace(t)
	socket := filepath.Join(t.TempDir(), "dolt.sock")
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
	fresh := CheckFreshClone(repo)
	report := fresh.Message + "\n" + fresh.Detail + "\n" + fresh.Fix
	if !strings.Contains(report, socket) {
		t.Errorf("Fresh Clone does not name the socket the store dials, %s:\n%s", socket, report)
	}
	if strings.Contains(report, "127.0.0.1:0") {
		t.Errorf("Fresh Clone dialed 127.0.0.1:0:\n%s", report)
	}
	db, _, err := openDoltDB(beadsDir)
	if err == nil {
		_ = db.Close()
		t.Fatal("openDoltDB opened a socket-only server; the schema fix gate would admit the DB fixes")
	}
	if strings.Contains(err.Error(), socket) {
		t.Errorf("openDoltDB dialed the socket (%v); the schema fix gate would admit the DB fixes", err)
	}
}

// TestDoctorFreshCloneReadsTheServerItsRepairWritesBesideAPort pins the
// Fresh Clone check, when a live unix socket and a resolved port both name
// the server, to the server its repair (fix.FreshCloneImport) writes, and its
// report to the server it dialed. That repair's store open dials
// BEADS_DOLT_SERVER_SOCKET socket first and never reads metadata.json's
// dolt_server_socket, so the check dials the socket only from the
// environment, and TCP at the port beside metadata.json's. It dialed
// metadata.json's socket too, while --fix imported issues.jsonl into the
// server at the port over its live rows; before that it named host:port for
// the socket it dialed (#7874 review). Nothing answers either address here,
// so the report names the one dialed.
func TestDoctorFreshCloneReadsTheServerItsRepairWritesBesideAPort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	for _, tc := range []struct {
		name  string
		inEnv bool // BEADS_DOLT_SERVER_SOCKET, else metadata.json dolt_server_socket
	}{
		{name: "BEADS_DOLT_SERVER_SOCKET, the socket", inEnv: true},
		{name: "metadata.json dolt_server_socket, TCP at the port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, beadsDir := doctorEndpointWorkspace(t)
			socket := liveUnixSocket(t)
			port := strconv.Itoa(closedLocalPort(t))
			t.Setenv("BEADS_DOLT_SERVER_PORT", port)
			dialed, other := "unix:"+socket, "127.0.0.1:"+port
			if tc.inEnv {
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
			} else {
				cfg, err := configfile.Load(beadsDir)
				if err != nil || cfg == nil {
					t.Fatalf("load metadata: %v", err)
				}
				cfg.DoltServerSocket = socket
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
				dialed, other = other, socket
			}
			fresh := CheckFreshClone(repo)
			report := fresh.Message + "\n" + fresh.Detail + "\n" + fresh.Fix
			if !strings.Contains(fresh.Message, dialed) {
				t.Errorf("Fresh Clone does not name %s, the server its repair writes:\n%s", dialed, report)
			}
			if strings.Contains(report, other) {
				t.Errorf("Fresh Clone names %s, which its repair does not write:\n%s", other, report)
			}
		})
	}
}

// TestDoctorFreshCloneFailsClosedOnAFailingCredentialCommand pins the Fresh
// Clone check to the identity its repair presents (freshCloneRepairUser) when
// BEADS_DOLT_CREDENTIAL_COMMAND fails: the repair's store open fails closed
// and never falls back to the static user, and the check does too, reporting
// the command's failure without dialing the server.
func TestDoctorFreshCloneFailsClosedOnAFailingCredentialCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the credential command is a POSIX shell command")
	}
	repo, _ := doctorEndpointWorkspace(t)
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(closedLocalPort(t)))
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "exit 23")
	fresh := CheckFreshClone(repo)
	report := fresh.Message + "\n" + fresh.Detail + "\n" + fresh.Fix
	if fresh.Status != StatusWarning || !strings.Contains(fresh.Detail, "BEADS_DOLT_CREDENTIAL_COMMAND") {
		t.Errorf("Fresh Clone does not report the failing BEADS_DOLT_CREDENTIAL_COMMAND:\n%s", report)
	}
	if strings.Contains(report, "connection refused") {
		t.Errorf("Fresh Clone dialed the server after its credential command failed:\n%s", report)
	}
}

// liveUnixSocket is a unix socket whose listener closes every connection
// unanswered: live to the store's socket probe, a failed login to a dial. Its
// path is short enough to bind; t.TempDir() nests too deeply under some
// runners.
func liveUnixSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "dolt.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return path
}

// TestDoltModeCheckProbesTheEndpointTheStoreDials pins the "Dolt Mode"
// check's reachability probe (doltModeServerReachable) to the server the store
// dials. It probed DefaultConfig's port, which leaves the legacy
// BEADS_DOLT_PORT out, so with only that variable naming a running server the
// check read it as unreachable, and with federation peers configured warned
// "Server not reachable" (#7874 review). A closed BEADS_DOLT_SERVER_PORT is
// the control.
func TestDoltModeCheckProbesTheEndpointTheStoreDials(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		live bool
	}{
		{name: "legacy BEADS_DOLT_PORT only, server running", env: "BEADS_DOLT_PORT", live: true},
		{name: "BEADS_DOLT_SERVER_PORT, nothing listening", env: "BEADS_DOLT_SERVER_PORT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, beadsDir := doctorEndpointWorkspace(t)
			port := closedLocalPort(t)
			if tc.live {
				port = acceptingLocalPort(t)
			}
			t.Setenv(tc.env, strconv.Itoa(port))
			cfg, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := doltModeServerReachable(beadsDir, cfg); got != tc.live {
				t.Errorf("Dolt Mode server reachable = %v with %s=%d, want %v: it did not probe the port the store dials", got, tc.env, port, tc.live)
			}
		})
	}
}

// acceptingLocalPort is a 127.0.0.1 port whose listener accepts and closes
// every connection: reachable to a probe.
func acceptingLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestDoctorRepairScansReportARepairServerTheyCannotRead pins what the
// Blocked State, Dependency Keys, Orphaned Dependencies and Child-Parent
// Dependencies checks report when they cannot read the server their bd
// doctor --fix repair writes (fix.OpenScanDB): their no-database result when
// the shared store found no database either, and otherwise a warning naming
// the failure. They read the store's server, which dials a live
// BEADS_DOLT_SERVER_SOCKET beside a resolved port while the repair writes
// the server at the port (#7874 review); that server can refuse a check the
// shared store let in, as a stopped server at the port does here, and a
// clean "No database yet" or "N/A (no database)" would hide a database the
// check never read.
func TestDoctorRepairScansReportARepairServerTheyCannotRead(t *testing.T) {
	repo, _ := doctorEndpointWorkspace(t)
	port := strconv.Itoa(closedLocalPort(t))
	t.Setenv("BEADS_DOLT_SERVER_PORT", port)
	// opened stands in for a shared store that opened the workspace's
	// database; the checks only ask whether it did.
	opened := &SharedStore{store: new(dolt.DoltStore)}
	for _, tc := range []struct {
		name       string
		check      func(string, *SharedStore) DoctorCheck
		noDatabase string // the message with no database at all
	}{
		{"Blocked State", CheckBlockedConsistencyWithStore, "No database yet"},
		{"Dependency Keys", CheckDependencyKeysWithStore, "No database yet"},
		{"Orphaned Dependencies", CheckOrphanedDependencies, "N/A (no database)"},
		{"Child-Parent Dependencies", CheckChildParentDependencies, "N/A (no database)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if c := tc.check(repo, nil); c.Status != StatusOK || c.Message != tc.noDatabase {
				t.Errorf("with no database: %s %q, want %s %q", c.Status, c.Message, StatusOK, tc.noDatabase)
			}
			c := tc.check(repo, opened)
			if c.Status != StatusWarning || !strings.Contains(c.Detail, "127.0.0.1:"+port) {
				t.Errorf("with a database whose repair server refuses the check: %s %q (%s), want a warning naming 127.0.0.1:%s", c.Status, c.Message, c.Detail, port)
			}
		})
	}
}
