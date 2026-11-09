//go:build !windows

package doltserver

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"

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

// pinnedInConfig is a launched server that reports the server UUID Start set
// in beadsDir's generated server config, as dolt does: "" when it set none.
func pinnedInConfig(beadsDir string) func() (string, error) {
	return func() (string, error) {
		body, err := os.ReadFile(doltServerConfigPath(beadsDir))
		if err != nil {
			return "", err
		}
		cfg, err := servercfg.NewYamlConfig(body)
		if err != nil {
			return "", err
		}
		pinned, _ := cfg.SystemVars()["server_uuid"].(string)
		return pinned, nil
	}
}

// mintUUIDs makes the server UUIDs bd mints ids, in order, and fails the
// test when it mints more.
func mintUUIDs(t *testing.T, ids ...string) {
	t.Helper()
	orig := newServerUUID
	newServerUUID = func() string {
		if len(ids) == 0 {
			t.Error("bd minted a server UUID it should have kept")
			return pairedUUIDB
		}
		id := ids[0]
		ids = ids[1:]
		return id
	}
	t.Cleanup(func() { newServerUUID = orig })
}

// countRecordWrites counts identity record writes; they still happen.
func countRecordWrites(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := writeServerIdentityFile
	writeServerIdentityFile = func(dataDir, uuid string) error { n++; return orig(dataDir, uuid) }
	t.Cleanup(func() { writeServerIdentityFile = orig })
	return &n
}

// TestServerIdentityPairsFreshLaunch: the first Start of a data directory
// mints its server UUID, records it there, private to this user, before the
// launch, and starts the server with it; once the server is up it checks,
// with the launch proven to own its listener before and after, that the
// server reports that UUID. A stop and a fresh start keep the UUID and write
// no record, and each server matches on a later check.
func TestServerIdentityPairsFreshLaunch(t *testing.T) {
	beadsDir := identityStartWorkspace(t)
	fake := &fakePairing{owner: always(OwnershipOwned), next: pinnedInConfig(beadsDir)}
	fake.install(t)
	mintUUIDs(t, pairedUUIDA)
	writes := countRecordWrites(t)
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
	if pinned, _ := pinnedInConfig(beadsDir)(); pinned != pairedUUIDA {
		t.Errorf("the server was started with server UUID %q, want the minted %q", pinned, pairedUUIDA)
	}
	assertRecord(t, record, pairedUUIDA)
	if err := VerifyServerIdentity(context.Background(), uuidRow(t, pairedUUIDA), &ServerIdentityExpectation{DataDir: ResolveDoltDir(beadsDir)}); err != nil {
		t.Errorf("check against the paired server: %v", err)
	}

	if err := Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertRecord(t, record, pairedUUIDA)
	if _, err := startForTest(t, beadsDir); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if pinned, _ := pinnedInConfig(beadsDir)(); pinned != pairedUUIDA {
		t.Errorf("the restarted server was started with server UUID %q, want %q kept", pinned, pairedUUIDA)
	}
	assertRecord(t, record, pairedUUIDA)
	if *writes != 1 {
		t.Errorf("the start and the restart wrote the record %d times, want once (the first start's mint)", *writes)
	}
	if err := VerifyServerIdentity(context.Background(), uuidRow(t, pairedUUIDA), &ServerIdentityExpectation{DataDir: ResolveDoltDir(beadsDir)}); err != nil {
		t.Errorf("check against the restarted server: %v", err)
	}
}

