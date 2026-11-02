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
// listener, and the Config an auto-starting CLI open of it uses.
func serverBinaryOpen(t *testing.T, events *serverBinaryEvents) *Config {
	t.Helper()
	externalPortAdviceEnv(t)
	t.Setenv("BEADS_TEST_MODE", "1")
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
