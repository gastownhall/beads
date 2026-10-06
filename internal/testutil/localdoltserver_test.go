//go:build !windows

package testutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// serverFingerprint is what a test can observe about "the Dolt test server"
// over SQL. Both backends must produce wantServerFingerprint: the container
// because that is what CI has always run against, the local backend because
// it has to be indistinguishable from it. A Dolt or image bump that changes
// any of these defaults fails here first (design D6 §2).
type serverFingerprint struct {
	Users        []string // user@host, sorted
	TestGrants   []string // SHOW GRANTS FOR 'test'@'%'
	Databases    []string // SHOW DATABASES, sorted
	CurrentUser  string   // CURRENT_USER() when connecting as root
	Version      string
	MaxConns     string
	SQLMode      string
	SystemTZ     string
	TimeZone     string
	Autocommit   string
	SecureFilePv string // normalized, see fingerprintServer
	CharsetSrv   string
	CollationSrv string
	MaxPacket    string
	TxIsolation  string
	LowerCaseTbl string
	LowerCaseFS  string // normalized off Linux, see fingerprintServer
	WaitTimeout  string
	LockWait     string // innodb_lock_wait_timeout
}

var wantServerFingerprint = serverFingerprint{
	Users: []string{
		"__dolt_local_user__@localhost",
		"event_scheduler@localhost",
		"root@%",
		"test@%",
	},
	TestGrants: []string{
		"GRANT USAGE ON *.* TO `test`@`%`",
		"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER, CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, EVENT, TRIGGER ON `beads_test`.* TO `test`@`%`",
	},
	Databases:    []string{"beads_test", "information_schema", "mysql"},
	CurrentUser:  "root@%",
	Version:      "8.0.31",
	MaxConns:     "151",
	SQLMode:      "NO_ENGINE_SUBSTITUTION,ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES",
	SystemTZ:     "UTC",
	TimeZone:     "SYSTEM",
	Autocommit:   "1",
	SecureFilePv: "",
	CharsetSrv:   "utf8mb4",
	CollationSrv: "utf8mb4_0900_bin",
	MaxPacket:    "1073741824",
	TxIsolation:  "REPEATABLE-READ",
	LowerCaseTbl: "0",
	LowerCaseFS:  "0",
	WaitTimeout:  "28800",
	LockWait:     "1",
}

func queryStrings(ctx context.Context, db *sql.DB, q string) ([]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("%s: %w", q, err)
		}
		out = append(out, s.String)
	}
	return out, rows.Err()
}

