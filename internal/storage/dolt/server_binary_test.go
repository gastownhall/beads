package dolt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/migration"
)

// serverBinaryEvents records, in order, each connection the fake server
// accepts ("conn") and each lifecycle call the stubs see.
type serverBinaryEvents struct {
	mu  sync.Mutex
	log []string
}

func (e *serverBinaryEvents) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *serverBinaryEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

// serverBinaryListener stands in for the tracked server: it records each
// connection before greeting it (so the open's probe, which drains the
// greeting, is recorded before anything the open does next) and then closes
// it, which fails any MySQL session opened on it.
func serverBinaryListener(t *testing.T, events *serverBinaryEvents) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			events.add("conn")
			_, _ = c.Write([]byte("\x0a5.7.9-test\x00"))
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// serverBinaryOpen prepares an owned workspace whose tracked server is the
// listener, and the Config an auto-starting CLI open of it uses. Test mode is
// off, as it forbids the restart these opens exercise; the circuit breaker
// it would bypass keeps its state in a directory of the test's own.
func serverBinaryOpen(t *testing.T, events *serverBinaryEvents) *Config {
	t.Helper()
	externalPortAdviceEnv(t)
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv(testCircuitBreakerDirEnv, t.TempDir())
	t.Setenv(migration.EnvFreezeFile, "")
	beadsDir := externalPortAdviceWorkspace(t, 0)
	if mode := doltserver.ResolveServerMode(beadsDir); mode != doltserver.ServerModeOwned {
		t.Fatalf("workspace server mode = %v, want owned", mode)
	}
	return &Config{
		Path:       filepath.Join(beadsDir, "dolt"),
		BeadsDir:   beadsDir,
		ServerHost: "127.0.0.1",
		ServerPort: serverBinaryListener(t, events),
		ServerUser: "root",
		Database:   "advice",
		AutoStart:  true,
	}
}

// stubServerBinary replaces the lifecycle calls reconcileServerBinary makes.
func stubServerBinary(t *testing.T, check func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck,
	replace func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error)) {
	t.Helper()
	origCheck, origReplace := checkServerBinary, replaceStaleServer
	checkServerBinary, replaceStaleServer = check, replace
	t.Cleanup(func() { checkServerBinary, replaceStaleServer = origCheck, origReplace })
}

func staleServerCheck(port int) doltserver.BinaryCheck {
	return doltserver.BinaryCheck{
		Verdict: doltserver.BinaryStale, PID: 111, Port: port,
		RunningVersion: "2.6.0", CandidatePath: "/opt/dolt/bin/dolt", CandidateVersion: "2.7.0",
	}
}

// captureServerBinaryStderr runs f with os.Stderr redirected and returns what
// it wrote.
func captureServerBinaryStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	defer func() {
		os.Stderr = orig
		_ = w.Close()
		<-done
	}()
	f()
	os.Stderr = orig
	_ = w.Close()
	<-done
	return buf.String()
}

// TestServerBinaryStaleServerReplacedBeforeFirstQuery: an auto-starting open
// of an owned workspace whose tracked server runs a dolt that has since been
// replaced replaces it after the reachability probe and before its first
// MySQL session, on the same port, and says so once on stderr, naming both
// releases (GH#6064).
func TestServerBinaryStaleServerReplacedBeforeFirstQuery(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	port := cfg.ServerPort
	stubServerBinary(t,
		func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return staleServerCheck(port) },
		func(_ string, p int, _ doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{
				Replaced: true, Check: staleServerCheck(p),
				State: &doltserver.State{Running: true, PID: 222, Port: p},
			}, nil
		})

	stderr := captureServerBinaryStderr(t, func() {
		if _, err := newServerMode(context.Background(), cfg); err == nil {
			t.Error("open of the fake server succeeded; it speaks no MySQL")
		}
	})

	got := events.snapshot()
	if len(got) < 3 || got[0] != "conn" || got[1] != "replace" || got[2] != "conn" {
		t.Fatalf("events = %v, want the probe, then the replacement, then the first session", got)
	}
	if n := strings.Count(strings.Join(got, " "), "replace"); n != 1 {
		t.Errorf("replaced %d times, want once", n)
	}
	if n := strings.Count(stderr, "Restarted the dolt sql-server"); n != 1 {
		t.Fatalf("stderr has %d restart notices, want one:\n%s", n, stderr)
	}
	for _, want := range []string{"port " + strconv.Itoa(port), "PID 111 → 222", "dolt 2.6.0", "/opt/dolt/bin/dolt", "now 2.7.0"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("notice does not mention %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "endpoint changed") {
		t.Errorf("same-port replacement reported an endpoint change:\n%s", stderr)
	}
}

