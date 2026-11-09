package doltserver

import (
	"context"
	"os"
	"regexp"
	"runtime"
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