// fingerprintServer reads the fingerprint of the server on port. secureDir
// is the secure_file_priv the backend sets on purpose: the container's is ""
// (it can only reach the container's own filesystem), the local backend's is
// its state root's sfp/ (it runs on the host; see writeServerConfig). That
// value is reported as "" so both backends compare against one fingerprint.
func fingerprintServer(t *testing.T, port, secureDir string) serverFingerprint {
	t.Helper()
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var fp serverFingerprint
	must := func(v []string, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	fp.Users = must(queryStrings(ctx, db, "SELECT CONCAT(user, '@', host) FROM mysql.user ORDER BY 1"))
	fp.TestGrants = must(queryStrings(ctx, db, "SHOW GRANTS FOR 'test'@'%'"))
	fp.Databases = must(queryStrings(ctx, db, "SHOW DATABASES"))
	sort.Strings(fp.Databases)
	row := db.QueryRowContext(ctx, "SELECT CURRENT_USER(), @@version, @@max_connections, @@sql_mode, "+
		"@@system_time_zone, @@time_zone, @@autocommit, @@secure_file_priv, "+
		"@@character_set_server, @@collation_server, @@max_allowed_packet, @@transaction_isolation, "+
		"@@lower_case_table_names, @@lower_case_file_system, @@wait_timeout, @@innodb_lock_wait_timeout")
	var secure sql.NullString
	if err := row.Scan(&fp.CurrentUser, &fp.Version, &fp.MaxConns, &fp.SQLMode,
		&fp.SystemTZ, &fp.TimeZone, &fp.Autocommit, &secure,
		&fp.CharsetSrv, &fp.CollationSrv, &fp.MaxPacket, &fp.TxIsolation,
		&fp.LowerCaseTbl, &fp.LowerCaseFS, &fp.WaitTimeout, &fp.LockWait); err != nil {
		t.Fatalf("server variables: %v", err)
	}
	fp.SecureFilePv = secure.String
	if secureDir != "" && filepath.Clean(fp.SecureFilePv) == filepath.Clean(secureDir) {
		fp.SecureFilePv = ""
	}
	// lower_case_file_system describes the filesystem under the data
	// directory: the container's is Linux, a local server on macOS sits on
	// case-insensitive APFS and reports ON. Only Linux hosts compare it.
	if runtime.GOOS != "linux" {
		fp.LowerCaseFS = wantServerFingerprint.LowerCaseFS
	}
	return fp
}

func checkFingerprint(t *testing.T, got serverFingerprint) {
	t.Helper()
	if g, w := fmt.Sprintf("%+q", got), fmt.Sprintf("%+q", wantServerFingerprint); g != w {
		t.Errorf("Dolt test server fingerprint changed\n got: %s\nwant: %s", g, w)
	}
}

// TestDoltServerFingerprint checks every backend available on this host
// against the same checked-in fingerprint, so container and local servers
// are compared with each other through it. The backend BEADS_TEST_DOLT_SERVER
// selects obeys the usual skip/fail rules; the other one is checked only when
// it happens to be available.
func TestDoltServerFingerprint(t *testing.T) {
	if hasTestSkip("dolt") {
		t.Skip("skipping: Dolt tests skipped (BEADS_TEST_SKIP=dolt)")
	}
	selectedLocal := useLocalDoltServer()

	t.Run("container", func(t *testing.T) {
		if !selectedLocal {
			if state := checkDolt(); state != doltReady {
				skipOrFailDoltUnavailable(t, state)
			}
		} else if !isDockerAvailable() || !isDoltImageCached() {
			t.Skipf("container backend not available on this host (docker + %s)", DoltDockerImage)
		}
		c := startIsolatedDoltContainer(t)
		checkFingerprint(t, fingerprintServer(t, c.Port, ""))
	})

	t.Run("local", func(t *testing.T) {
		skipLocalServerOffLinuxUnlessSelected(t)
		if selectedLocal {
			if state := checkDolt(); state != doltReady {
				skipOrFailDoltUnavailable(t, state)
			}
		} else if _, err := resolveLocalDoltBinary(); err != nil {
			t.Skipf("local backend not available on this host: %v", err)
		}
		c := startIsolatedLocalDoltServer(t)
		checkFingerprint(t, fingerprintServer(t, c.Port, c.local.secureFilePrivDir()))
	})
}

// skipLocalServerOffLinuxUnlessSelected keeps the local backend's own tests
// off non-Linux hosts unless BEADS_TEST_DOLT_SERVER=local asks for them. The
// macOS CI lanes run `go test -short ./...` with the pinned dolt installed;
// without Pdeathsig (Linux only) a test binary killed mid-test there would
// orphan its servers on the runner, and the backend is validated on Linux.
func skipLocalServerOffLinuxUnlessSelected(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && !useLocalDoltServer() {
		t.Skipf("skipping local dolt sql-server test on %s (no Pdeathsig); set %s=local to run it",
			runtime.GOOS, EnvDoltServerBackend)
	}
}

// requireLocalDoltCLI skips (or, under BEADS_TEST_REQUIRE_DOLT_CONTAINER=1,
// fails) a test of the local backend itself when the pinned dolt CLI is
// missing. These tests run whichever backend is selected.
func requireLocalDoltCLI(t *testing.T) {
	t.Helper()
	skipLocalServerOffLinuxUnlessSelected(t)
	if hasTestSkip("dolt") {
		t.Skip("skipping: Dolt tests skipped (BEADS_TEST_SKIP=dolt)")
	}
	if _, err := resolveLocalDoltBinary(); err != nil {
		if os.Getenv(EnvRequireDoltContainer) == "1" && useLocalDoltServer() {
			t.Fatalf("local Dolt CLI unavailable (%v) but %s=1", err, EnvRequireDoltContainer)
		}
		t.Skipf("local Dolt CLI unavailable: %v", err)
	}
}

func TestLocalDoltServer_BindRaceRetriesOnAnotherPort(t *testing.T) {
	requireLocalDoltCLI(t)
	// Hold a port the way a concurrent action would between FindFreePort and
	// dolt's own bind.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	taken := l.Addr().(*net.TCPAddr).Port

	s, err := newLocalDoltServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.terminate)
	if err := s.start(taken); err != nil {
		t.Fatalf("start with port %d taken: %v", taken, err)
	}
	if s.Port() == taken {
		t.Fatalf("server reports the taken port %d", taken)
	}
	if !strings.Contains(s.logSince(0), "already in use") {
		t.Errorf("first attempt did not hit the taken port; log: %s", s.logSince(0))
	}
	if err := pingDoltOnce(fmt.Sprintf("root@tcp(127.0.0.1:%d)/", s.Port())); err != nil {
		t.Errorf("ping after retry: %v", err)
	}
}