// TestServerBinaryFailedReplacementSendsNoQuery: when replacing the stale
// server fails, the open fails before any MySQL session, saying so and
// naming the manual restart.
func TestServerBinaryFailedReplacementSendsNoQuery(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	port := cfg.ServerPort
	stubServerBinary(t,
		func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return staleServerCheck(port) },
		func(_ string, p int, _ doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{Check: staleServerCheck(p)}, errors.New("stop failed")
		})

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("open succeeded after a failed replacement")
	}
	for _, want := range []string{"dolt 2.6.0", "now 2.7.0", "no query was sent", "stop failed", "bd dolt stop, then bd dolt start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if got := events.snapshot(); len(got) != 2 || got[0] != "conn" || got[1] != "replace" {
		t.Errorf("events = %v, want only the probe and the replacement attempt", got)
	}
}

// TestServerBinaryCurrentServerKept: an open of a current server neither
// replaces it nor prints a notice; one whose record needs a refresh (the
// same release was reinstalled) asks the lifecycle to record it, which
// keeps the server, and prints nothing either.
func TestServerBinaryCurrentServerKept(t *testing.T) {
	for _, tc := range []struct {
		name        string
		refresh     bool
		wantReplace int
	}{
		{name: "unchanged executable"},
		{name: "same release reinstalled", refresh: true, wantReplace: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := &serverBinaryEvents{}
			cfg := serverBinaryOpen(t, events)
			port := cfg.ServerPort
			current := doltserver.BinaryCheck{
				Verdict: doltserver.BinaryCurrent, PID: 111, Port: port, Refresh: tc.refresh,
				RunningVersion: "2.6.0", CandidatePath: "/opt/dolt/bin/dolt", CandidateVersion: "2.6.0",
			}
			stubServerBinary(t,
				func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return current },
				func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
					events.add("replace")
					return doltserver.BinaryReplacement{Check: current}, nil
				})

			stderr := captureServerBinaryStderr(t, func() {
				_, _ = newServerMode(context.Background(), cfg)
			})
			if n := strings.Count(strings.Join(events.snapshot(), " "), "replace"); n != tc.wantReplace {
				t.Errorf("replaceStaleServer called %d times, want %d", n, tc.wantReplace)
			}
			if strings.Contains(stderr, "Restarted the dolt sql-server") || strings.Contains(stderr, "endpoint changed") {
				t.Errorf("current server reported a restart:\n%s", stderr)
			}
		})
	}
}

// TestServerBinaryAsksTheServerItsVersion: the open hands the lifecycle a way
// to ask the server it dials for its version (how a server with no launch
// record is compared), and that question fails, bounded, rather than hanging
// on a server that does not answer it.
func TestServerBinaryAsksTheServerItsVersion(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	var asked error
	called := false
	stubServerBinary(t,
		func(_ string, _ int, serverVersion doltserver.ServerVersionFunc) doltserver.BinaryCheck {
			called = true
			if serverVersion == nil {
				asked = errors.New("no ServerVersionFunc")
				return doltserver.BinaryCheck{Verdict: doltserver.BinaryUnknown}
			}
			start := time.Now()
			v, err := serverVersion()
			if err == nil {
				asked = errors.New("the fake server answered " + v)
			} else if d := time.Since(start); d > serverVersionTimeout+time.Second {
				asked = errors.New("question took " + d.String())
			}
			return doltserver.BinaryCheck{Verdict: doltserver.BinaryUnknown}
		},
		func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			t.Error("unknown verdict led to a replacement")
			return doltserver.BinaryReplacement{}, nil
		})

	_, _ = newServerMode(context.Background(), cfg)
	if !called {
		t.Fatal("checkServerBinary not called")
	}
	if asked != nil {
		t.Error(asked)
	}
}

