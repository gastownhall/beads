//go:build cgo && unix

package main

// Helpers for the MANAGED-LOCAL proxied-server lane: bd launches its own
// loopback proxy (a detached `bd db-proxy-child` process) and that proxy
// launches a locally installed `dolt sql-server`. No external host/port and
// no testcontainer are involved, which is the production local-first
// topology that the external-harness shards (BEADS_TEST_PROXIED_SERVER)
// deliberately do not cover.
//
// The lane is gated by its own environment variable so CI can run the two
// proxied lanes independently. These helpers are written to be reusable by
// lifecycle/failure-injection tests (stale artifacts, process identity,
// port safety) beyond the initial smoke test.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

const managedLocalProxiedEnvVar = "BEADS_TEST_PROXIED_LOCAL"

// requireManagedLocalProxiedEnv gates the managed-local proxied lane.
// Unlike requireProxiedServerEnv, a missing dolt binary FAILS the test when
// the lane is explicitly requested: this lane exists to prove that bd can
// launch and supervise a local Dolt child, so skipping on a broken
// prerequisite would report a green run without testing anything.
func requireManagedLocalProxiedEnv(t *testing.T) {
	t.Helper()
	if os.Getenv(managedLocalProxiedEnvVar) != "1" {
		t.Skipf("set %s=1 to run managed-local proxied lifecycle tests", managedLocalProxiedEnvVar)
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Fatalf("%s=1 but dolt is not in PATH; this lane must fail rather than skip: %v",
			managedLocalProxiedEnvVar, err)
	}
}

// bdManagedLocalInit initializes a disposable managed-local proxied project:
// `bd init --proxied-server` with NO external host/port flags, so command
// dispatch launches the loopback proxy and the proxy launches dolt.
// idleTimeout is passed through --proxied-server-idle-timeout so tests can
// observe idle shutdown without waiting out the 30s production default.
func bdManagedLocalInit(t *testing.T, bd, prefix string, idleTimeout time.Duration, extraInitArgs ...string) proxiedProject {
	t.Helper()
	args := append([]string{"--proxied-server-idle-timeout", idleTimeout.String()}, extraInitArgs...)
	p := bdProxiedInit(t, bd, prefix, args...)

	info, err := configfile.LoadProxiedServerClientInfo(p.beadsDir)
	if err != nil {
		t.Fatalf("LoadProxiedServerClientInfo(%s): %v", p.beadsDir, err)
	}
	if info == nil {
		t.Fatalf("missing %s in %s after managed-local init", configfile.ProxiedServerClientInfoFileName, p.beadsDir)
	}
	if info.External != nil {
		t.Fatalf("expected managed-local topology (no External block), got %+v", info.External)
	}
	if info.IdleTimeout != idleTimeout {
		t.Fatalf("persisted managed-local idle timeout: got %s, want %s", info.IdleTimeout, idleTimeout)
	}
	return p
}

// readManagedProxyPidFile returns the loopback proxy's pidfile (proxy.pid:
// the detached `bd db-proxy-child` process and the port the proxy listens
// on), or nil when it does not exist.
func readManagedProxyPidFile(t *testing.T, p proxiedProject) *pidfile.PidFile {
	t.Helper()
	pf, err := pidfile.Read(p.proxyRoot, proxy.PIDFileName)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s in %s: %v", proxy.PIDFileName, p.proxyRoot, err)
	}
	return pf
}

// readManagedBackendPidFile returns the supervised Dolt backend's pidfile
// (proxy-child.pid: the `dolt sql-server` process and its listener port),
// or nil when it does not exist.
func readManagedBackendPidFile(t *testing.T, p proxiedProject) *pidfile.PidFile {
	t.Helper()
	pf, err := pidfile.Read(p.proxyRoot, server.PIDFileName)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s in %s: %v", server.PIDFileName, p.proxyRoot, err)
	}
	return pf
}

// processAlive reports whether pid refers to a live process we can signal
// (kill -0). A pid that exists but is owned by another user would report
// alive=false here; in this lane every process is spawned by the test user.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// managedProxiedArtifacts is a point-in-time snapshot of which lifecycle
// artifacts exist under the proxy root. Lock files legitimately persist on
// disk after a clean shutdown (the flock is what matters, not the file), so
// assertions should usually key on the pid files.
type managedProxiedArtifacts struct {
	ProxyLock   bool
	ProxyPid    bool
	BackendLock bool
	BackendPid  bool
	ConfigYAML  bool
}