func TestLocalDoltServer_NeverDefaultPort(t *testing.T) {
	requireLocalDoltCLI(t)
	s, err := newLocalDoltServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.terminate)
	if err := s.start(3307); err != nil {
		t.Fatal(err)
	}
	if s.Port() == 3307 {
		t.Fatal("local test server listens on 3307, the production default port")
	}
}

func TestLocalDoltServer_Lifecycle(t *testing.T) {
	requireLocalDoltCLI(t)
	c := startIsolatedLocalDoltServer(t)
	s := c.local
	ctx := context.Background()

	t.Run("ExecRunsInDataDir", func(t *testing.T) {
		code, out, err := c.Exec(ctx, []string{"pwd", "-P"})
		if err != nil || code != 0 {
			t.Fatalf("exec pwd: code=%d err=%v out=%q", code, err, out)
		}
		want, _ := filepath.EvalSymlinks(s.dataDir)
		if got := strings.TrimSpace(out); got != want {
			t.Errorf("Exec working directory = %q, want the data dir %q", got, want)
		}
		code, _, err = c.Exec(ctx, []string{"test", "-d", "beads_test"})
		if err != nil || code != 0 {
			t.Errorf("provisioned database beads_test not visible from Exec's cwd: code=%d err=%v", code, err)
		}
		code, _, err = c.Exec(ctx, []string{"false"})
		if err != nil || code != 1 {
			t.Errorf("Exec exit status: code=%d err=%v, want 1, nil", code, err)
		}
	})

	t.Run("StopStartKeepsData", func(t *testing.T) {
		dsn := func(port string) string { return fmt.Sprintf("root@tcp(127.0.0.1:%s)/beads_test", port) }
		db, err := sql.Open("mysql", dsn(c.Port))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "CREATE TABLE kept (id INT PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO kept VALUES (42)"); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()

		if err := c.Stop(ctx); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if gone, _ := s.exited(); !gone {
			t.Fatal("server still running after Stop")
		}
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		port, err := c.CurrentPort(ctx)
		if err != nil {
			t.Fatal(err)
		}
		db, err = sql.Open("mysql", dsn(port))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var id int
		if err := db.QueryRowContext(ctx, "SELECT id FROM kept").Scan(&id); err != nil || id != 42 {
			t.Fatalf("data after restart: id=%d err=%v", id, err)
		}
	})

	t.Run("CrashIsDetected", func(t *testing.T) {
		s.mu.Lock()
		pid := s.cmd.Process.Pid
		s.mu.Unlock()
		if gone, _ := s.exited(); gone {
			t.Fatal("server reported exited before the kill")
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if gone, _ := s.exited(); gone {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("killed server not reported as exited")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func TestLocalDoltServer_SingletonCrashDetection(t *testing.T) {
	requireLocalDoltCLI(t)
	s, err := startLocalDoltServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.terminate()
	// Exercise the exported crash API on a singleton that is this server,
	// without disturbing whatever singleton the package may hold.
	doltServerMu.Lock()
	saved := doltSingletonSrv
	doltSingletonSrv = &doltServer{local: s}
	doltServerMu.Unlock()
	t.Cleanup(func() {
		doltServerMu.Lock()
		doltSingletonSrv = saved
		doltServerMu.Unlock()
	})

	if DoltContainerCrashed() || DoltContainerCrashError() != nil {
		t.Fatal("running server reported as crashed")
	}
	s.kill()
	if !DoltContainerCrashed() {
		t.Error("DoltContainerCrashed() = false after the server died")
	}
	if err := DoltContainerCrashError(); err == nil {
		t.Error("DoltContainerCrashError() = nil after the server died")
	}
	if !ServerUnreachable(errors.New("anything")) {
		t.Error("ServerUnreachable ignores a dead local singleton")
	}
}

// The suites' leak sweeps (doltserver.SweepSuiteTestServers) report and kill
// dolt servers whose working directory is under the suite root. Test servers
// must live outside it even when started after PinSuiteTempRoot re-pointed
// TMPDIR.
func TestLocalDoltServer_StateOutsideSuiteRoot(t *testing.T) {
	requireLocalDoltCLI(t)
	for _, k := range []string{"TMPDIR", "GOTMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, os.Getenv(k)) // restored after the test
	}
	root, err := PinSuiteTempRoot("bdt-suite-root-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if !PathUnderSuiteRoot(filepath.Join(os.TempDir(), "x"), root) {
		t.Fatalf("PinSuiteTempRoot did not re-point os.TempDir() under %s", root)
	}

	s, err := startLocalDoltServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.terminate()
	for _, p := range []string{s.root, s.dataDir} {
		if PathUnderSuiteRoot(p, root) {
			t.Errorf("local server state %s is under the suite root %s", p, root)
		}
	}
}

func TestLocalDoltServer_TerminateLeavesNothing(t *testing.T) {
	requireLocalDoltCLI(t)
	s, err := startLocalDoltServer()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	pid := s.cmd.Process.Pid
	s.mu.Unlock()
	s.terminate()
	if processAlive(pid) {
		t.Errorf("dolt sql-server pid %d still alive after terminate", pid)
	}
	if _, err := os.Stat(s.root); !os.IsNotExist(err) {
		t.Errorf("state dir %s not removed: %v", s.root, err)
	}
	if err := pingDoltOnce(fmt.Sprintf("root@tcp(127.0.0.1:%d)/", s.Port())); err == nil {
		t.Errorf("something still answers on port %d", s.Port())
	}
	// A detached `dolt send-metrics` child of the exiting server would
	// re-create <root>/home/.dolt a few hundred ms later.
	time.Sleep(time.Second)
	if _, err := os.Stat(s.root); !os.IsNotExist(err) {
		t.Errorf("state dir %s re-created after terminate (dolt child outlived the server?): %v", s.root, err)
	}
}

// The version probe's throwaway HOME must be gone for good once it returns.
func TestLocalDoltServer_VersionProbeLeavesNothing(t *testing.T) {
	requireLocalDoltCLI(t)
	bin, _ := resolveLocalDoltBinary()
	base := t.TempDir()
	if _, err := probeDoltVersion(bin, base); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if ents, _ := os.ReadDir(base); len(ents) != 0 {
		t.Errorf("dolt version left %d entries in %s (dolt child outlived it?): %v", len(ents), base, ents)
	}
}

// Only files under the server's own sfp/ directory are reachable through
// LOAD_FILE and SELECT ... INTO OUTFILE: a local server runs as the test user
// on the host, with a passwordless root@%.
func TestLocalDoltServer_SecureFilePriv(t *testing.T) {
	requireLocalDoltCLI(t)
	c := startIsolatedLocalDoltServer(t)
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", c.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT LOAD_FILE(?)", outside).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Errorf("LOAD_FILE(%s) outside secure_file_priv = %q, want NULL", outside, got.String)
	}
	written := filepath.Join(filepath.Dir(outside), "written")
	if _, err := db.ExecContext(ctx, "SELECT 1 INTO OUTFILE '"+written+"'"); err == nil {
		t.Errorf("SELECT ... INTO OUTFILE %s outside secure_file_priv succeeded", written)
	}
	if _, err := os.Stat(written); !os.IsNotExist(err) {
		t.Errorf("INTO OUTFILE created %s: %v", written, err)
	}

	inside := filepath.Join(c.local.secureFilePrivDir(), "ok")
	if err := os.WriteFile(inside, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT LOAD_FILE(?)", inside).Scan(&got); err != nil || got.String != "ok" {
		t.Errorf("LOAD_FILE inside secure_file_priv = %q, %v; want \"ok\"", got.String, err)
	}
}

func TestSweepDeadLocalServerRoots(t *testing.T) {
	base := t.TempDir()
	const livePID, deadPID = 1001, 1002
	mk := func(name, marker string) string {
		t.Helper()
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(p, "data", "beads_test"), 0o700); err != nil {
			t.Fatal(err)
		}
		if marker != "" {
			if err := os.WriteFile(filepath.Join(p, localRootOwnerFile), []byte(marker), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	deadServer := mk("bdt-dolt-1", strconv.Itoa(deadPID)+"\n")
	deadVersion := mk("bdt-doltver-1", strconv.Itoa(deadPID))
	live := mk("bdt-dolt-2", strconv.Itoa(livePID))
	noMarker := mk("bdt-dolt-3", "")
	garbage := mk("bdt-dolt-4", "not a pid")
	foreign := mk("other-dolt-1", strconv.Itoa(deadPID))
	target := mk("target", strconv.Itoa(deadPID))
	link := filepath.Join(base, "bdt-dolt-5")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	alive := func(pid int) bool { return pid == livePID }
	swept := sweepDeadLocalServerRoots(base, alive)
	sort.Strings(swept)
	want := []string{deadServer, deadVersion}
	if fmt.Sprint(swept) != fmt.Sprint(want) {
		t.Errorf("swept %v, want %v", swept, want)
	}
	for _, p := range want {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed: %v", p, err)
		}
	}
	for _, p := range []string{live, noMarker, garbage, foreign, target, link} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s must be left alone: %v", p, err)
		}
	}
	if got := sweepDeadLocalServerRoots(base, alive); len(got) != 0 {
		t.Errorf("second sweep removed %v", got)
	}
}

func TestLocalRootOwnerMarker(t *testing.T) {
	root := t.TempDir()
	if _, ok := readLocalRootOwner(root); ok {
		t.Fatal("marker reported in an empty root")
	}
	if err := writeLocalRootOwner(root); err != nil {
		t.Fatal(err)
	}
	if pid, ok := readLocalRootOwner(root); !ok || pid != os.Getpid() {
		t.Errorf("readLocalRootOwner = %d, %v; want %d, true", pid, ok, os.Getpid())
	}
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false")
	}
}

