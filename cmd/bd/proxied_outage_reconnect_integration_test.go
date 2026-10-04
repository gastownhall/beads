//go:build cgo && unix

package main

// Real front-door outage/reconnect acceptance matrix. Each case seeds a
// sentinel through bd, takes only the upstream out of service, and verifies a
// bounded failure without mutation before restoring the same endpoint. Proxy
// ownership is exercised separately by killing the proxy and letting bd adopt
// a replacement on the next invocation.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
	"github.com/stretchr/testify/require"
)

func TestProxiedServerOutageReconnectAcceptanceMatrix(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	for _, topology := range []struct {
		name   string
		socket bool
	}{
		{name: "external-tcp"},
		{name: "external-unix-socket", socket: true},
	} {
		t.Run(topology.name, func(t *testing.T) {
			requireSocat(t)
			upstream := testutil.StartIsolatedDoltContainerHandle(t)
			gatePort, err := proxy.PickFreePort()
			require.NoError(t, err)
			var endpoint string
			if topology.socket {
				endpoint = shortSocketPath(t, "dolt.sock")
			} else {
				endpoint = strconv.Itoa(gatePort)
			}
			bridge := startOutageBridge(t, endpoint, upstream.Port, topology.socket)

			var p proxiedProject
			if topology.socket {
				p = bdProxiedInit(t, bd, "outage_socket", "--proxied-server-external-socket-path", endpoint)
			} else {
				p = bdProxiedInit(t, bd, "outage_tcp", "--proxied-server-external-host", "127.0.0.1", "--proxied-server-external-port", endpoint)
			}
			sentinel := bdProxiedCreate(t, bd, p.dir, "outage sentinel")
			before := bdProxiedShow(t, bd, p.dir, sentinel.ID)
			if _, err := os.Stat(filepath.Join(p.proxyRoot, "proxy-child.pid")); err == nil {
				t.Fatal("external topology spawned a local Dolt sidecar")
			}

			// Stop only the upstream. The proxy remains alive and must surface a
			// bounded transport failure; no local fallback is permitted.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			require.NoError(t, upstream.Stop(ctx))
			cancel()
			_, stderr, err, timedOut := runProxiedDeadline(t, bd, p.dir, 3*time.Second, "show", sentinel.ID, "--json")
			if timedOut {
				t.Fatal("upstream outage command exceeded 3s bound")
			}
			if err == nil {
				t.Fatal("show unexpectedly succeeded while upstream was stopped")
			}
			if !actionableTransportError(stderr) {
				t.Fatalf("outage error is not actionable transport refusal: %q", stderr)
			}
			// Restore and verify the exact sentinel survives the outage.
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			require.NoError(t, upstream.Start(ctx))
			newPort, err := upstream.CurrentPort(ctx)
			cancel()
			require.NoError(t, err)
			require.NoError(t, bridge.Process.Kill())
			bridge = startOutageBridge(t, endpoint, newPort, topology.socket)
			var after *types.Issue
			require.Eventually(t, func() bool {
				stdout, _, runErr := bdProxiedRunBuffers(t, bd, p.dir, "show", sentinel.ID, "--json")
				if runErr != nil {
					return false
				}
				after = parseIssueJSON(t, []byte(stdout))
				return true
			}, 10*time.Second, 100*time.Millisecond, "bd reconnect after upstream restart")
			if after.ID != before.ID || after.Title != before.Title {
				t.Fatalf("sentinel changed after reconnect: before=%q/%q after=%q/%q", before.ID, before.Title, after.ID, after.Title)
			}
			if _, err := os.Stat(filepath.Join(p.proxyRoot, "proxy-child.pid")); err == nil {
				t.Fatal("external topology created a local Dolt sidecar during reconnect")
			}

			// Killing the proxy itself must cause a fresh proxy to be adopted on
			// the next command, still reaching the same upstream data.
			if err := proxy.Shutdown(p.proxyRoot); err != nil {
				t.Fatalf("shutdown proxy: %v", err)
			}
			afterProxy := bdProxiedShow(t, bd, p.dir, sentinel.ID)
			if afterProxy.ID != sentinel.ID || afterProxy.Title != sentinel.Title {
				t.Fatalf("sentinel lost after proxy restart: %+v", afterProxy)
			}

			// Endpoint identity changes must refuse stale-proxy adoption.
			info, err := configfile.LoadProxiedServerClientInfo(p.beadsDir)
			if err != nil || info == nil || info.External == nil {
				t.Fatalf("load endpoint identity: info=%+v err=%v", info, err)
			}
			original := *info.External
			if topology.socket {
				info.External.Socket = filepath.Join(t.TempDir(), "stale.sock")
			} else {
				info.External.Port++
			}
			if err := configfile.SaveProxiedServerClientInfo(p.beadsDir, info); err != nil {
				t.Fatalf("rewrite endpoint identity: %v", err)
			}
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "show", sentinel.ID, "--json")
			if err == nil {
				t.Fatalf("stale endpoint unexpectedly succeeded:\nstdout=%s\nstderr=%s", stdout, stderr)
			}
			if !strings.Contains(strings.ToLower(stdout+stderr), "endpoint") {
				t.Fatalf("stale endpoint refusal lacks identity context: %s%s", stdout, stderr)
			}
			info.External = &original
			require.NoError(t, configfile.SaveProxiedServerClientInfo(p.beadsDir, info))
		})
	}
}