// TestServerIdentityCheckOffStartKeepsTheRecordedUUID: a Start with the check
// turned off, after one with it on, still starts the server with the UUID the
// data directory's record names, and writes and pairs nothing, so a later
// open with the check on accepts that server. Turning the check off for one
// command, or a project on the shared server whose config turns it off, does
// not get bd's own server refused by every command that checks it.
func TestServerIdentityCheckOffStartKeepsTheRecordedUUID(t *testing.T) {
	beadsDir := identityStartWorkspace(t)
	fake := &fakePairing{owner: always(OwnershipOwned), next: pinnedInConfig(beadsDir)}
	fake.install(t)
	mintUUIDs(t, pairedUUIDA)
	dataDir := ResolveDoltDir(beadsDir)
	if _, err := startForTest(t, beadsDir); err != nil {
		t.Fatalf("Start with the check on: %v", err)
	}
	if err := Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	t.Setenv(VerifyServerIdentityEnv, "false")
	writes := countRecordWrites(t)
	if _, err := startForTest(t, beadsDir); err != nil {
		t.Fatalf("Start with the check off: %v", err)
	}
	pinned, err := pinnedInConfig(beadsDir)()
	if err != nil {
		t.Fatal(err)
	}
	if pinned != pairedUUIDA {
		t.Errorf("the server started with the check off was given server UUID %q, want the recorded %q", pinned, pairedUUIDA)
	}
	if *writes != 0 || fake.queries != 1 {
		t.Errorf("the start with the check off wrote the record %d times and paired %d times, want neither", *writes, fake.queries-1)
	}
	assertRecord(t, ServerIdentityRecordPath(dataDir), pairedUUIDA)

	// A server started with nothing pinned reports dolt's own UUID, never the
	// one bd recorded.
	reports := pinned
	if reports == "" {
		reports = pairedUUIDB
	}
	if err := VerifyServerIdentity(context.Background(), uuidRow(t, reports), &ServerIdentityExpectation{DataDir: dataDir}); err != nil {
		t.Errorf("a checked open of the server started with the check off: %v", err)
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
// listener another process holds, or that a host which lists this process's
// own listeners does not list, or whose proof lapses while its UUID is read,
// or that reports a server UUID other than the one Start pinned, is not
// paired: Start fails naming why and what to do, the server it launched is
// stopped and its state files removed, the record is unchanged, and the
// foreign listener is left running.
func TestServerIdentityPairingRefusesUnprovenListener(t *testing.T) {
	origProvable := listenerOwnershipProvable
	listenerOwnershipProvable = func() bool { return true }
	t.Cleanup(func() { listenerOwnershipProvable = origProvable })

	ownedThen := func(after Ownership) func(int) Ownership {
		return func(call int) Ownership {
			if call == 1 {
				return OwnershipOwned
			}
			return after
		}
	}
	// The record names pairedUUIDA, so Start pins it and its own server
	// reports it.
	ok := func() (string, error) { return pairedUUIDA, nil }
	cases := []struct {
		name    string
		owner   func(int) Ownership
		next    func() (string, error)
		queries int
		reason  string
	}{
		{"foreign", always(OwnershipForeign), ok, 0, "another process holds the listener"},
		{"unlisted on a host that lists its own", always(OwnershipUnlisted), ok, 0, "no listener on the port is listed"},
		{"foreign after the UUID query", ownedThen(OwnershipForeign), ok, 1, "still holds the listener after reading its UUID"},
		{"unlisted after the UUID query", ownedThen(OwnershipUnlisted), ok, 1, "still holds the listener after reading its UUID"},
		{"unknown after the UUID query", ownedThen(OwnershipUnknown), ok, 1, "no longer shows which process holds the listener"},
		{"UUID unreadable", always(OwnershipOwned), func() (string, error) { return "", errors.New("access denied") }, 1, "access denied"},
		{"another server UUID than the pinned one", always(OwnershipOwned), func() (string, error) { return pairedUUIDB, nil }, 1,
			"it reports server UUID " + pairedUUIDB + ", not the " + pairedUUIDA + " bd started it with"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := identityStartWorkspace(t)
			foreign, _ := startFakeSQLServerDelay(t, freeLoopbackPort(t), t.TempDir()+"/foreign.log", "0s")
			fake := &fakePairing{owner: tc.owner, next: tc.next}
			fake.install(t)
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
			for _, want := range []string{"could not verify its server identity", tc.reason, VerifyServerIdentityEnv + "=false"} {
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

// TestServerIdentityRecordWriteFailureLaunchesNothing: when the server UUID a
// first Start mints cannot be recorded, Start fails naming the record and
// the write error before it launches any server, and leaves no state files
// and no record behind.
func TestServerIdentityRecordWriteFailureLaunchesNothing(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	t.Setenv(VerifyServerIdentityEnv, "")
	fake := &fakePairing{owner: always(OwnershipOwned), next: pinnedInConfig(beadsDir)}
	fake.install(t)
	orig := writeServerIdentityFile
	writeServerIdentityFile = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { writeServerIdentityFile = orig })
	record := ServerIdentityRecordPath(ResolveDoltDir(beadsDir))

	state, err := startForTest(t, beadsDir)
	if err == nil {
		t.Fatalf("Start = %+v, want the record write failure", state)
	}
	for _, want := range []string{"disk full", record} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Start error does not say %q:\n%v", want, err)
		}
	}
	if got := launchedPorts(t, launches); len(got) != 0 {
		t.Errorf("Start launched dolt sql-server on %v before it could record the server UUID", got)
	}
	if fake.queries != 0 {
		t.Errorf("pairing ran %d UUID queries for a launch that never happened", fake.queries)
	}
	assertNoStateFiles(t, beadsDir)
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed record write left a record: %v", err)
	}
}

// TestServerIdentityPairingOnUnprovableHost: where the host cannot show
// which process holds a listener (no lookup on the platform, as on FreeBSD,
// or a /proc that lists no sockets, as on WSL1), Start pairs the server it
// launched on the evidence its startup wait accepted. With the server UUID
// pinned, the server reporting it is that evidence and Start warns of
// nothing; a CLI-flag launch (a dolt whose version bd cannot tell) pins
// none, so Start records the UUID the server reports and warns that it did
// so on its startup alone. A listing that shows another process holding the
// listener still refuses (TestServerIdentityPairingRefusesUnprovenListener).
func TestServerIdentityPairingOnUnprovableHost(t *testing.T) {
	origProvable := listenerOwnershipProvable
	listenerOwnershipProvable = func() bool { return false }
	t.Cleanup(func() { listenerOwnershipProvable = origProvable })
	unknownThenOwned := func(call int) Ownership {
		if call == 1 {
			return OwnershipUnknown
		}
		return OwnershipOwned
	}
	launches := []struct {
		name string
		// version is the fake dolt's `dolt version`; one bd cannot read
		// makes Start launch with CLI flags, which pin no UUID.
		version string
		pinned  bool
	}{
		{"pinned", "", true},
		{"CLI flags", "unknown", false},
	}
	// identityLaunch is the workspace and the fake pairing for one launch:
	// the server reports the pinned UUID, or pairedUUIDA when none is.
	identityLaunch := func(t *testing.T, version string, pinned bool, owner func(int) Ownership) (string, *fakePairing, *strings.Builder) {
		beadsDir := identityStartWorkspace(t)
		if version != "" {
			t.Setenv(testFakeDoltVersionEnv, version)
		}
		next := func() (string, error) { return pairedUUIDA, nil }
		if pinned {
			mintUUIDs(t, pairedUUIDA)
			next = pinnedInConfig(beadsDir)
		}
		fake := &fakePairing{owner: owner, next: next}
		fake.install(t)
		var warnings strings.Builder
		origWarnings := pairWarnings
		pairWarnings = &warnings
		t.Cleanup(func() { pairWarnings = origWarnings })
		return beadsDir, fake, &warnings
	}
	for _, launch := range launches {
		for _, tc := range []struct {
			name  string
			owner func(int) Ownership
		}{
			{"no lookup on this platform", always(OwnershipUnknown)},
			{"no sockets listed on this host", always(OwnershipUnlisted)},
			{"shown only after the UUID query", unknownThenOwned},
		} {
			t.Run(launch.name+"/"+tc.name, func(t *testing.T) {
				beadsDir, fake, warnings := identityLaunch(t, launch.version, launch.pinned, tc.owner)
				record := ServerIdentityRecordPath(ResolveDoltDir(beadsDir))

				state, err := startForTest(t, beadsDir)
				if err != nil {
					t.Fatalf("Start: %v", err)
				}
				if fake.queries != 1 || fake.ownerCalls != 2 {
					t.Errorf("pairing read the UUID %d times and ownership %d times, want 1 and 2", fake.queries, fake.ownerCalls)
				}
				assertRecord(t, record, pairedUUIDA)
				if pinned, _ := pinnedInConfig(beadsDir)(); launch.pinned != (pinned == pairedUUIDA) {
					t.Errorf("the server was started with server UUID %q; pinned launch = %v", pinned, launch.pinned)
				}
				if launch.pinned {
					if warnings.Len() != 0 {
						t.Errorf("a pairing the pinned UUID proved warned:\n%s", warnings.String())
					}
					return
				}
				for _, want := range []string{"cannot show which process holds the listener", "PID " + strconv.Itoa(state.PID), record} {
					if !strings.Contains(warnings.String(), want) {
						t.Errorf("pairing warning does not say %q:\n%s", want, warnings.String())
					}
				}
			})
		}
		t.Run(launch.name+"/a foreign holder still refuses", func(t *testing.T) {
			beadsDir, _, _ := identityLaunch(t, launch.version, launch.pinned, always(OwnershipForeign))
			if state, err := startForTest(t, beadsDir); err == nil || !strings.Contains(err.Error(), "another process holds the listener") {
				t.Fatalf("Start = (%+v, %v), want the foreign listener refused", state, err)
			}
			if !launch.pinned {
				if _, err := os.Stat(ServerIdentityRecordPath(ResolveDoltDir(beadsDir))); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("a refused pairing wrote a record: %v", err)
				}
			}
		})
	}
	t.Run("proven pairing does not warn", func(t *testing.T) {
		beadsDir, _, warnings := identityLaunch(t, "unknown", false, always(OwnershipOwned))
		if _, err := startForTest(t, beadsDir); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if warnings.Len() != 0 {
			t.Errorf("a proven pairing warned:\n%s", warnings.String())
		}
	})
}

// TestServerIdentityPairingRefusesExitedChild: a child that has exited is
// not paired, whatever answers on its port.
func TestServerIdentityPairingRefusesExitedChild(t *testing.T) {
	fake := &fakePairing{owner: always(OwnershipOwned), next: func() (string, error) { return pairedUUIDB, nil }}
	fake.install(t)
	exited := make(chan struct{})
	close(exited)
	dataDir := t.TempDir()
	err := pairServerIdentity(t.TempDir(), &startedServer{pid: 4242, exited: exited}, "127.0.0.1", 3307, dataDir, "")
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

// TestServerIdentityPairingRefusesChildExitedDuringQuery: a child that exits
// while its UUID is being read is not paired, although it held the listener
// before the query and the query answered: the UUID may have come from
// whatever took the port over. The previous record is left as it was.
func TestServerIdentityPairingRefusesChildExitedDuringQuery(t *testing.T) {
	exited := make(chan struct{})
	fake := &fakePairing{owner: always(OwnershipOwned), next: func() (string, error) {
		close(exited)
		return pairedUUIDB, nil
	}}
	fake.install(t)
	writes := countRecordWrites(t)
	dataDir := t.TempDir()
	if err := writeServerIdentityRecord(dataDir, pairedUUIDA); err != nil {
		t.Fatal(err)
	}

	err := pairServerIdentity(t.TempDir(), &startedServer{pid: 4242, exited: exited}, "127.0.0.1", 3307, dataDir, "")
	if err == nil || !strings.Contains(err.Error(), "still holds the listener after reading its UUID: it exited") {
		t.Fatalf("pairServerIdentity = %v, want a refusal because the child exited during the UUID query", err)
	}
	if fake.queries != 1 {
		t.Errorf("pairing read the UUID %d times, want 1", fake.queries)
	}
	if *writes != 0 {
		t.Errorf("pairing attempted %d record writes, want none", *writes)
	}
	assertRecord(t, ServerIdentityRecordPath(dataDir), pairedUUIDA)
}

// TestServerIdentityPairingWritesOnlyAChangedRecord: pairing leaves a record
// that already names the server's UUID alone, so a restart rewrites nothing
// (on Windows a rename over a record another process is reading is refused).
// A launch with nothing pinned records a UUID that differs, and a record it
// cannot write fails the pairing and stays as it was.
func TestServerIdentityPairingWritesOnlyAChangedRecord(t *testing.T) {
	alive := &startedServer{pid: 4242, exited: make(chan struct{})}
	pair := func(t *testing.T, reports, pinned string, record string) (string, *int, error) {
		t.Helper()
		fake := &fakePairing{owner: always(OwnershipOwned), next: func() (string, error) { return reports, nil }}
		fake.install(t)
		dataDir := t.TempDir()
		if record != "" {
			if err := writeServerIdentityRecord(dataDir, record); err != nil {
				t.Fatal(err)
			}
		}
		writes := countRecordWrites(t)
		err := pairServerIdentity(t.TempDir(), alive, "127.0.0.1", 3307, dataDir, pinned)
		return dataDir, writes, err
	}

	for _, pinned := range []string{"", pairedUUIDA} {
		dataDir, writes, err := pair(t, pairedUUIDA, pinned, pairedUUIDA)
		if err != nil {
			t.Fatalf("pinned %q: pairing a server whose UUID the record names: %v", pinned, err)
		}
		if *writes != 0 {
			t.Errorf("pinned %q: pairing rewrote a record that already names the server %d times", pinned, *writes)
		}
		assertRecord(t, ServerIdentityRecordPath(dataDir), pairedUUIDA)
	}

	dataDir, writes, err := pair(t, pairedUUIDB, "", pairedUUIDA)
	if err != nil {
		t.Fatalf("pairing an unpinned launch's new UUID: %v", err)
	}
	if *writes != 1 {
		t.Errorf("pairing wrote the record %d times, want once", *writes)
	}
	assertRecord(t, ServerIdentityRecordPath(dataDir), pairedUUIDB)

	orig := writeServerIdentityFile
	writeServerIdentityFile = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { writeServerIdentityFile = orig })
	fake := &fakePairing{owner: always(OwnershipOwned), next: func() (string, error) { return pairedUUIDB, nil }}
	fake.install(t)
	dataDir = t.TempDir()
	if err := writeServerIdentityRecord(dataDir, pairedUUIDA); err != nil {
		t.Fatal(err)
	}
	err = pairServerIdentity(t.TempDir(), alive, "127.0.0.1", 3307, dataDir, "")
	for _, want := range []string{"could not verify its server identity", "disk full"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("pairServerIdentity with a failing record write = %v, want it to say %q", err, want)
		}
	}
	assertRecord(t, ServerIdentityRecordPath(dataDir), pairedUUIDA)
}

