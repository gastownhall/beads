package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestWithPeerCredentialsMissingPeerRow covers the behavior #4837 asked for:
// a peer name with no federation_peers row is a credential-free remote, so
// withPeerCredentials must invoke fn once with nil credentials and return
// fn's result instead of failing the lookup. The stored-credential case is
// kept alongside so the non-regression side stays honest.
func TestWithPeerCredentialsMissingPeerRow(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	baseDir := t.TempDir()
	beadsDir := filepath.Join(baseDir, ".beads")
	dbName := fmt.Sprintf("test_peer_creds_missing_%d", testServerPort)

	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	store, err := New(ctx, &Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	t.Run("no peer row proceeds with nil credentials", func(t *testing.T) {
		calls := 0
		err := store.withPeerCredentials(ctx, "no-such-peer", func(creds *remoteCredentials) error {
			calls++
			if creds != nil {
				t.Errorf("fn received creds %+v, want nil for missing peer row", creds)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withPeerCredentials() error = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("fn invoked %d times, want 1", calls)
		}
	})

	t.Run("no peer row propagates fn error", func(t *testing.T) {
		want := errors.New("remote rejected")
		err := store.withPeerCredentials(ctx, "no-such-peer", func(*remoteCredentials) error {
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("withPeerCredentials() error = %v, want %v", err, want)
		}
	})

	t.Run("stored credentials reach fn", func(t *testing.T) {
		peer := &storage.FederationPeer{
			Name:        "peerwithcreds",
			RemoteURL:   "file:///tmp/nonexistent-peer",
			Username:    "alice",
			Password:    "s3cret",
			Sovereignty: "T2",
		}
		if err := store.AddFederationPeer(ctx, peer); err != nil {
			t.Fatalf("AddFederationPeer() error = %v", err)
		}

		calls := 0
		err := store.withPeerCredentials(ctx, peer.Name, func(creds *remoteCredentials) error {
			calls++
			if creds == nil {
				t.Fatal("fn received nil creds, want stored credentials")
			}
			if creds.username != peer.Username || creds.password != peer.Password {
				t.Errorf("fn received creds %q/%q, want %q/%q", creds.username, creds.password, peer.Username, peer.Password)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withPeerCredentials() error = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("fn invoked %d times, want 1", calls)
		}
	})
}

func TestIsMissingFederationPeer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain not found", storage.ErrNotFound, true},
		{"wrapped not found", fmt.Errorf("%w: federation peer origin", storage.ErrNotFound), true},
		{"double wrap", fmt.Errorf("failed to get peer credentials: %w", fmt.Errorf("%w: federation peer origin", storage.ErrNotFound)), true},
		{"other error", errors.New("decrypt failed"), false},
		{"sql-ish other", fmt.Errorf("failed to get federation peer: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingFederationPeer(tc.err); got != tc.want {
				t.Fatalf("isMissingFederationPeer(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestFederationVerbsWithoutPeerRow is the end-to-end case #4837 reported: a
// Dolt remote registered without a federation_peers row (add-peer without
// --user, bd dolt remote add) must push, fetch and pull through the
// server-mode peer verbs, all of which used to fail at the credential lookup.
// Peer setup mirrors TestPullFromSettlesFKCascadeViolations, except that the
// remote is registered with AddRemote alone.
func TestFederationVerbsWithoutPeerRow(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('rowless-a', 'rowless-a', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddRemote(ctx, "peer", remoteURL); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	assertNoPeerRow := func(when string) {
		t.Helper()
		if _, err := store.GetFederationPeer(ctx, "peer"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("%s: GetFederationPeer() error = %v, want storage.ErrNotFound", when, err)
		}
	}
	assertNoPeerRow("after AddRemote")

	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("PushTo a remote without a peer row: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB); err != nil {
		t.Fatalf("clone peer database: %v", err)
	}

	// The peer adds an issue and pushes it back to the shared remote.
	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	defer peerConn.Close()
	peerConn.SetMaxOpenConns(1)
	for _, q := range []string{
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
			"VALUES ('rowless-b', 'rowless-b', '', '', '', '', 'open', 2, 'task')",
		"CALL DOLT_COMMIT('-Am', 'peer adds issue')",
		"CALL DOLT_PUSH('origin', 'main')",
	} {
		if _, err := peerConn.ExecContext(ctx, q); err != nil {
			t.Fatalf("peer %q: %v", q, err)
		}
	}
	var peerHead string
	if err := peerConn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&peerHead); err != nil {
		t.Fatalf("read peer HEAD: %v", err)
	}

	if err := store.Fetch(ctx, "peer"); err != nil {
		t.Fatalf("Fetch from a remote without a peer row: %v", err)
	}
	var fetched string
	if err := db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_remote_branches WHERE name = 'remotes/peer/main'").Scan(&fetched); err != nil {
		t.Fatalf("read fetched tracking ref: %v", err)
	}
	if fetched != peerHead {
		t.Fatalf("Fetch left remotes/peer/main at %s, want the peer's HEAD %s", fetched, peerHead)
	}

	conflicts, err := store.PullFrom(ctx, "peer")
	if err != nil {
		t.Fatalf("PullFrom a remote without a peer row: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %d", len(conflicts))
	}
	var pulled int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = 'rowless-b'").Scan(&pulled); err != nil {
		t.Fatalf("count pulled issue: %v", err)
	}
	if pulled != 1 {
		t.Fatalf("PullFrom did not merge the peer's issue: %d row(s) for rowless-b, want 1", pulled)
	}

	// Proceeding without a row must not create one.
	assertNoPeerRow("after push, fetch and pull")
}

// seedFederationPeerWithRow commits a seed issue, registers "peer" with
// AddFederationPeer (so a federation_peers row exists) and publishes main to
// its file remote with a raw DOLT_PUSH, which leaves last_sync NULL. It
// returns the remote URL and a fresh database name for a peer clone.
func seedFederationPeerWithRow(t *testing.T, ctx context.Context, store *DoltStore) (remoteURL, peerDB string) {
	t.Helper()
	db := store.db
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('stamp-a', 'stamp-a', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerDB = uniqueTestDBName(t)
	remoteURL = "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('peer', 'main')"); err != nil {
		t.Fatalf("publish to peer remote: %v", err)
	}
	if peerLastSyncSet(t, ctx, store) {
		t.Fatal("seed: federation_peers.last_sync for peer is already set, want NULL")
	}
	return remoteURL, peerDB
}

// peerLastSyncSet reports whether federation_peers.last_sync for "peer" is
// non-NULL. last_sync is a one-second DATETIME, so tests start from NULL
// rather than comparing timestamps.
func peerLastSyncSet(t *testing.T, ctx context.Context, store *DoltStore) bool {
	t.Helper()
	var lastSync sql.NullTime
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_sync FROM federation_peers WHERE name = 'peer'").Scan(&lastSync); err != nil {
		t.Fatalf("read peer last_sync: %v", err)
	}
	return lastSync.Valid
}

// TestFederationSyncNotStompedByPeerLastSync reproduces GH#3547: Sync commits
// pending changes, then fetched through withPeerCredentials, which stamped
// federation_peers.last_sync outside any commit. When the peer's history also
// changed federation_peers, the bare DOLT_MERGE that followed refused with
// "local changes would be stomped by merge: federation_peers" on every sync.
func TestFederationSyncNotStompedByPeerLastSync(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	remoteURL, peerDB := seedFederationPeerWithRow(t, ctx, store)
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB); err != nil {
		t.Fatalf("clone peer database: %v", err)
	}
	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	defer peerConn.Close()
	peerConn.SetMaxOpenConns(1)
	peerExec := func(qs ...string) {
		t.Helper()
		for _, q := range qs {
			if _, err := peerConn.ExecContext(ctx, q); err != nil {
				t.Fatalf("peer %q: %v", q, err)
			}
		}
	}
	countPeerRow := func(name string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM federation_peers WHERE name = ?", name).Scan(&n); err != nil {
			t.Fatalf("count federation_peers row %s: %v", name, err)
		}
		return n
	}

	// A local commit makes the merge a true three-way merge, as in the report.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('stamp-local', 'stamp-local', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("local issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local issue')"); err != nil {
		t.Fatalf("commit local issue: %v", err)
	}

	// The peer changes federation_peers on a different row, so the incoming
	// change cannot conflict with a local last_sync on "peer".
	peerExec(
		"INSERT INTO federation_peers (name, remote_url) VALUES ('peer-of-peer', 'file:///tmp/elsewhere')",
		"CALL DOLT_COMMIT('-Am', 'peer adds a peer')",
		"CALL DOLT_PUSH('origin', 'main')",
	)

	res, err := store.Sync(ctx, "peer", "")
	if err != nil {
		t.Fatalf("Sync with a peer federation_peers change: %v", err)
	}
	if !res.Merged {
		t.Fatalf("Sync result not merged: %+v", res)
	}
	if n := countPeerRow("peer-of-peer"); n != 1 {
		t.Fatalf("Sync did not merge the peer's federation_peers row: %d row(s), want 1", n)
	}
}