func snapshotManagedProxiedArtifacts(p proxiedProject) managedProxiedArtifacts {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(p.proxyRoot, name))
		return err == nil
	}
	return managedProxiedArtifacts{
		ProxyLock:   exists(proxy.LockFileName),
		ProxyPid:    exists(proxy.PIDFileName),
		BackendLock: exists(server.LockFileName),
		BackendPid:  exists(server.PIDFileName),
		ConfigYAML:  exists(proxiedServerConfigName),
	}
}

// waitForManagedProxied polls cond until it reports done or timeout elapses,
// failing the test with the condition's last reported state.
func waitForManagedProxied(t *testing.T, timeout time.Duration, desc string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "(condition never evaluated)"
	for {
		done, state := cond()
		if done {
			return
		}
		last = state
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; last state: %s", timeout, desc, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitForManagedProxiedShutdown waits until both pid files are gone and the
// previously observed proxy and backend processes are dead — the observable
// contract of a clean idle shutdown.
func waitForManagedProxiedShutdown(t *testing.T, p proxiedProject, proxyPID, backendPID int, timeout time.Duration) {
	t.Helper()
	waitForManagedProxied(t, timeout, "idle shutdown of managed proxy and dolt backend", func() (bool, string) {
		arts := snapshotManagedProxiedArtifacts(p)
		proxyDead := !processAlive(proxyPID)
		backendDead := !processAlive(backendPID)
		if !arts.ProxyPid && !arts.BackendPid && proxyDead && backendDead {
			return true, ""
		}
		return false, fmt.Sprintf(
			"proxy.pid exists=%v proxy-child.pid exists=%v proxy(pid %d) alive=%v dolt(pid %d) alive=%v",
			arts.ProxyPid, arts.BackendPid, proxyPID, !proxyDead, backendPID, !backendDead)
	})
}

// heldProxiedConn is a single checked-out SQL connection through the
// managed proxy. While it is held, the proxy's idle watcher sees an active
// connection and will not shut the topology down — the intended way to keep
// the process tree stable while inspecting it. Release() must close the
// entire pool, not just return the conn to it: a pooled-but-idle connection
// keeps its TCP session to the proxy open, which still counts as active and
// blocks idle shutdown indefinitely.
type heldProxiedConn struct {
	Conn *sql.Conn
	db   *sql.DB
}

// Release drops every TCP connection this holder has to the proxy, allowing
// the idle countdown to begin. Safe to call more than once.
func (h *heldProxiedConn) Release() {
	if h.Conn != nil {
		_ = h.Conn.Close()
		h.Conn = nil
	}
	if h.db != nil {
		_ = h.db.Close()
		h.db = nil
	}
}

// openHeldManagedProxiedConn ensures the managed proxy is running (issuing a
// cheap bd command to start or restart it when needed), then returns a held
// connection through it. Callers must Release() it to let the topology idle
// out; a t.Cleanup Release is registered as a backstop.
func openHeldManagedProxiedConn(t *testing.T, bd string, p proxiedProject) *heldProxiedConn {
	t.Helper()

	var held *heldProxiedConn
	waitForManagedProxied(t, 60*time.Second, "held connection through managed proxy", func() (bool, string) {
		pf := readManagedProxyPidFile(t, p)
		if pf == nil || !processAlive(pf.Pid) {
			// Proxy not up (yet, or idled out between commands): any data
			// command restarts it transparently.
			if out, err := bdProxiedRun(t, bd, p.dir, "list", "--json"); err != nil {
				return false, fmt.Sprintf("bd list to (re)start proxy failed: %v\n%s", err, out)
			}
			return false, "proxy pidfile absent; issued bd list to start it"
		}

		dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", pf.Port, p.database)
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return false, fmt.Sprintf("sql.Open %s: %v", dsn, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := db.Conn(ctx)
		cancel()
		if err != nil {
			_ = db.Close()
			return false, fmt.Sprintf("checkout conn via %s: %v", dsn, err)
		}
		held = &heldProxiedConn{Conn: c, db: db}
		t.Cleanup(held.Release)
		return true, ""
	})
	return held
}
