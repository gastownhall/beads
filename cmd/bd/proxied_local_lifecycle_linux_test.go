//go:build cgo && linux

package main

// Managed-local proxied lifecycle smoke lane (Linux first).
//
// This is the first end-to-end test of the production local-first proxied
// topology: bd spawns a detached loopback proxy, the proxy spawns a local
// `dolt sql-server`, and no external host/port or testcontainer is
// involved. It proves, on one disposable repository:
//
//   1. init/create/read work against the launched local process tree;
//   2. the proxy and the default generated backend listeners are
//      loopback-only (both statically, via the generated config.yaml, and
//      dynamically, by enumerating the live processes' listening sockets);
//   3. the topology shuts itself down after the configured idle window;
//   4. a later command transparently restarts it and the data persisted.
//
// Run locally with:
//
//	BEADS_TEST_PROXIED_LOCAL=1 go test -tags gms_pure_go ./cmd/bd \
//	    -run TestManagedLocalProxiedLifecycleSmoke -v
//
// CI additionally runs it inside a network namespace with only loopback up
// (see .github/workflows/proxied-local-smoke.yml), proving the whole
// lifecycle needs no outbound network once bd and dolt are installed.
// When BEADS_TEST_PROXIED_LOCAL=1 is set, missing prerequisites or a Dolt
// child that fails to launch FAIL the test rather than skipping.

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
)

func TestManagedLocalProxiedLifecycleSmoke(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)

	// Short idle window so the shutdown phase is observable in test time;
	// the production default is 30s.
	const idleTimeout = 5 * time.Second
	p := bdManagedLocalInit(t, bd, "mlp", idleTimeout)

	// Hold a connection through the proxy for the whole inspection phase:
	// with an active connection the idle watcher never arms, so the process
	// tree cannot shut down under us between assertions.
	held := openHeldManagedProxiedConn(t, bd, p)

	issue := bdProxiedCreate(t, bd, p.dir, "managed local smoke issue")
	if issue.ID == "" {
		t.Fatal("bd create returned an empty issue ID")
	}
	shown := bdProxiedShow(t, bd, p.dir, issue.ID)
	if shown.Title != "managed local smoke issue" {
		t.Errorf("bd show title: got %q, want %q", shown.Title, "managed local smoke issue")
	}

	// --- Live topology inspection -------------------------------------

	proxyPF := readManagedProxyPidFile(t, p)
	if proxyPF == nil {
		t.Fatal("proxy.pid missing while a proxied connection is held open")
	}
	backendPF := readManagedBackendPidFile(t, p)
	if backendPF == nil {
		t.Fatal("proxy-child.pid missing while a proxied connection is held open")
	}
	if !processAlive(proxyPF.Pid) {
		t.Fatalf("proxy process %d is not alive", proxyPF.Pid)
	}
	if !processAlive(backendPF.Pid) {
		t.Fatalf("dolt backend process %d is not alive", backendPF.Pid)
	}

	// Identity sanity: the pidfiles must point at the kinds of processes
	// they claim to (the full identity handshake is separate hardening
	// work; this catches gross mismatches).
	if cl := procCmdline(proxyPF.Pid); !strings.Contains(cl, "db-proxy-child") {
		t.Errorf("proxy pid %d cmdline %q does not look like a bd db-proxy-child", proxyPF.Pid, cl)
	}
	if cl := procCmdline(backendPF.Pid); !strings.Contains(cl, "sql-server") {
		t.Errorf("backend pid %d cmdline %q does not look like dolt sql-server", backendPF.Pid, cl)
	}

	// Static loopback proof: the generated config.yaml is Beads-managed and
	// binds the backend listener to 127.0.0.1 on the backend pidfile's port.
	assertManagedConfigLoopback(t, p, backendPF.Port)

	// Dynamic loopback proof: each live process owns a 127.0.0.1 listener
	// on the exact port its pidfile advertises, and neither process holds a
	// TCP listener on a non-loopback address.
	assertExpectedLoopbackListener(t, proxyPF.Pid, proxyPF.Port, "proxy (bd db-proxy-child)")
	assertExpectedLoopbackListener(t, backendPF.Pid, backendPF.Port, "dolt sql-server backend")

	// The held connection must reach the same data bd wrote, through the
	// proxy listener the pidfile advertises.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var title string
	if err := held.Conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issue.ID).Scan(&title); err != nil {
		t.Fatalf("SELECT through held proxy connection for %s: %v", issue.ID, err)
	}
	if title != "managed local smoke issue" {
		t.Errorf("title via proxy SQL: got %q, want %q", title, "managed local smoke issue")
	}

	arts := snapshotManagedProxiedArtifacts(p)
	if !arts.ProxyLock || !arts.BackendLock || !arts.ConfigYAML {
		t.Errorf("expected proxy.lock, proxy-child.lock, and config.yaml under %s while running; got %+v",
			p.proxyRoot, arts)
	}

	// --- Idle shutdown --------------------------------------------------

	held.Release()
	waitForManagedProxiedShutdown(t, p, proxyPF.Pid, backendPF.Pid, 90*time.Second)

	// --- Transparent restart and persistence ----------------------------

	reread := bdProxiedShow(t, bd, p.dir, issue.ID)
	if reread.ID != issue.ID || reread.Title != "managed local smoke issue" {
		t.Fatalf("after restart: got id=%q title=%q, want id=%q title=%q",
			reread.ID, reread.Title, issue.ID, "managed local smoke issue")
	}

	held2 := openHeldManagedProxiedConn(t, bd, p)
	defer held2.Release()

	proxyPF2 := readManagedProxyPidFile(t, p)
	if proxyPF2 == nil {
		t.Fatal("proxy.pid missing after transparent restart")
	}
	if proxyPF2.Pid == proxyPF.Pid {
		t.Errorf("restarted proxy reused pid %d of the shut-down proxy; expected a new process", proxyPF.Pid)
	}
	if !processAlive(proxyPF2.Pid) {
		t.Fatalf("restarted proxy process %d is not alive", proxyPF2.Pid)
	}
	assertExpectedLoopbackListener(t, proxyPF2.Pid, proxyPF2.Port, "restarted proxy (bd db-proxy-child)")
}