// TestServerBinaryFailedReplacementReportsState: an open whose replacement
// fails sends no query and says what state it left: a refused preflight left
// the server running; a failed stop names the manual restart; a failed start
// says none runs and the port is kept for `bd dolt start`.
func TestServerBinaryFailedReplacementReportsState(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cannot      bool
		stopped     bool
		wantReplace int
		want        []string
		banned      []string
	}{
		{name: "preflight", cannot: true, want: []string{"cannot be replaced", "left running", "bd dolt stop"}, banned: []string{"bd dolt start"}},
		{name: "stop", wantReplace: 1, want: []string{"replacing the server failed", "bd dolt stop, then bd dolt start"}},
		{name: "start", stopped: true, wantReplace: 1, want: []string{"replacing the server failed", "none is running", "is kept for the next start: bd dolt start"}, banned: []string{"bd dolt stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := &serverBinaryEvents{}
			cfg := serverBinaryOpen(t, events)
			port := cfg.ServerPort
			check := staleServerCheck(port)
			if tc.cannot {
				check.Verdict = doltserver.BinaryCannotReplace
				check.Reason = errors.New("dolt 2.5.1 is below the minimum")
			}
			stubServerBinary(t,
				func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return check },
				func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
					events.add("replace")
					return doltserver.BinaryReplacement{Check: check, Stopped: tc.stopped}, errors.New("replacement step failed")
				})

			_, err := newServerMode(context.Background(), cfg)
			if err == nil {
				t.Fatal("open succeeded after a failed replacement")
			}
			msg := err.Error()
			for _, want := range append([]string{"no query was sent", "2.6.0"}, tc.want...) {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not mention %q:\n%s", want, msg)
				}
			}
			if tc.stopped && !strings.Contains(msg, "SQL port "+strconv.Itoa(port)) {
				t.Errorf("error does not name the kept port %d:\n%s", port, msg)
			}
			for _, b := range tc.banned {
				if strings.Contains(msg, b) {
					t.Errorf("error mentions %q:\n%s", b, msg)
				}
			}
			got := events.snapshot()
			if n := strings.Count(strings.Join(got, " "), "replace"); n != tc.wantReplace || strings.Count(strings.Join(got, " "), "conn") != 1 {
				t.Errorf("events = %v, want the probe and %d replacement attempts, no session", got, tc.wantReplace)
			}
		})
	}
}