// TestPinServerIdentity: the server UUID a launch pins is the one the data
// directory's record names, read without a write, with the check on or off;
// with no usable record (none, empty or malformed) it is a new one, recorded
// before it is returned, but only with the check on. With the check off and
// no usable record, or in a CLI-flag launch, record or not, it pins nothing
// and leaves the data directory alone.
func TestPinServerIdentity(t *testing.T) {
	t.Setenv(VerifyServerIdentityEnv, "")
	t.Run("kept from the record", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.WriteFile(ServerIdentityRecordPath(dataDir), []byte(strings.ToUpper(pairedUUIDA)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mintUUIDs(t)
		writes := countRecordWrites(t)
		got, err := pinServerIdentity(t.TempDir(), dataDir, true)
		if err != nil || got != pairedUUIDA {
			t.Fatalf("pinServerIdentity = (%q, %v), want the recorded %q", got, err, pairedUUIDA)
		}
		if *writes != 0 {
			t.Errorf("keeping the recorded UUID wrote the record %d times", *writes)
		}
	})
	for name, content := range map[string]string{"no record": "", "empty": "\n", "malformed": "not-a-uuid\n"} {
		t.Run("minted with "+name, func(t *testing.T) {
			dataDir := t.TempDir()
			if content != "" {
				if err := os.WriteFile(ServerIdentityRecordPath(dataDir), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mintUUIDs(t, pairedUUIDB)
			got, err := pinServerIdentity(t.TempDir(), dataDir, true)
			if err != nil || got != pairedUUIDB {
				t.Fatalf("pinServerIdentity = (%q, %v), want the minted %q", got, err, pairedUUIDB)
			}
			assertRecord(t, ServerIdentityRecordPath(dataDir), pairedUUIDB)
		})
	}
	t.Run("a real mint is a server UUID", func(t *testing.T) {
		dataDir := t.TempDir()
		got, err := pinServerIdentity(t.TempDir(), dataDir, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := normalizeServerUUID(got); !ok {
			t.Errorf("minted %q, which the record check would refuse as malformed", got)
		}
		again, err := pinServerIdentity(t.TempDir(), dataDir, true)
		if err != nil || again != got {
			t.Errorf("second pin = (%q, %v), want %q kept", again, err, got)
		}
	})
	t.Run("check off with a record pins it", func(t *testing.T) {
		t.Setenv(VerifyServerIdentityEnv, "false")
		dataDir := t.TempDir()
		if err := writeServerIdentityRecord(dataDir, pairedUUIDA); err != nil {
			t.Fatal(err)
		}
		mintUUIDs(t)
		writes := countRecordWrites(t)
		got, err := pinServerIdentity(t.TempDir(), dataDir, true)
		if err != nil || got != pairedUUIDA {
			t.Fatalf("pinServerIdentity = (%q, %v), want the recorded %q", got, err, pairedUUIDA)
		}
		if *writes != 0 {
			t.Errorf("pinning the recorded UUID with the check off wrote the record %d times", *writes)
		}
	})
	for _, tc := range []struct {
		name         string
		configLaunch bool
		check        string
		// record is the record's content beforehand; "" is no record.
		record string
	}{
		{"check off with no record", true, "false", ""},
		{"check off with a malformed record", true, "false", "not-a-uuid\n"},
		{"CLI-flag launch", false, "", ""},
		{"CLI-flag launch with a record", false, "", pairedUUIDA + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(VerifyServerIdentityEnv, tc.check)
			mintUUIDs(t)
			dataDir := t.TempDir()
			record := ServerIdentityRecordPath(dataDir)
			if tc.record != "" {
				if err := os.WriteFile(record, []byte(tc.record), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writes := countRecordWrites(t)
			got, err := pinServerIdentity(t.TempDir(), dataDir, tc.configLaunch)
			if err != nil || got != "" {
				t.Fatalf("pinServerIdentity = (%q, %v), want nothing pinned", got, err)
			}
			if *writes != 0 {
				t.Errorf("pinning nothing wrote the record %d times", *writes)
			}
			if tc.record == "" {
				if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("pinning nothing wrote a record: %v", err)
				}
			} else if got, err := os.ReadFile(record); err != nil || string(got) != tc.record {
				t.Errorf("record after pinning nothing = (%q, %v), want %q unchanged", got, err, tc.record)
			}
		})
	}
}

// TestServerIdentityNetstatOwnership: on Windows the listener is matched to
// the launched PID by address and port, every listener that receives the
// endpoint's connections counting, not the first one whose address ends in
// ":port". A listener is told by its foreign address, whatever language
// netstat prints its state in, and a holder that descends from the launched
// PID (a launcher shim's child) is the launch's own.
func TestServerIdentityNetstatOwnership(t *testing.T) {
	const pid = 4100
	origParents := processParents
	processParents = func() (map[int]int, error) { return map[int]int{4100: 1, 4200: 4100, 9876: 1}, nil }
	t.Cleanup(func() { processParents = origParents })
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
		{"an IPv6 wildcard beside the own IPv4 listener", "127.0.0.1",
			"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n" +
				"  TCP    [::]:3307              [::]:0                 LISTENING       9876\n", OwnershipOwned},
		{"a foreign IPv6 wildcard alone", "127.0.0.1", "  TCP    [::]:3307              [::]:0                 LISTENING       9876\n", OwnershipForeign},
		{"another address family's loopback does not count", "127.0.0.1",
			"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n" +
				"  TCP    [::1]:3307             [::]:0                 LISTENING       9876\n", OwnershipOwned},
		{"a longer port with the same suffix does not count", "127.0.0.1",
			"  TCP    127.0.0.1:13307        0.0.0.0:0              LISTENING       9876\n", OwnershipUnlisted},
		{"connections and UDP do not count", "127.0.0.1",
			"  TCP    127.0.0.1:3307         127.0.0.1:50000        ESTABLISHED     9876\n" +
				"  UDP    127.0.0.1:3307         *:*                                    9876\n" +
				"  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\n", OwnershipOwned},
		{"German netstat", "127.0.0.1",
			"  TCP    127.0.0.1:3307         0.0.0.0:0              ABHÖREN         4100\n" +
				"  TCP    127.0.0.1:3307         127.0.0.1:50000        HERGESTELLT     9876\n", OwnershipOwned},
		{"a state of two words", "127.0.0.1", "  TCP    127.0.0.1:3307         0.0.0.0:0              EN ÉCOUTE       9876\n", OwnershipForeign},
		{"a launcher's child holds the listener", "127.0.0.1", "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4200\n", OwnershipOwned},
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
	t.Run("process tree unreadable", func(t *testing.T) {
		processParents = func() (map[int]int, error) { return nil, errors.New("snapshot failed") }
		lines := "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4200\r\n"
		if got := classifyNetstatListeners(header+lines, pid, "127.0.0.1", 3307); got != OwnershipUnknown {
			t.Errorf("classifyNetstatListeners with no process tree = %v, want %v", got, OwnershipUnknown)
		}
		own := "  TCP    127.0.0.1:3307         0.0.0.0:0              LISTENING       4100\r\n"
		if got := classifyNetstatListeners(header+own, pid, "127.0.0.1", 3307); got != OwnershipOwned {
			t.Errorf("classifyNetstatListeners of the launched PID's own listener with no process tree = %v, want %v", got, OwnershipOwned)
		}
	})
}

// TestSharedServerIdentityExpectationExemptsServersManagedOutsideBD: the
// shared server bd runs is checked, unless the workspace declares that
// something else runs it: external mode, or auto-start off in the
// environment, its config.yaml or the loaded configuration.
func TestSharedServerIdentityExpectationExemptsServersManagedOutsideBD(t *testing.T) {
	for _, env := range []string{"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_AUTO_START", VerifyServerIdentityEnv} {
		t.Setenv(env, "")
	}
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	workspace := func(t *testing.T, configYAML string) string {
		beadsDir := t.TempDir()
		if configYAML != "" {
			if err := os.WriteFile(beadsDir+"/config.yaml", []byte(configYAML), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return beadsDir
	}
	if SharedServerIdentityExpectation(workspace(t, ""), "127.0.0.1", 3308) == nil {
		t.Fatal("no expectation for the shared server bd runs")
	}
	if SharedServerIdentityExpectation(workspace(t, "dolt.auto-start: true\n"), "127.0.0.1", 3308) == nil {
		t.Fatal("auto-start on exempted the shared server")
	}
	for _, tc := range []struct {
		name, env, value, configYAML string
	}{
		{"external mode", "BEADS_DOLT_SERVER_MODE", "1", ""},
		{"auto-start off in the environment", "BEADS_DOLT_AUTO_START", "0", ""},
		{"auto-start off in config.yaml", "", "", "dolt.auto-start: false\n"},
		{"auto-start off in config.yaml, nested", "", "", "dolt:\n  auto-start: off\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(tc.env, tc.value)
			}
			beadsDir := workspace(t, tc.configYAML)
			if got := SharedServerIdentityExpectation(beadsDir, "127.0.0.1", 3308); got != nil {
				t.Errorf("expectation = %+v, want none", *got)
			}
			if !ServerManagedOutsideBD(beadsDir) {
				t.Error("ServerManagedOutsideBD = false")
			}
		})
	}
}

// countIdentityLookups counts the refusal diagnostics' PID lookups.
func countIdentityLookups(t *testing.T) *int {
	t.Helper()
	n := 0
	origListener, origRecorded := identityListenerPID, identityRecordedPID
	identityListenerPID = func(int) int { n++; return 0 }
	identityRecordedPID = func(string) int { n++; return 0 }
	t.Cleanup(func() { identityListenerPID, identityRecordedPID = origListener, origRecorded })
	return &n
}

// TestServerIdentityMatchingRecordNeedsNoLookups: a record that names the
// answering server passes with no listener or process lookup, whatever the
// record's case and trailing whitespace; only a refusal looks them up.
func TestServerIdentityMatchingRecordNeedsNoLookups(t *testing.T) {
	lookups := countIdentityLookups(t)
	dataDir := t.TempDir()
	exp := &ServerIdentityExpectation{DataDir: dataDir, ServerDir: t.TempDir(), Host: "127.0.0.1", Port: 3307}
	if err := os.WriteFile(exp.RecordPath(), []byte(strings.ToUpper(pairedUUIDA)+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyServerIdentity(context.Background(), uuidRow(t, pairedUUIDA), exp); err != nil {
		t.Fatalf("matching record: %v", err)
	}
	if *lookups != 0 {
		t.Errorf("a matching check made %d PID lookups, want none", *lookups)
	}

	err := VerifyServerIdentity(context.Background(), uuidRow(t, pairedUUIDB), exp)
	if !errors.Is(err, ErrServerIdentityMismatch) {
		t.Fatalf("mismatch = %v, want ErrServerIdentityMismatch", err)
	}
	if *lookups != 2 {
		t.Errorf("a refusal made %d PID lookups, want 2 (recorded and listener)", *lookups)
	}
}