// assertManagedConfigLoopback verifies the generated backend config.yaml
// carries the Beads-managed marker, binds its listener host to 127.0.0.1,
// and agrees with the port the backend pidfile advertises.
func assertManagedConfigLoopback(t *testing.T, p proxiedProject, backendPort int) {
	t.Helper()
	path := proxiedServerConfigPath(p.beadsDir)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated backend config %s: %v", path, err)
	}
	if !isManagedProxiedServerConfig(body) {
		t.Errorf("%s lacks the Beads-managed marker; the default lane must use a generated config", path)
	}
	cfg, err := servercfg.NewYamlConfig(body)
	if err != nil {
		t.Fatalf("parse generated backend config %s: %v", path, err)
	}
	if got := cfg.Host(); got != "127.0.0.1" {
		t.Errorf("generated backend listener host: got %q, want %q", got, "127.0.0.1")
	}
	if got := cfg.Port(); got != backendPort {
		t.Errorf("generated backend port %d disagrees with proxy-child.pid port %d", got, backendPort)
	}
}

// assertExpectedLoopbackListener fails unless pid owns 127.0.0.1 on the exact
// port its pidfile advertises, or if it owns any listening TCP socket bound to
// a non-loopback address.
func assertExpectedLoopbackListener(t *testing.T, pid, advertisedPort int, label string) {
	t.Helper()
	listeners, err := listeningTCPAddrs(pid)
	if err != nil {
		t.Fatalf("enumerate listeners of %s (pid %d): %v", label, pid, err)
	}
	expected := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(advertisedPort))
	if err := validateExpectedLoopbackListener(listeners, expected); err != nil {
		t.Errorf("%s (pid %d): %v", label, pid, err)
	}
}

func validateExpectedLoopbackListener(listeners []netip.AddrPort, expected netip.AddrPort) error {
	expected = unmapAddrPort(expected)
	foundExpected := false
	for _, ap := range listeners {
		normalized := unmapAddrPort(ap)
		if !normalized.Addr().IsLoopback() {
			return fmt.Errorf("listens on non-loopback address %s", ap)
		}
		if normalized == expected {
			foundExpected = true
		}
	}
	if !foundExpected {
		return fmt.Errorf("does not listen on advertised address %s; listeners: %v", expected, listeners)
	}
	return nil
}

