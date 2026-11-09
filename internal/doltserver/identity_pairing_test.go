package doltserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pairing refusal tests (GH#4637, AC-4): a launch whose listener belongs to
// another process, or whose holder cannot be proven, must never trust or
// record a UUID, must leave any prior record untouched, and must return an
// actionable failure. Every outcome is forced through the seams, so no live
// server is needed and no case depends on the platform's listener classifier.

// fakePairingServer is a startedServer whose liveness the test controls.
type fakePairingServer struct {
	srv *startedServer
	pid int
}

func newLivePairingServer() *fakePairingServer {
	return &fakePairingServer{srv: &startedServer{pid: 424242, exited: make(chan struct{})}}
}

// pairOutcome describes one forced listener-ownership answer.
type pairOutcome struct {
	owned bool
	known bool
}

// runPairing executes pairServerIdentity against a live fake child with the
// given seam behaviors: the ownership answers (consumed in order, repeating
// the last one), the UUID the query reports, and whether the child exits
// while the query runs. It returns the error, the number of record writes
// attempted, and the data dir the record would live in.
func runPairing(t *testing.T, ownership []pairOutcome, uuid string, exitDuringQuery bool, writeErr error) (error, int, string) {
	t.Helper()

	origOwnership, origUUID, origWrite := pairingListenerOwnership, pairingServerUUID, writeServerUUIDRecord
	t.Cleanup(func() {
		pairingListenerOwnership = origOwnership
		pairingServerUUID = origUUID
		writeServerUUIDRecord = origWrite
	})

	fake := newLivePairingServer()
	calls := 0
	writes := 0
	pairingListenerOwnership = func(pid int, host string, port int) (bool, bool) {
		calls++
		if calls > len(ownership) {
			calls = len(ownership)
		}
		outcome := ownership[calls-1]
		return outcome.owned, outcome.known
	}
	pairingServerUUID = func(host string, port int) (string, error) {
		if exitDuringQuery {
			close(fake.srv.exited)
		}
		if uuid == "" {
			return "", errors.New("the server reported an empty @@server_uuid")
		}
		return uuid, nil
	}
	writeServerUUIDRecord = func(path, recorded string) error {
		writes++
		return writeErr
	}

	dataDir := t.TempDir()
	err := pairServerIdentity(fake.srv, "127.0.0.1", 3319, dataDir)
	return err, writes, dataDir
}

// assertNoRecordWritten fails when the refusal path still recorded or
// replaced a UUID, and returns the record path for state assertions.
func assertNoRecordWritten(t *testing.T, writes int, dataDir string, prior []byte) string {
	t.Helper()
	recordPath := filepath.Join(dataDir, ServerIdentityRecordName)
	if writes != 0 {
		t.Fatalf("pairing refusal must not record or replace the identity record; %d write(s) attempted", writes)
	}
	if prior == nil {
		if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
			t.Fatalf("identity record must not exist after a refused pairing: %v", statErr)
		}
	} else {
		got, readErr := os.ReadFile(recordPath)
		if readErr != nil {
			t.Fatalf("prior identity record must survive a refused pairing: %v", readErr)
		}
		if string(got) != string(prior) {
			t.Fatalf("prior identity record was rewritten by a refused pairing: got %q, want %q", string(got), string(prior))
		}
	}
	return recordPath
}

// TestPairingRefusesForeignListener: a listener the launched child provably
// does NOT own — the mirrored/forwarded-port case the gate exists for — must
// refuse before any UUID is trusted or recorded.
func TestPairingRefusesForeignListener(t *testing.T) {
	err, writes, dataDir := runPairing(t, []pairOutcome{{owned: false, known: true}}, "dddddddd-1111-2222-3333-444444444444", false, nil)
	if err == nil {
		t.Fatal("expected a pairing refusal for a listener owned by another process")
	}
	assertNoRecordWritten(t, writes, dataDir, nil)
	for _, want := range []string{"another process", "424242", "127.0.0.1:3319"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must be actionable and name %q, got:\n%s", want, err.Error())
		}
	}
}

// TestPairingRefusesUnknownOwnership: a holder the platform cannot prove —
// including a listener whose only evidence is the readiness greeting — must
// not pair. The readiness greeting says a dolt answered; it does not say
// WHICH dolt, or that it is the child bd launched.
func TestPairingRefusesUnknownOwnership(t *testing.T) {
	err, writes, dataDir := runPairing(t, []pairOutcome{{owned: false, known: false}}, "dddddddd-1111-2222-3333-444444444444", false, nil)
	if err == nil {
		t.Fatal("expected a pairing refusal for an unprovable listener")
	}
	assertNoRecordWritten(t, writes, dataDir, nil)
	for _, want := range []string{"cannot prove", "greeting"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name %q, got:\n%s", want, err.Error())
		}
	}
}

// TestPairingRefusesChildExitBeforeQuery: a child that died before its
// identity could be read cannot pair — the listener that answered the
// greeting is not provably the child anymore.
func TestPairingRefusesChildExitBeforeQuery(t *testing.T) {
	err, writes, dataDir := runPairing(t, []pairOutcome{{owned: true, known: true}}, "dddddddd-1111-2222-3333-444444444444", true, nil)
	if err == nil {
		t.Fatal("expected a pairing refusal for an exited child")
	}
	assertNoRecordWritten(t, writes, dataDir, nil)
	if !strings.Contains(err.Error(), "exited before its identity") {
		t.Fatalf("refusal must name the exited child, got:\n%s", err.Error())
	}
}