func TestManagedLocalProxiedOutageReconnectContract(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "outage_managed", 5*time.Minute)
	sentinel := bdProxiedCreate(t, bd, p.dir, "managed outage sentinel")
	proxyBefore := readManagedProxyPidFile(t, p)
	if proxyBefore == nil || !processAlive(proxyBefore.Pid) {
		t.Fatalf("managed-local proxy is not running: %+v", proxyBefore)
	}
	backend := readManagedBackendPidFile(t, p)
	if backend == nil || !processAlive(backend.Pid) {
		t.Fatal("managed-local backend is not running")
	}
	proc, err := os.FindProcess(backend.Pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	require.Eventually(t, func() bool { return !processAlive(proxyBefore.Pid) }, 3*time.Second, 50*time.Millisecond, "managed proxy did not retire after child death")
	// 1.3.0 contract: child death terminates the owned proxy; the next bd
	// invocation starts a fresh proxy/backend rather than in-place restarting.
	got := bdProxiedShow(t, bd, p.dir, sentinel.ID)
	if got.ID != sentinel.ID || got.Title != "managed outage sentinel" {
		t.Fatalf("managed-local sentinel lost after command-level restart: %+v", got)
	}
	proxyAfter := readManagedProxyPidFile(t, p)
	backendAfter := readManagedBackendPidFile(t, p)
	if proxyBefore == nil || processAlive(proxyBefore.Pid) || processAlive(backend.Pid) {
		t.Fatalf("managed child death left old processes alive: proxy=%v backend=%v", processAlive(proxyBefore.Pid), processAlive(backend.Pid))
	}
	if backendAfter == nil || !processAlive(backendAfter.Pid) {
		t.Fatalf("managed restart backend not alive: %+v", backendAfter)
	}
	if proxyBefore == nil || proxyAfter == nil || proxyAfter.Pid == proxyBefore.Pid {
		t.Fatalf("managed child death did not force a fresh proxy: before=%+v after=%+v", proxyBefore, proxyAfter)
	}
}

func runProxiedDeadline(t *testing.T, bd, dir string, timeout time.Duration, args ...string) (stdout, stderr string, err error, timedOut bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bd, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir, cmd.Env = dir, bdProxiedEnv(dir)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// CommandContext calls Cancel only after Start, so reading cmd.Process
	// here is ordered after Start's write to it -- unlike the watcher
	// goroutine this replaces, which raced Start/Run's write to cmd.Process
	// from a separate goroutine (caught by -race once the deadline expired).
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	err = cmd.Run()
	return out.String(), errOut.String(), err, ctx.Err() != nil
}

func actionableTransportError(s string) bool {
	s = strings.ToLower(s)
	for _, token := range []string{"connection refused", "connect:", "endpoint", "dial", "upstream", "unexpected eof", "invalid connection"} {
		if strings.Contains(s, token) {
			return true
		}
	}
	return false
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 3*time.Second, 20*time.Millisecond,
		"unix socket did not appear")
}

func startOutageBridge(t *testing.T, endpoint, upstreamPort string, socket bool) *exec.Cmd {
	t.Helper()
	var listen string
	if socket {
		// socat refuses to bind over a stale pathname after an outage.
		_ = os.Remove(endpoint)
		listen = "UNIX-LISTEN:" + endpoint + ",fork"
	} else {
		listen = "TCP-LISTEN:" + endpoint + ",bind=127.0.0.1,reuseaddr,fork"
	}
	cmd := exec.Command("socat", listen, "TCP:127.0.0.1:"+upstreamPort)
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool { return processAlive(cmd.Process.Pid) }, time.Second, 20*time.Millisecond, "outage bridge process did not remain alive")
	if socket {
		waitForSocket(t, endpoint)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return cmd
}

// shortSocketPath returns a unix-socket path under a short private directory:
// t.TempDir() nests the (64-byte-truncated) test name under TMPDIR and the
// suite root, which overflows sockaddr_un's 108-byte sun_path.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}
