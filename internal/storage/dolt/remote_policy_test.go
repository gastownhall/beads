package dolt

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// setAllowedRemotePatternsForTest installs federation.allowed-remote-patterns
// for one test on a freshly initialized config, restoring the process config
// on cleanup.
func setAllowedRemotePatternsForTest(t *testing.T, patterns []string) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.allowed-remote-patterns", patterns)
}

// policyRefusal reports whether err is the allowlist's refusal.
func policyRefusal(err error) bool {
	var policyErr *remotecache.RemotePolicyError
	return errors.As(err, &policyErr)
}

// GH#6233: federation.allowed-remote-patterns was read only by `bd config
// validate`; every covered transfer on the server store dialed the remote
// regardless. Each funnel must refuse an out-of-policy remote before any
// transport runs — no bytes out, no auto-commit, no ref movement.
//
// The peer is registered and seeded BEFORE the list tightens, pinning the
// AC-2 requirement that remotes registered ahead of the lockdown are still
// refused at transfer time.
func TestRemoteTransfersRefusedByAllowedRemotePatterns(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('policy-seed', 'policy-seed', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("seed push to peer remote: %v", err)
	}

	// Tighten after registration: nothing in the list matches the remote.
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})

	// A dirty working set proves the refused pull never reached its
	// auto-commit-before-pull step, which lives beyond the gate.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('policy-late', 'policy-late', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert uncommitted issue: %v", err)
	}

	if err := store.PullRemote(ctx, "peer"); !policyRefusal(err) {
		t.Fatalf("PullRemote error = %v, want *RemotePolicyError", err)
	}
	if _, err := store.PullFrom(ctx, "peer"); !policyRefusal(err) {
		t.Fatalf("PullFrom error = %v, want *RemotePolicyError", err)
	}
	if err := store.Fetch(ctx, "peer"); !policyRefusal(err) {
		t.Fatalf("Fetch error = %v, want *RemotePolicyError", err)
	}

	var dirty int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if dirty == 0 {
		t.Error("working set is clean after a refused pull: the pre-pull auto-commit ran despite the policy refusal")
	}

	// Commit the local row, then attempt to publish it: the refused push must
	// leave the remote without it (clone the remote and look).
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local-only issue')"); err != nil {
		t.Fatalf("commit local-only issue: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); !policyRefusal(err) {
		t.Fatalf("PushTo error = %v, want *RemotePolicyError", err)
	}
	if err := store.PushRemote(ctx, "peer", false); !policyRefusal(err) {
		t.Fatalf("PushRemote error = %v, want *RemotePolicyError", err)
	}

	checkDB := uniqueTestDBName(t)
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, checkDB); err != nil {
		t.Fatalf("clone remote for inspection: %v", err)
	}
	checkConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: checkDB,
	}.String())
	if err != nil {
		t.Fatalf("open check connection: %v", err)
	}
	defer checkConn.Close()
	checkConn.SetMaxOpenConns(1)
	var remoteLate int
	if err := checkConn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = 'policy-late'").Scan(&remoteLate); err != nil {
		t.Fatalf("count remote-local-only issues: %v", err)
	}
	if remoteLate != 0 {
		t.Errorf("remote received %d 'policy-late' row(s): data crossed despite the policy refusal", remoteLate)
	}
}

// The accepting half of GH#6233: a list that matches the remote's normalized
// destination must not change what a transfer does. file:// URLs are native
// (doltremote.Normalize leaves them alone), so the pattern matches the URL
// exactly as registered. The no-list behavior of these same funnels is pinned
// by the federation tests that run without a list; this pins that adding a
// matching entry is the same contract.
func TestRemoteTransfersSucceedUnderMatchingAllowedRemotePatterns(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('policy-ok', 'policy-ok', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	setAllowedRemotePatternsForTest(t, []string{"file:///tmp/" + peerDB + "*"})

	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("PushTo under a matching list: %v", err)
	}
	if err := store.PushRemote(ctx, "peer", false); err != nil {
		t.Fatalf("PushRemote under a matching list: %v", err)
	}
	if err := store.Fetch(ctx, "peer"); err != nil {
		t.Fatalf("Fetch under a matching list: %v", err)
	}
	if _, err := store.PullFrom(ctx, "peer"); err != nil {
		t.Fatalf("PullFrom under a matching list: %v", err)
	}
}

// GH#6233, registration half: an out-of-policy proposed URL must be refused
// before any record is written, and the prior registrations (a federation
// peer, a plain Dolt remote) must survive the refusal untouched.
func TestRemoteRegistrationRefusedByAllowedRemotePatterns(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// Prior records, registered under an empty list.
	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add prior federation peer: %v", err)
	}
	if err := store.AddRemote(ctx, "secondary", remoteURL); err != nil {
		t.Fatalf("add prior dolt remote: %v", err)
	}

	// Tighten after the fact; nothing in the list matches the proposals.
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})

	if err := store.AddRemote(ctx, "evil", "https://evil.example.com/attacker"); !policyRefusal(err) {
		t.Fatalf("AddRemote error = %v, want *RemotePolicyError", err)
	}
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "evilpeer", RemoteURL: "https://evil.example.com/attacker/peer",
	}); !policyRefusal(err) {
		t.Fatalf("AddFederationPeer error = %v, want *RemotePolicyError", err)
	}

	// The prior records survive.
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("list remotes: %v", err)
	}
	got := map[string]string{}
	for _, r := range remotes {
		got[r.Name] = r.URL
	}
	if got["peer"] != remoteURL || got["secondary"] != remoteURL {
		t.Errorf("prior remotes after refusal = %v, want peer and secondary at %s", got, remoteURL)
	}
	if _, ok := got["evil"]; ok {
		t.Errorf("refused remote %q is registered", "evil")
	}
	peer, err := store.GetFederationPeer(ctx, "peer")
	if err != nil || peer.RemoteURL != remoteURL {
		t.Errorf("prior federation peer after refusal = (%+v, %v), want URL %s", peer, err, remoteURL)
	}
	if _, err := store.GetFederationPeer(ctx, "evilpeer"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("refused peer row exists or misreported: %v, want storage.ErrNotFound", err)
	}
	var evilRows int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM federation_peers WHERE name = 'evilpeer'").Scan(&evilRows); err != nil {
		t.Fatalf("count refused peer rows: %v", err)
	}
	if evilRows != 0 {
		t.Errorf("federation_peers holds %d refused peer row(s)", evilRows)
	}
}