// TestServerBinaryNoPermissionAdvises: an open that may not restart the
// tracked server (auto-start off, a frozen command, a MIGRATION-FREEZE above
// the workspace, a preview, strict --readonly, test mode, a server declared
// external, a shared server) changes nothing: it does not ask the lifecycle
// to replace the server, writes nothing in the workspace, opens its session
// as before, and warns once, naming both releases and the manual restart.
// The shared-server warning names the clients a restart disconnects.
func TestServerBinaryNoPermissionAdvises(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, cfg *Config)
		shared bool
	}{
		{name: "auto-start disabled", setup: func(t *testing.T, _ *Config) { t.Setenv("BEADS_DOLT_AUTO_START", "0") }},
		{name: "frozen command", setup: func(_ *testing.T, cfg *Config) { cfg.DisableAutoRestart = true }},
		{name: "ancestor freeze", setup: func(t *testing.T, cfg *Config) {
			// A target-scoped open (the doctor's) builds its own Config: only the
			// marker above the workspace says it is frozen.
			marker := filepath.Join(filepath.Dir(cfg.BeadsDir), migration.FileName)
			if err := os.WriteFile(marker, []byte("operator: test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "preview", setup: func(_ *testing.T, cfg *Config) { cfg.Preview = true }},
		{name: "strict readonly", setup: func(_ *testing.T, cfg *Config) { cfg.ReadOnly = true }},
		{name: "test mode", setup: func(t *testing.T, _ *Config) { t.Setenv("BEADS_TEST_MODE", "1") }},
		{name: "declared external", setup: func(t *testing.T, cfg *Config) {
			// The open still auto-starts (its Config says so); the environment
			// hands the server's lifecycle to an operator.
			t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
			if !cfg.AutoStart {
				t.Fatal("open does not auto-start")
			}
		}},
		{name: "shared server", setup: func(t *testing.T, _ *Config) { t.Setenv("BEADS_DOLT_SHARED_SERVER", "1") }, shared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := &serverBinaryEvents{}
			cfg := serverBinaryOpen(t, events)
			tc.setup(t, cfg)
			port := cfg.ServerPort
			stubServerBinary(t,
				func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return staleServerCheck(port) },
				func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
					events.add("replace")
					return doltserver.BinaryReplacement{}, errors.New("must not be called")
				})

			before := serverBinaryWorkspaceFiles(t, cfg.BeadsDir)
			stderr := captureServerBinaryStderr(t, func() {
				for range 2 {
					_, _ = newServerMode(context.Background(), cfg)
				}
			})
			if after := serverBinaryWorkspaceFiles(t, cfg.BeadsDir); after != before {
				t.Errorf("workspace changed by an open without permission:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			got := strings.Join(events.snapshot(), " ")
			if strings.Contains(got, "replace") {
				t.Errorf("events = %s, want no replacement", got)
			}
			if n := strings.Count(got, "conn"); n < 4 {
				t.Errorf("events = %s, want each open's probe and session as before", got)
			}
			if n := strings.Count(stderr, "this command does not restart it"); n != 1 {
				t.Fatalf("stderr has %d warnings, want one:\n%s", n, stderr)
			}
			for _, want := range []string{"port " + strconv.Itoa(port), "dolt 2.6.0", "/opt/dolt/bin/dolt", "is now 2.7.0", "bd dolt stop, then bd dolt start"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("warning does not mention %q:\n%s", want, stderr)
				}
			}
			if strings.Contains(stderr, "every client") != tc.shared {
				t.Errorf("warning names the shared server's clients = %v, want %v:\n%s", !tc.shared, tc.shared, stderr)
			}
			if strings.Contains(stderr, "Restarted the dolt sql-server") {
				t.Errorf("open without permission reported a restart:\n%s", stderr)
			}
		})
	}
}

// serverBinaryWorkspaceFiles lists every file under beadsDir with its
// contents, so a test can tell nothing (lock, state, record) was written.
func serverBinaryWorkspaceFiles(t *testing.T, beadsDir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(beadsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp dir
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(beadsDir, path)
		b.WriteString(rel + ": " + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestServerBinaryProtectedOpenCannotReplaceAdvisesReason: a protected open
// of a server whose executable changed into one bd cannot launch keeps using
// the server and warns once with the reason and the repair to make before a
// restart; the open does not fail.
func TestServerBinaryProtectedOpenCannotReplaceAdvisesReason(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	cfg.Preview = true
	port := cfg.ServerPort
	check := staleServerCheck(port)
	check.Verdict, check.CandidateVersion = doltserver.BinaryCannotReplace, ""
	check.Reason = errors.New("dolt executable /opt/dolt/bin/dolt does not run (`dolt version` failed)")
	stubServerBinary(t,
		func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return check },
		func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{}, errors.New("must not be called")
		})

	var err error
	stderr := captureServerBinaryStderr(t, func() {
		_, err = newServerMode(context.Background(), cfg)
	})
	if err != nil && strings.Contains(err.Error(), "cannot be replaced") {
		t.Fatalf("protected open failed on the cannot-replace verdict: %v", err)
	}
	if got := strings.Join(events.snapshot(), " "); strings.Contains(got, "replace") {
		t.Errorf("events = %s, want no replacement", got)
	}
	if n := strings.Count(stderr, "this command does not restart it"); n != 1 {
		t.Fatalf("stderr has %d warnings, want one:\n%s", n, stderr)
	}
	for _, want := range []string{"dolt 2.6.0", "has changed", "does not run (`dolt version` failed)", "Fix the dolt installation first", "bd dolt stop, then bd dolt start"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning does not mention %q:\n%s", want, stderr)
		}
	}
}

// TestServerBinaryClassifiedReadReplaces: a read the CLI classified as such
// (a --readonly open that is not strict, e.g. `bd list`) may restart the
// server as auto-start may, so the stale server is replaced.
func TestServerBinaryClassifiedReadReplaces(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	cfg.ReadOnly, cfg.ClassifiedRead = true, true
	port := cfg.ServerPort
	stubServerBinary(t,
		func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return staleServerCheck(port) },
		func(_ string, p int, _ doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{
				Replaced: true, Check: staleServerCheck(p),
				State: &doltserver.State{Running: true, PID: 222, Port: p},
			}, nil
		})

	stderr := captureServerBinaryStderr(t, func() {
		_, _ = newServerMode(context.Background(), cfg)
	})
	if n := strings.Count(strings.Join(events.snapshot(), " "), "replace"); n != 1 {
		t.Errorf("replaced %d times, want once", n)
	}
	if !strings.Contains(stderr, "Restarted the dolt sql-server") || strings.Contains(stderr, "does not restart it") {
		t.Errorf("stderr = %q, want the restart notice and no warning", stderr)
	}
}