// TestPairingRefusesLostOwnershipAfterQuery: the actual takeover case — the
// child owns the listener when the identity query starts, and a foreign
// process holds it by the time the query returns. The second ownership check
// exists for exactly this; a single check would have recorded the foreign
// listener's identity.
func TestPairingRefusesLostOwnershipAfterQuery(t *testing.T) {
	err, writes, dataDir := runPairing(t, []pairOutcome{
		{owned: true, known: true},
		{owned: false, known: true},
	}, "dddddddd-1111-2222-3333-444444444444", false, nil)
	if err == nil {
		t.Fatal("expected a pairing refusal when ownership is lost across the identity query")
	}
	assertNoRecordWritten(t, writes, dataDir, nil)
	if !strings.Contains(err.Error(), "another process") {
		t.Fatalf("refusal must name the foreign holder, got:\n%s", err.Error())
	}
}

// TestPairingWriteFailurePreservesPriorRecord: a failed record write fails
// identity establishment without replacing the prior record — the atomic
// write must leave the workspace verifiable against the old identity.
func TestPairingWriteFailurePreservesPriorRecord(t *testing.T) {
	dataDir := t.TempDir()
	prior := []byte("99999999-8888-7777-6666-555555555555\n")
	if err := os.WriteFile(filepath.Join(dataDir, ServerIdentityRecordName), prior, 0o600); err != nil {
		t.Fatal(err)
	}

	origOwnership, origUUID, origWrite := pairingListenerOwnership, pairingServerUUID, writeServerUUIDRecord
	t.Cleanup(func() {
		pairingListenerOwnership = origOwnership
		pairingServerUUID = origUUID
		writeServerUUIDRecord = origWrite
	})
	pairingListenerOwnership = func(pid int, host string, port int) (bool, bool) { return true, true }
	pairingServerUUID = func(host string, port int) (string, error) {
		return "dddddddd-1111-2222-3333-444444444444", nil
	}
	writeServerUUIDRecord = func(path, recorded string) error {
		return errors.New("disk full")
	}

	fake := newLivePairingServer()
	err := pairServerIdentity(fake.srv, "127.0.0.1", 3319, dataDir)
	if err == nil {
		t.Fatal("expected a pairing failure when the record cannot be written")
	}
	if !strings.Contains(err.Error(), "recording the started server's identity") {
		t.Fatalf("refusal must name the failed record write, got:\n%s", err.Error())
	}
	got, readErr := os.ReadFile(filepath.Join(dataDir, ServerIdentityRecordName))
	if readErr != nil {
		t.Fatalf("prior identity record must survive a failed write: %v", readErr)
	}
	if string(got) != string(prior) {
		t.Fatalf("failed write replaced the prior record: got %q, want %q", string(got), string(prior))
	}
}

// TestPairingRefusesEmptyUUID: a server that answers but reports no identity
// cannot be paired — there is nothing trustworthy to record.
func TestPairingRefusesEmptyUUID(t *testing.T) {
	err, writes, dataDir := runPairing(t, []pairOutcome{{owned: true, known: true}}, "", false, nil)
	if err == nil {
		t.Fatal("expected a pairing refusal for an empty @@server_uuid")
	}
	assertNoRecordWritten(t, writes, dataDir, nil)
}

// TestNetstatListenerPIDs is the Windows pairing-authority fixture set: the
// parser must match the exact local address (host AND port), never a
// wildcard or a same-port different-address holder, and report every PID it
// finds on the exact address so the ambiguity is visible to the caller.
func TestNetstatListenerPIDs(t *testing.T) {
	// Realistic netstat -aon shape: proto, local, foreign, state, PID.
	fixture := strings.Join([]string{
		"  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1084",
		"  TCP    127.0.0.1:3319         0.0.0.0:0              LISTENING       4242",
		"  TCP    127.0.0.1:3319         [::]:0                 LISTENING       7777",
		"  TCP    192.168.1.5:3319       0.0.0.0:0              LISTENING       9999",
		"  TCP    [::1]:3320             [::]:0                 LISTENING       1200",
		"  TCP    127.0.0.1:3319         10.0.0.1:54321         ESTABLISHED     4242",
	}, "\n")

	cases := []struct {
		name string
		host string
		port int
		want []int
	}{
		{name: "exact holder matches", host: "127.0.0.1", port: 3319, want: []int{4242, 7777}},
		{name: "same port, other address does not authorize", host: "10.9.9.9", port: 3319, want: nil},
		{name: "wildcard listener does not authorize the managed address", host: "127.0.0.1", port: 135, want: nil},
		{name: "bracketed IPv6 exact holder", host: "::1", port: 3320, want: []int{1200}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := netstatListenerPIDs([]byte(fixture), tc.host, tc.port)
			if len(got) != len(tc.want) {
				t.Fatalf("netstatListenerPIDs(%s:%d) = %v, want %v", tc.host, tc.port, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("netstatListenerPIDs(%s:%d) = %v, want %v", tc.host, tc.port, got, tc.want)
				}
			}
		})
	}
}