// The server's own dolt global config turns off the release check and usage
// events: a hermetic test action must not depend on (or phone) the network.
func TestLocalDoltServer_NoNetworkCallsConfigured(t *testing.T) {
	requireLocalDoltCLI(t)
	s, err := newLocalDoltServer()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(s.root)
	b, err := os.ReadFile(filepath.Join(s.root, "home", ".dolt", "config_global.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"metrics.disabled":"true"`, `"versioncheck.disabled":"true"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("server dolt global config lacks %s: %s", k, b)
		}
	}
}

func TestDoltBackendSelection(t *testing.T) {
	for _, tc := range []struct {
		val, srcdir string
		local       bool
		bad         bool
	}{
		{"", "", false, false},
		{"", "/runfiles", false, false}, // bazel: local only when a target or --test_env asks
		{"local", "/runfiles", true, false},
		{"container", "/runfiles", false, false},
		{"local", "", true, false},
		{"docker", "", false, true},
	} {
		t.Run(tc.val+"/"+strconv.Quote(tc.srcdir), func(t *testing.T) {
			t.Setenv(EnvDoltServerBackend, tc.val)
			t.Setenv("TEST_SRCDIR", tc.srcdir)
			if got := useLocalDoltServer(); got != tc.local {
				t.Errorf("useLocalDoltServer() = %v, want %v", got, tc.local)
			}
			if got := doltBackendErr() != nil; got != tc.bad {
				t.Errorf("doltBackendErr() != nil = %v, want %v", got, tc.bad)
			}
		})
	}
}