// TestServerBinaryProtectedRefreshSilent: a protected open of a current
// server whose record needs a refresh (the same release reinstalled) neither
// asks the lifecycle to record it nor warns: nothing is stale.
func TestServerBinaryProtectedRefreshSilent(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	cfg.Preview = true
	port := cfg.ServerPort
	current := doltserver.BinaryCheck{
		Verdict: doltserver.BinaryCurrent, PID: 111, Port: port, Refresh: true,
		RunningVersion: "2.6.0", CandidatePath: "/opt/dolt/bin/dolt", CandidateVersion: "2.6.0",
	}
	stubServerBinary(t,
		func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return current },
		func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{Check: current}, nil
		})

	before := serverBinaryWorkspaceFiles(t, cfg.BeadsDir)
	stderr := captureServerBinaryStderr(t, func() {
		_, _ = newServerMode(context.Background(), cfg)
	})
	if got := strings.Join(events.snapshot(), " "); strings.Contains(got, "replace") {
		t.Errorf("events = %s, want no lifecycle call for a protected refresh", got)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
	if after := serverBinaryWorkspaceFiles(t, cfg.BeadsDir); after != before {
		t.Errorf("workspace changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestServerBinaryNoLongerOwnedUnderLockAdvises: when the lifecycle finds the
// server still stale under its lock but no longer bd's to restart (it leaves
// it running and reports no replacement and no error), the open goes on to
// use the server and warns as an open without permission does.
func TestServerBinaryNoLongerOwnedUnderLockAdvises(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	port := cfg.ServerPort
	stubServerBinary(t,
		func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck { return staleServerCheck(port) },
		func(_ string, p int, _ doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{Check: staleServerCheck(p)}, nil
		})

	var err error
	stderr := captureServerBinaryStderr(t, func() {
		_, err = newServerMode(context.Background(), cfg)
	})
	if err != nil && strings.Contains(err.Error(), "no query was sent") {
		t.Fatalf("open failed as a failed replacement: %v", err)
	}
	got := events.snapshot()
	if len(got) < 3 || got[0] != "conn" || got[1] != "replace" || got[2] != "conn" ||
		strings.Count(strings.Join(got, " "), "replace") != 1 {
		t.Errorf("events = %v, want the probe, one replacement attempt, then the session as before", got)
	}
	if n := strings.Count(stderr, "this command does not restart it"); n != 1 {
		t.Fatalf("stderr has %d warnings, want one:\n%s", n, stderr)
	}
	for _, want := range []string{"port " + strconv.Itoa(port), "dolt 2.6.0", "is now 2.7.0"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning does not mention %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "Restarted the dolt sql-server") {
		t.Errorf("unreplaced server reported a restart:\n%s", stderr)
	}
}

// TestServerBinaryNotComparedForOtherEndpoints: an endpoint that is not a
// server bd launches (a remote host, the IPv6 loopback bd never runs its
// server at, no resolved port, a unix socket, TLS, a gateway, a proxied
// server) is never compared: no lifecycle call, no output, the port as
// configured. The local TCP endpoint bd launches is compared.
func TestServerBinaryNotComparedForOtherEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       Config
		noPort    bool
		wantCheck bool
	}{
		{name: "remote host", cfg: Config{ServerHost: "db.example.com"}},
		{name: "ipv6 loopback", cfg: Config{ServerHost: "::1"}},
		{name: "no port", noPort: true},
		{name: "unix socket", cfg: Config{ServerSocket: "/tmp/dolt.sock"}},
		{name: "tls", cfg: Config{ServerTLS: true}},
		{name: "gateway", cfg: Config{Gateway: true}},
		{name: "proxied", cfg: Config{ProxiedServer: true}},
		{name: "local tcp (compared)", wantCheck: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := &serverBinaryEvents{}
			base := serverBinaryOpen(t, events)
			cfg := tc.cfg
			cfg.Path, cfg.BeadsDir, cfg.ServerPort, cfg.AutoStart = base.Path, base.BeadsDir, base.ServerPort, true
			if tc.noPort {
				cfg.ServerPort = 0
			}
			if cfg.ServerHost == "" {
				cfg.ServerHost = "127.0.0.1"
			}
			stubServerBinary(t,
				func(string, int, doltserver.ServerVersionFunc) doltserver.BinaryCheck {
					events.add("check")
					return doltserver.BinaryCheck{Verdict: doltserver.BinaryCurrent, Port: cfg.ServerPort}
				},
				func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
					events.add("replace")
					return doltserver.BinaryReplacement{}, errors.New("must not be called")
				})

			var port int
			var err error
			stderr := captureServerBinaryStderr(t, func() {
				port, err = reconcileServerBinary(&cfg, cfg.BeadsDir, doltserver.ResolveServerDir(cfg.BeadsDir))
			})
			if err != nil || port != cfg.ServerPort {
				t.Errorf("reconcileServerBinary = %d, %v; want %d, nil", port, err, cfg.ServerPort)
			}
			want := []string(nil)
			if tc.wantCheck {
				want = []string{"check"}
			}
			if got := events.snapshot(); strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("events = %v, want %v", got, want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
		})
	}
}

