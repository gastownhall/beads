//go:build linux

package doltserver

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProc builds a /proc tree with one listener (inode 4242) on port and a
// process pid whose fd table holds sockets for inodes.
func fakeProc(t *testing.T, port, pid int, tcpRows bool, inodes ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o750); err != nil {
		t.Fatal(err)
	}
	table := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	if tcpRows {
		table += fmt.Sprintf("   0: 0100007F:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 4242 1 0 100 0 0 10 0\n", port)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	fd := filepath.Join(root, fmt.Sprint(pid), "fd")
	if err := os.MkdirAll(fd, 0o750); err != nil {
		t.Fatal(err)
	}
	for i, ino := range inodes {
		if err := os.Symlink("socket:["+ino+"]", filepath.Join(fd, fmt.Sprint(i+3))); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func withProcRoot(t *testing.T, root string) {
	t.Helper()
	orig := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = orig })
}

func withListenerOwnershipProvable(t *testing.T, provable bool) {
	t.Helper()
	orig := listenerOwnershipProvable
	listenerOwnershipProvable = func() bool { return provable }
	t.Cleanup(func() { listenerOwnershipProvable = orig })
}

func TestListenerOwnership_FakeProc(t *testing.T) {
	const port, pid = 41234, 777
	for _, tc := range []struct {
		name         string
		rows         bool
		inodes       []string
		pid          int
		unreadableFD bool
		noNetTCP     bool
		provable     bool
		ownership    Ownership
		owned, known bool
	}{
		{name: "child holds the listener", rows: true, inodes: []string{"4242"}, pid: pid, ownership: OwnershipOwned, owned: true, known: true},
		{name: "listener belongs to someone else", rows: true, inodes: []string{"9999"}, pid: pid, ownership: OwnershipForeign, known: true},
		// A child that has exited (and been reaped) owns nothing; this must
		// not read as "unknown", which would accept a foreign greeting.
		{name: "child gone from /proc", rows: true, pid: pid + 1, ownership: OwnershipForeign, known: true},
		// Something answered but /proc lists no listener. Where this /proc
		// lists no sockets at all (WSL1, some sandboxes) it is not telling
		// the whole story, so server mode falls back rather than call it
		// foreign; where it provably lists this user's own listeners, what
		// answered is out of its sight (a forward, or on darwin another
		// user's socket) and so not the child's.
		{name: "no listener row, host lists no sockets", rows: false, inodes: []string{"4242"}, pid: pid, ownership: OwnershipUnlisted},
		{name: "no listener row, ownership provable", rows: false, inodes: []string{"4242"}, pid: pid, provable: true, ownership: OwnershipUnlisted, known: true},
		{name: "fd table unreadable", rows: true, inodes: []string{"4242"}, pid: pid, unreadableFD: true, ownership: OwnershipUnknown},
		// A listener table that cannot be read is not an empty one.
		{name: "no /proc/net/tcp", rows: true, inodes: []string{"4242"}, pid: pid, noNetTCP: true, ownership: OwnershipUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeProc(t, port, pid, tc.rows, tc.inodes...)
			if tc.noNetTCP {
				if err := os.Remove(filepath.Join(root, "net", "tcp")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unreadableFD {
				if os.Geteuid() == 0 {
					t.Skip("root reads a mode-000 directory")
				}
				fd := filepath.Join(root, fmt.Sprint(pid), "fd")
				if err := os.Chmod(fd, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(fd, 0o750) })
			}
			withProcRoot(t, root)
			withListenerOwnershipProvable(t, tc.provable)
			if got := classifyListener(tc.pid, port); got != tc.ownership {
				t.Errorf("classifyListener = %d, want %d", got, tc.ownership)
			}
			owned, known := listenerOwnership(tc.pid, port)
			if owned != tc.owned || known != tc.known {
				t.Errorf("listenerOwnership = (%v, %v), want (%v, %v)", owned, known, tc.owned, tc.known)
			}
		})
	}
}

