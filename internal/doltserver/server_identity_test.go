package doltserver

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/doltversion"
)

// fakePairing makes Start's pairing see ownership as owner reports it and
// read the UUID next returns, and records what it was asked.
type fakePairing struct {
	owner      func(call int) Ownership
	next       func() (string, error)
	ownerCalls int
	queries    int
	pids       []int
	ports      []int
}

func (f *fakePairing) install(t *testing.T) {
	t.Helper()
	origOwner, origQuery := pairListenerOwnership, pairQueryServerUUID
	pairListenerOwnership = func(pid int, host string, port int) Ownership {
		f.ownerCalls++
		f.pids, f.ports = append(f.pids, pid), append(f.ports, port)
		return f.owner(f.ownerCalls)
	}
	pairQueryServerUUID = func(context.Context, string, string, int) (string, error) {
		f.queries++
		return f.next()
	}
	t.Cleanup(func() { pairListenerOwnership, pairQueryServerUUID = origOwner, origQuery })
}

func always(o Ownership) func(int) Ownership { return func(int) Ownership { return o } }

// identityStartWorkspace is fakeStartWorkspace with server identity
// verification on.
func identityStartWorkspace(t *testing.T) string {
	t.Helper()
	beadsDir, _ := fakeStartWorkspace(t)
	t.Setenv(VerifyServerIdentityEnv, "")
	return beadsDir
}

const (
	pairedUUIDA = "aaaaaaaa-1111-4222-8333-444444444444"
	pairedUUIDB = "bbbbbbbb-1111-4222-8333-444444444444"
)

// TestServerIdentityPairsFreshLaunch: Start records the UUID of the server it
// launched and proved owned its listener, before and after reading the UUID,
// in the data directory, private to this user; a stop and a fresh start
// record the new server's UUID, and each matches on a later check.
func TestServerIdentityPairsFreshLaunch(t *testing.T) {
	beadsDir := identityStartWorkspace(t)
	uuid := pairedUUIDA
	fake := &fakePairing{owner: always(OwnershipOwned), next: func() (string, error) { return uuid, nil }}
	fake.install(t)
	record := ServerIdentityRecordPath(ResolveDoltDir(beadsDir))

	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if fake.queries != 1 || fake.ownerCalls != 2 {
		t.Errorf("pairing read the UUID %d times and ownership %d times, want 1 and 2 (before and after)", fake.queries, fake.ownerCalls)
	}
	for i := range fake.pids {
		if fake.pids[i] != state.PID || fake.ports[i] != state.Port {
			t.Errorf("ownership asked about pid %d port %d, want the launched server's %d and %d", fake.pids[i], fake.ports[i], state.PID, state.Port)
		}
	}
	assertRecord(t, record, pairedUUIDA)
	if err := VerifyServerIdentity(context.Background(), uuidRow(t, pairedUUIDA), &ServerIdentityExpectation{DataDir: ResolveDoltDir(beadsDir)}); err != nil {
		t.Errorf("check against the paired server: %v", err)
	}

	if err := Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertRecord(t, record, pairedUUIDA)
	uuid = pairedUUIDB
	if _, err := startForTest(t, beadsDir); err != nil {
		t.Fatalf("restart: %v", err)
	}
	assertRecord(t, record, pairedUUIDB)
	if err := VerifyServerIdentity(context.Background(), uuidRow(t, pairedUUIDB), &ServerIdentityExpectation{DataDir: ResolveDoltDir(beadsDir)}); err != nil {
		t.Errorf("check against the restarted server: %v", err)
	}
}