// TestServerBinarySocketFallbackComparedAtResolvedEndpoint: a configured
// unix socket that is absent while the tracked server answers on TCP falls
// back to TCP, and the open compares the server at that resolved endpoint, as
// it would one configured for TCP.
func TestServerBinarySocketFallbackComparedAtResolvedEndpoint(t *testing.T) {
	events := &serverBinaryEvents{}
	cfg := serverBinaryOpen(t, events)
	cfg.ServerSocket = filepath.Join(t.TempDir(), "absent.sock")
	port := cfg.ServerPort
	var checkedPort int
	stubServerBinary(t,
		func(_ string, p int, _ doltserver.ServerVersionFunc) doltserver.BinaryCheck {
			events.add("check")
			checkedPort = p
			return doltserver.BinaryCheck{Verdict: doltserver.BinaryCurrent, Port: p}
		},
		func(string, int, doltserver.ServerVersionFunc) (doltserver.BinaryReplacement, error) {
			events.add("replace")
			return doltserver.BinaryReplacement{}, errors.New("must not be called")
		})

	_, _ = newServerMode(context.Background(), cfg)
	if cfg.ServerSocket != "" {
		t.Fatalf("socket %q kept; want the fallback to TCP", cfg.ServerSocket)
	}
	if got := strings.Join(events.snapshot(), " "); strings.Count(got, "check") != 1 || checkedPort != port {
		t.Errorf("events = %s, checked port %d; want one check of the tracked server on port %d", got, checkedPort, port)
	}
}