// TestAwaitOwnedListener_UnlistedListenerOnProvableHostIsForeign pins server
// mode against another account's listener on the port it picked. darwin's
// lsof cannot see another user's socket, so classifyListener reports such a
// listener unlisted (simulated here by a /proc that lists none); where the
// host provably lists this process's own listeners it is not the child's,
// and at the default warning level, with no ready line to wait for, startup
// must end with ErrPortInUse instead of accepting its greeting.
func TestAwaitOwnedListener_UnlistedListenerOnProvableHostIsForeign(t *testing.T) {
	port := foreignGreeter(t)
	logPath := filepath.Join(t.TempDir(), "dolt-server.log")
	srv, off := startFakeSQLServerDelay(t, port, logPath, "10s")
	withProcRoot(t, fakeProc(t, port, srv.pid, false))
	withListenerOwnershipProvable(t, true)
	err := awaitOwnedListener(srv, startupProbe{
		host: "127.0.0.1", port: port, logPath: logPath, logOffset: off, timeout: 5 * time.Second,
	})
	if !errors.Is(err, ErrPortInUse) {
		t.Fatalf("awaitOwnedListener = %v, want ErrPortInUse for another account's listener", err)
	}
}

func TestParseProcNetTCPListeners(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:A0D2 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0 100 0 0 10 0
   1: 0100007F:A0D2 0100007F:C000 01 00000000:00000000 00:00000000 00000000  1000        0 222 1 0 100 0 0 10 0
   2: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 333 1 0 100 0 0 10 0
`
	for _, tc := range []struct {
		name  string
		table string
		port  int
		want  []string
	}{
		{name: "listener only, not the established row", table: table, port: 0xA0D2, want: []string{"111"}},
		{name: "other port", table: table, port: 0x1F90, want: []string{"333"}},
		{name: "empty table", table: "", port: 0xA0D2},
		{name: "header only", table: strings.SplitN(table, "\n", 2)[0] + "\n", port: 0xA0D2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]bool{}
			if err := parseProcNetTCPListeners(bufio.NewScanner(strings.NewReader(tc.table)), tc.port, got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("inodes = %v, want %v", got, tc.want)
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("missing inode %s in %v", w, got)
				}
			}
		})
	}
}

// TestListenerOwnership_RealProc runs against the real /proc: this process
// owns its own listener, and a pid that does not exist owns nothing (known).
func TestListenerOwnership_RealProc(t *testing.T) {
	port := foreignGreeter(t)
	if owned, known := listenerOwnership(os.Getpid(), port); !owned || !known {
		t.Errorf("listenerOwnership(self, own listener) = (%v, %v), want (true, true)", owned, known)
	}
	if owned, known := listenerOwnership(1<<30, port); owned || !known {
		t.Errorf("listenerOwnership(no such pid) = (%v, %v), want (false, true)", owned, known)
	}
}

// TestListenerOwnershipProvable pins the host probe cmd/bd uses to choose
// the generated proxied config's log level: true where /proc shows this
// process's own listener, and false where /proc lists no listener at all
// (WSL1, some sandboxes), so such a host keeps dolt's ready line as proof.
func TestListenerOwnershipProvable(t *testing.T) {
	if !ListenerOwnershipProvable() {
		t.Fatal("ListenerOwnershipProvable() = false against the real /proc, want true")
	}
	withProcRoot(t, fakeProc(t, 1, os.Getpid(), false))
	if ListenerOwnershipProvable() {
		t.Error("ListenerOwnershipProvable() = true although /proc lists no listener, want false")
	}
}

// procListener is one listener row of a fake /proc/net/tcp{,6}.
type procListener struct {
	ip    string
	inode string
}

// procNetAddr encodes ip as /proc/net/tcp{,6} prints it: 32-bit words in
// host byte order.
func procNetAddr(ip net.IP) string {
	var b strings.Builder
	for i := 0; i < len(ip); i += 4 {
		fmt.Fprintf(&b, "%08X", binary.NativeEndian.Uint32(ip[i:]))
	}
	return b.String()
}

// fakeProcAt builds a /proc tree whose tcp and tcp6 tables list listeners
// on port, whose processes hold the socket inodes fds names, and whose
// children files name each process's children.
func fakeProcAt(t *testing.T, port int, listeners []procListener, fds map[int][]string, children map[int][]int) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o750); err != nil {
		t.Fatal(err)
	}
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	tables := map[string]string{"tcp": header, "tcp6": header}
	for i, l := range listeners {
		ip, table := net.ParseIP(l.ip), "tcp6"
		if v4 := ip.To4(); v4 != nil {
			ip, table = v4, "tcp"
		}
		tables[table] += fmt.Sprintf("   %d: %s:%04X %s:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 %s 1 0 100 0 0 10 0\n",
			i, procNetAddr(ip), port, strings.Repeat("0", len(procNetAddr(ip))), l.inode)
	}
	for name, table := range tables {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(table), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for pid, inodes := range fds {
		fd := filepath.Join(root, fmt.Sprint(pid), "fd")
		if err := os.MkdirAll(fd, 0o750); err != nil {
			t.Fatal(err)
		}
		for i, ino := range inodes {
			if err := os.Symlink("socket:["+ino+"]", filepath.Join(fd, fmt.Sprint(i+3))); err != nil {
				t.Fatal(err)
			}
		}
	}
	for pid, kids := range children {
		task := filepath.Join(root, fmt.Sprint(pid), "task", fmt.Sprint(pid))
		if err := os.MkdirAll(task, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(task, "children"), []byte(strings.Trim(fmt.Sprint(kids), "[]")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestServerIdentityListenerOwnershipAt_FakeProc: pairing counts every
// listener that receives the endpoint's connections, so a foreign holder of
// the dialed address (or of a wildcard covering it) is foreign even when the
// child holds another socket on the port, which classifyListener accepts. An
// IPv6 wildcard receives an IPv4 endpoint's connections only where no IPv4
// listener does: beside one it is IPv6-only, or loses them to it.
func TestServerIdentityListenerOwnershipAt_FakeProc(t *testing.T) {
	const port, child, wrapped, foreign = 41235, 777, 778, 900
	cases := []struct {
		name      string
		host      string
		listeners []procListener
		fds       map[int][]string
		children  map[int][]int
		pid       int
		noNetTCP  bool
		want      Ownership
		portOnly  Ownership
	}{
		{name: "child holds the address", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "wrapper's child holds the address", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}},
			fds: map[int][]string{child: nil, wrapped: {"11"}}, children: map[int][]int{child: {wrapped}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "foreign holds the address, child another", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}, {"::1", "12"}},
			fds: map[int][]string{child: {"12"}, foreign: {"11"}}, pid: child, want: OwnershipForeign, portOnly: OwnershipOwned},
		{name: "mixed holders: child and a foreign wildcard", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}, {"0.0.0.0", "12"}},
			fds: map[int][]string{child: {"11"}, foreign: {"12"}}, pid: child, want: OwnershipForeign, portOnly: OwnershipOwned},
		{name: "foreign IPv6 wildcard beside the child's IPv4 listener", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}, {"::", "12"}},
			fds: map[int][]string{child: {"11"}, foreign: {"12"}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "foreign IPv6 wildcard alone", host: "127.0.0.1", listeners: []procListener{{"::", "12"}},
			fds: map[int][]string{child: nil, foreign: {"12"}}, pid: child, want: OwnershipForeign, portOnly: OwnershipForeign},
		{name: "child's IPv6 wildcard alone", host: "127.0.0.1", listeners: []procListener{{"::", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "foreign IPv4 wildcard beside the child's IPv6 wildcard", host: "127.0.0.1", listeners: []procListener{{"::", "11"}, {"0.0.0.0", "12"}},
			fds: map[int][]string{child: {"11"}, foreign: {"12"}}, pid: child, want: OwnershipForeign, portOnly: OwnershipOwned},
		{name: "localhost endpoint", host: "localhost", listeners: []procListener{{"127.0.0.1", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "a host name has no address to match", host: "db.example", listeners: []procListener{{"127.0.0.1", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, want: OwnershipUnknown, portOnly: OwnershipOwned},
		{name: "foreign on another address does not count", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}, {"192.0.2.7", "12"}},
			fds: map[int][]string{child: {"11"}, foreign: {"12"}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "IPv6 loopback endpoint", host: "::1", listeners: []procListener{{"::1", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, want: OwnershipOwned, portOnly: OwnershipOwned},
		{name: "only another address listed", host: "127.0.0.1", listeners: []procListener{{"192.0.2.7", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, want: OwnershipUnlisted, portOnly: OwnershipOwned},
		{name: "child gone from /proc", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}},
			fds: map[int][]string{foreign: {"11"}}, pid: child, want: OwnershipForeign, portOnly: OwnershipForeign},
		{name: "no /proc/net/tcp", host: "127.0.0.1", listeners: []procListener{{"127.0.0.1", "11"}},
			fds: map[int][]string{child: {"11"}}, pid: child, noNetTCP: true, want: OwnershipUnknown, portOnly: OwnershipUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeProcAt(t, port, tc.listeners, tc.fds, tc.children)
			if tc.noNetTCP {
				for _, name := range []string{"tcp", "tcp6"} {
					if err := os.Remove(filepath.Join(root, "net", name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			withProcRoot(t, root)
			if got := classifyListenerAt(tc.pid, tc.host, port); got != tc.want {
				t.Errorf("classifyListenerAt = %d, want %d", got, tc.want)
			}
			if got := classifyListener(tc.pid, port); got != tc.portOnly {
				t.Errorf("classifyListener = %d, want %d (the port-only check this replaces for pairing)", got, tc.portOnly)
			}
		})
	}
}

func TestServerIdentityDecodeProcNetAddr(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "0.0.0.0", "192.0.2.7", "::1", "::", "2001:db8::5"} {
		parsed := net.ParseIP(ip)
		if v4 := parsed.To4(); v4 != nil {
			parsed = v4
		}
		if got := decodeProcNetAddr(procNetAddr(parsed)); got != netip.MustParseAddr(ip) {
			t.Errorf("decodeProcNetAddr(%s) = %v, want %s", procNetAddr(parsed), got, ip)
		}
	}
	// A tcp6 socket bound to an IPv4-mapped address is that IPv4 address.
	if got := decodeProcNetAddr(procNetAddr(net.ParseIP("::ffff:127.0.0.1").To16())); got != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("decodeProcNetAddr(::ffff:127.0.0.1) = %v, want 127.0.0.1", got)
	}
	if got := decodeProcNetAddr("0100007F"); binary.NativeEndian.Uint16([]byte{1, 0}) == 1 && got != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("decodeProcNetAddr(0100007F) = %v on a little-endian host, want 127.0.0.1", got)
	}
	for _, bad := range []string{"", "0100007", "zz00007F", "0100007F00"} {
		if got := decodeProcNetAddr(bad); got.IsValid() {
			t.Errorf("decodeProcNetAddr(%q) = %v, want the zero address", bad, got)
		}
	}
}

// TestServerIdentityIPv6OnlyWildcardBesideIPv4Listener runs the IPv6-only
// case against this host's real sockets: an IPv6-only wildcard listener
// (IPV6_V6ONLY, which Go sets for "tcp6") on the port of an IPv4 loopback
// listener is listed in /proc beside it, receives none of the IPv4
// endpoint's connections, and so does not count against the listener that
// does.
func TestServerIdentityIPv6OnlyWildcardBesideIPv4Listener(t *testing.T) {
	v4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v4.Close() }()
	port := v4.Addr().(*net.TCPAddr).Port
	v6, err := net.Listen("tcp6", fmt.Sprintf("[::]:%d", port))
	if err != nil {
		t.Skipf("no IPv6-only wildcard listener on this host beside the IPv4 one: %v", err)
	}
	defer func() { _ = v6.Close() }()

	conn, err := net.Dial("tcp4", v4.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := v6.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if c, err := v6.Accept(); err == nil {
		_ = c.Close()
		t.Fatal("the IPv6-only wildcard accepted the IPv4 connection")
	}

	listeners, err := listeningSocketAddrs(port)
	if err != nil {
		t.Fatal(err)
	}
	addrs := make([]netip.Addr, len(listeners))
	for i, l := range listeners {
		addrs[i] = l.addr
	}
	want := map[netip.Addr]bool{netip.MustParseAddr("127.0.0.1"): true, netip.IPv6Unspecified(): false}
	if len(addrs) != len(want) {
		t.Fatalf("/proc lists %v on port %d, want the IPv4 loopback and the IPv6 wildcard", addrs, port)
	}
	for i, recv := range receivingListeners(addrs, netip.MustParseAddr("127.0.0.1")) {
		if wantRecv, ok := want[addrs[i]]; !ok || recv != wantRecv {
			t.Errorf("listener %v receives 127.0.0.1's connections = %v, want %v", addrs[i], recv, wantRecv)
		}
	}
	if got := classifyListenerAt(os.Getpid(), "127.0.0.1", port); got != OwnershipOwned {
		t.Errorf("classifyListenerAt(self) = %v, want %v", got, OwnershipOwned)
	}
}
