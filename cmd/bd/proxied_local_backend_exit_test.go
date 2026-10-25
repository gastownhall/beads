//go:build cgo && unix

package main

import (
	"encoding/json"
	"strings"
	"syscall"
	"testing"
	"time"
)

// backendCleanExitRetireTimeout bounds how long the proxy may outlive a dolt
// backend that shut down gracefully. dolt's own SIGTERM shutdown dominates it;
// the proxy's health watcher polls every 100ms.
const backendCleanExitRetireTimeout = 30 * time.Second

// TestManagedLocalProxiedBackendCleanExitRetiresProxy extends the child-death
// contract (see TestManagedLocalProxiedOutageReconnectContract, which kills
// the backend) to a backend that exits CLEANLY: SIGTERM makes dolt shut down
// gracefully with status 0. The proxy must retire just as it does for a
// crash, so the next bd command starts a fresh proxy/backend pair and `bd
// ping` answers from a live store.
//
// Before the fix the proxy only noticed non-zero exits. With idle timeout 0
// (never idle out) it stayed up and adoptable in front of a dead backend
// indefinitely: `bd dolt status` said "running", and every command, `bd ping`
// included, failed until an operator ran `bd dolt stop`.
func TestManagedLocalProxiedBackendCleanExitRetiresProxy(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "clean_exit", "--proxied-server-idle-timeout", "0")
	sentinel := bdProxiedCreate(t, bd, p.dir, "clean exit sentinel")

	proxyBefore := readManagedProxyPidFile(t, p)
	if proxyBefore == nil || !processAlive(proxyBefore.Pid) {
		t.Fatalf("managed-local proxy is not running: %+v", proxyBefore)
	}
	backendBefore := readManagedBackendPidFile(t, p)
	if backendBefore == nil || !processAlive(backendBefore.Pid) {
		t.Fatalf("managed-local backend is not running: %+v", backendBefore)
	}

	if err := syscall.Kill(backendBefore.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM dolt backend pid %d: %v", backendBefore.Pid, err)
	}
	waitForManagedProxiedShutdown(t, p, proxyBefore.Pid, backendBefore.Pid, backendCleanExitRetireTimeout)

	out, err := bdProxiedRun(t, bd, p.dir, "ping", "--json")
	if err != nil {
		t.Fatalf("bd ping after the backend exited: %v\n%s", err, out)
	}
	var ping struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &ping); err != nil || ping.Status != "ok" {
		t.Fatalf("bd ping --json: status=%q err=%v\n%s", ping.Status, err, out)
	}
	if got := bdProxiedShow(t, bd, p.dir, sentinel.ID); got.Title != "clean exit sentinel" {
		t.Fatalf("sentinel after restart: %+v", got)
	}

	proxyAfter := readManagedProxyPidFile(t, p)
	backendAfter := readManagedBackendPidFile(t, p)
	if proxyAfter == nil || proxyAfter.Pid == proxyBefore.Pid || !processAlive(proxyAfter.Pid) {
		t.Fatalf("expected a fresh live proxy: before=%+v after=%+v", proxyBefore, proxyAfter)
	}
	if backendAfter == nil || backendAfter.Pid == backendBefore.Pid || !processAlive(backendAfter.Pid) {
		t.Fatalf("expected a fresh live backend: before=%+v after=%+v", backendBefore, backendAfter)
	}
}