func unmapAddrPort(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

func TestValidateExpectedLoopbackListener(t *testing.T) {
	v4 := netip.MustParseAddr
	tests := []struct {
		name      string
		listeners []netip.AddrPort
		expected  netip.AddrPort
		wantErr   string
	}{
		{
			name:      "exact advertised listener",
			listeners: []netip.AddrPort{netip.AddrPortFrom(v4("127.0.0.1"), 3307)},
			expected:  netip.AddrPortFrom(v4("127.0.0.1"), 3307),
		},
		{
			name:      "IPv4-mapped listener matches",
			listeners: []netip.AddrPort{netip.AddrPortFrom(v4("::ffff:127.0.0.1"), 3307)},
			expected:  netip.AddrPortFrom(v4("127.0.0.1"), 3307),
		},
		{
			name: "additional loopback listener allowed",
			listeners: []netip.AddrPort{
				netip.AddrPortFrom(v4("127.0.0.1"), 3307),
				netip.AddrPortFrom(v4("::1"), 3308),
			},
			expected: netip.AddrPortFrom(v4("127.0.0.1"), 3307),
		},
		{
			name:      "missing advertised port",
			listeners: []netip.AddrPort{netip.AddrPortFrom(v4("127.0.0.1"), 3308)},
			expected:  netip.AddrPortFrom(v4("127.0.0.1"), 3307),
			wantErr:   "does not listen on advertised address 127.0.0.1:3307",
		},
		{
			name: "non-loopback listener rejected",
			listeners: []netip.AddrPort{
				netip.AddrPortFrom(v4("127.0.0.1"), 3307),
				netip.AddrPortFrom(v4("0.0.0.0"), 3307),
			},
			expected: netip.AddrPortFrom(v4("127.0.0.1"), 3307),
			wantErr:  "listens on non-loopback address 0.0.0.0:3307",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExpectedLoopbackListener(tt.listeners, tt.expected)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateExpectedLoopbackListener() error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateExpectedLoopbackListener() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// listeningTCPAddrs returns every TCP address pid is LISTENing on, by
// joining the socket inodes in /proc/<pid>/fd against /proc/net/tcp and
// /proc/net/tcp6. Reusable for port-safety and stale-listener scenarios.
func listeningTCPAddrs(pid int) ([]netip.AddrPort, error) {
	inodes, err := socketInodes(pid)
	if err != nil {
		return nil, err
	}
	var out []netip.AddrPort
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		addrs, err := listenersInTable(table, inodes)
		if err != nil {
			return nil, err
		}
		out = append(out, addrs...)
	}
	return out, nil
}

// socketInodes returns the set of socket inodes held open by pid.
func socketInodes(pid int) (map[string]bool, error) {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fdDir, err)
	}
	inodes := make(map[string]bool)
	for _, e := range entries {
		target, err := os.Readlink(fmt.Sprintf("%s/%s", fdDir, e.Name()))
		if err != nil {
			continue // fd closed while iterating
		}
		if rest, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(rest, "]")] = true
		}
	}
	return inodes, nil
}

// listenersInTable parses a /proc/net/tcp{,6} table and returns the local
// addresses of rows in LISTEN state whose inode is in inodes.
func listenersInTable(path string, inodes map[string]bool) ([]netip.AddrPort, error) {
	const tcpListen = "0A"
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // e.g. tcp6 absent on ipv6-less kernels
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out []netip.AddrPort
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		// sl local_address rem_address st ... inode is field index 9
		if len(fields) < 10 || fields[3] != tcpListen || !inodes[fields[9]] {
			continue
		}
		ap, err := parseProcNetAddr(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s: parse local address %q: %w", path, fields[1], err)
		}
		out = append(out, ap)
	}
	return out, nil
}

// parseProcNetAddr decodes a /proc/net/tcp{,6} local_address column
// ("HEXIP:HEXPORT", with the IP stored as little-endian 32-bit words).
func parseProcNetAddr(s string) (netip.AddrPort, error) {
	ipHex, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("no port separator in %q", s)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("port %q: %w", portHex, err)
	}
	raw, err := hex.DecodeString(ipHex)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("ip %q: %w", ipHex, err)
	}
	switch len(raw) {
	case 4:
		return netip.AddrPortFrom(
			netip.AddrFrom4([4]byte{raw[3], raw[2], raw[1], raw[0]}), uint16(port)), nil
	case 16:
		var b [16]byte
		for word := 0; word < 4; word++ {
			for i := 0; i < 4; i++ {
				b[word*4+i] = raw[word*4+3-i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom16(b), uint16(port)), nil
	default:
		return netip.AddrPort{}, fmt.Errorf("ip %q: unexpected length %d", ipHex, len(raw))
	}
}

// procCmdline returns the space-joined command line of pid via procfs, or
// "" when unreadable. A cheap identity check that a pidfile points at the
// kind of process it claims to.
func procCmdline(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
}