func assertRecord(t *testing.T, record, want string) {
	t.Helper()
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("identity record: %v", err)
	}
	if string(got) != want+"\n" {
		t.Errorf("identity record = %q, want %q", got, want+"\n")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(record); err == nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("identity record mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

// uuidRow is a server that reports uuid as its @@server_uuid.
func uuidRow(t *testing.T, uuid string) doltversion.RowQuerier {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT @@server_uuid")).
		WillReturnRows(sqlmock.NewRows([]string{"@@server_uuid"}).AddRow(uuid))
	return db
}

// TestServerIdentityPairingRefusesUnprovenListener: a fresh launch whose
// listener another process holds, or whose holder this host cannot show, is
// not paired: Start fails naming why and what to do, the server it launched
// is stopped and its state files removed, the previous record is unchanged,
// and the foreign listener is left running.
func TestServerIdentityPairingRefusesUnprovenListener(t *testing.T) {
	ownedThen := func(after Ownership) func(int) Ownership {
		return func(call int) Ownership {
			if call == 1 {
				return OwnershipOwned
			}
			return after
		}
	}
	ok := func() (string, error) { return pairedUUIDB, nil }
	cases := []struct {
		name     string
		owner    func(int) Ownership
		next     func() (string, error)
		writeErr error
		queries  int
		reason   string
	}{
		{"foreign", always(OwnershipForeign), ok, nil, 0, "another process holds the listener"},
		{"unlisted", always(OwnershipUnlisted), ok, nil, 0, "no listener on the port is listed"},
		{"unknown despite greeting", always(OwnershipUnknown), ok, nil, 0, "cannot show which process holds the listener"},
		{"foreign after the UUID query", ownedThen(OwnershipForeign), ok, nil, 1, "still holds the listener after reading its UUID"},
		{"unknown after the UUID query", ownedThen(OwnershipUnknown), ok, nil, 1, "still holds the listener after reading its UUID"},
		{"UUID unreadable", always(OwnershipOwned), func() (string, error) { return "", errors.New("access denied") }, nil, 1, "access denied"},
		{"record write fails", always(OwnershipOwned), ok, errors.New("disk full"), 1, "disk full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := identityStartWorkspace(t)
			foreign, _ := startFakeSQLServerDelay(t, freeLoopbackPort(t), t.TempDir()+"/foreign.log", "0s")
			fake := &fakePairing{owner: tc.owner, next: tc.next}
			fake.install(t)
			if tc.writeErr != nil {
				orig := writeServerIdentityFile
				writeServerIdentityFile = func(string, string) error { return tc.writeErr }
				t.Cleanup(func() { writeServerIdentityFile = orig })
			}
			dataDir := ResolveDoltDir(beadsDir)
			if err := os.MkdirAll(dataDir, 0o750); err != nil {
				t.Fatal(err)
			}
			record := ServerIdentityRecordPath(dataDir)
			if err := writeServerIdentityRecord(dataDir, pairedUUIDA); err != nil {
				t.Fatal(err)
			}

			state, err := startForTest(t, beadsDir)
			if err == nil {
				t.Fatalf("Start = %+v, want a pairing failure", state)
			}
			for _, want := range []string{"did not record its server identity", tc.reason, VerifyServerIdentityEnv + "=false"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Start error does not say %q:\n%v", want, err)
				}
			}
			if fake.queries != tc.queries {
				t.Errorf("pairing read the UUID %d times, want %d", fake.queries, tc.queries)
			}
			assertRecord(t, record, pairedUUIDA)
			assertNoStateFiles(t, beadsDir)
			if len(fake.pids) == 0 || launchedHere(fake.pids[0]) {
				t.Errorf("the server Start launched (PID %v) is still running", fake.pids)
			}
			if foreign.hasExited() {
				t.Errorf("the foreign listener (PID %d) was stopped", foreign.pid)
			}
		})
	}
}

// TestServerIdentityPairingRefusesExitedChild: a child that has exited is
// not paired, whatever answers on its port.
func TestServerIdentityPairingRefusesExitedChild(t *testing.T) {
	fake := &fakePairing{owner: always(OwnershipOwned), next: func() (string, error) { return pairedUUIDB, nil }}
	fake.install(t)
	exited := make(chan struct{})
	close(exited)
	dataDir := t.TempDir()
	err := pairServerIdentity(t.TempDir(), &startedServer{pid: 4242, exited: exited}, "127.0.0.1", 3307, dataDir)
	if err == nil || !strings.Contains(err.Error(), "it exited") {
		t.Fatalf("pairServerIdentity = %v, want a refusal because the child exited", err)
	}
	if fake.queries != 0 {
		t.Errorf("pairing read the UUID of a server whose child exited")
	}
	if _, err := os.Stat(ServerIdentityRecordPath(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pairing wrote a record: %v", err)
	}
}

// TestServerIdentityNetstatOwnership: on Windows the listener is matched to
// the launched PID by address and port, every listener that receives the
// endpoint's connections counting, not the first one whose address ends in
// ":port".
func TestServerIdentityNetstatOwnership(t *testing.T) {
	const pid = 4100
	header := "\nActive Connections\n\n  Proto  Local Address          Foreign Address        State           PID\n"
	cases := []struct {
		name  string
		host  string
		lines string
		want  Ownership
	}{
		{"own listener", "127.0.0.1", "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n", OwnershipOwned},
		{"wslrelay holds the port", "127.0.0.1", "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       9876\n", OwnershipForeign},
		{"own and a wildcard holder", "127.0.0.1",
			"  TCP    0.0.0.0:3307           0.0.0.0:0              LISTENING       9876\n" +
				"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n", OwnershipForeign},
		{"IPv6 wildcard holder", "127.0.0.1",
			"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n" +
				"  TCP    [::]:3307              [::]:0                 LISTENING       9876\n", OwnershipForeign},
		{"another address family's loopback does not count", "127.0.0.1",
			"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n" +
				"  TCP    [::1]:3307             [::]:0                 LISTENING       9876\n", OwnershipOwned},
		{"a longer port with the same suffix does not count", "127.0.0.1",
			"  TCP    127.0.0.1:13307        0.0.0.0:0              LISTENING       9876\n", OwnershipUnlisted},
		{"connections and UDP do not count", "127.0.0.1",
			"  TCP    127.0.0.1:3307         127.0.0.1:50000        ESTABLISHED     9876\n" +
				"  UDP    127.0.0.1:3307         *:*                                    9876\n" +
				"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n", OwnershipOwned},
		{"IPv6 loopback endpoint", "::1", "  TCP    [::1]:3307             [::]:0                 LISTENING       4100\n", OwnershipOwned},
		{"localhost endpoint", "localhost", "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       9876\n", OwnershipForeign},
		{"nothing listed", "127.0.0.1", "", OwnershipUnlisted},
		{"unparseable PID", "127.0.0.1", "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       ?\n", OwnershipUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyNetstatListeners(header+strings.ReplaceAll(tc.lines, "\n", "\r\n"), pid, tc.host, 3307); got != tc.want {
				t.Errorf("classifyNetstatListeners = %v, want %v", got, tc.want)
			}
		})
	}
}
