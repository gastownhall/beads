//go:build linux

package doltserver

import (
	"bufio"
	"errors"
	"fmt"
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
