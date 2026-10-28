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

// peerLastSyncIsNull reports whether the named peer's federation_peers row has
// a NULL last_sync. NULL is the precondition the stamp tests start from —
// last_sync is a one-second DATETIME, so a re-stamp within the same second
// could otherwise leave the row unchanged and let a run pass without proving
// anything — and the state a bare Fetch must leave intact (GH#3547).
func peerLastSyncIsNull(t *testing.T, ctx context.Context, db *sql.DB, name string) bool {
	t.Helper()
	var isNull int
	if err := db.QueryRowContext(ctx,
		"SELECT last_sync IS NULL FROM federation_peers WHERE name = ?", name).Scan(&isNull); err != nil {
		t.Fatalf("read last_sync for peer %s: %v", name, err)
	}
	return isNull == 1
}

// TestFederationSyncNotStompedByPeerLastSync reproduces GH#3547: Sync's Fetch
// ran inside withPeerCredentials, which stamped federation_peers.last_sync
// after a successful fetch — an uncommitted write between Sync's pre-merge
// commit and its merge. When the peer's history also changed federation_peers,
// DOLT_MERGE refused with "local changes would be stomped by merge:
// federation_peers" on every sync. The stamp belongs after the transfer: the
// peer row's last_sync is written once the merge has landed, so nothing bd
// writes lands between the commit and the merge.
func TestFederationSyncNotStompedByPeerLastSync(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('stomp-a', 'stomp-a', '', '', '', '', 'open', 2, 'task')"); err != nil {
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
	// Publish with a raw push: PushTo stamps last_sync after a successful
	// transfer (like PullFrom), and a NULL last_sync is the precondition that
	// proves who stamped the row.
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('peer', 'main')"); err != nil {
		t.Fatalf("raw push to peer remote: %v", err)
	}
	if !peerLastSyncIsNull(t, ctx, db, "peer") {
		t.Fatal("precondition: last_sync must be NULL before the first Sync")
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB); err != nil {
		t.Fatalf("clone peer database: %v", err)
	}

	// Commit one more local issue so Sync's merge is a true three-way merge —
	// the reporter's shape, not a fast-forward.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('stomp-b', 'stomp-b', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed divergent local issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local diverges from the peer')"); err != nil {
		t.Fatalf("commit divergent local issue: %v", err)
	}

	// The peer commits a federation_peers row of its own — a DIFFERENT row, so
	// the incoming table change cannot three-way-conflict with a local
	// last_sync stamp on 'peer'.
	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	defer peerConn.Close()
	peerConn.SetMaxOpenConns(1)
	for _, q := range []string{
		"INSERT INTO federation_peers (name, remote_url) VALUES ('peer-of-peer', 'file:///tmp/elsewhere')",
		"CALL DOLT_COMMIT('-Am', 'peer adds its own federation peer')",
		"CALL DOLT_PUSH('origin', 'main')",
	} {
		if _, err := peerConn.ExecContext(ctx, q); err != nil {
			t.Fatalf("peer %q: %v", q, err)
		}
	}

	// Before the fix this fails with: merge failed: merge branch peer/main:
	// error: local changes would be stomped by merge: federation_peers.
	result, err := store.Sync(ctx, "peer", "")
	if err != nil {
		t.Fatalf("Sync should merge the peer's federation_peers change, got: %v", err)
	}
	if !result.Merged {
		t.Fatalf("Sync result not merged: %+v", result)
	}
	var peerRows int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM federation_peers WHERE name = 'peer-of-peer'").Scan(&peerRows); err != nil {
		t.Fatalf("count merged peer rows: %v", err)
	}
	if peerRows != 1 {
		t.Fatalf("Sync merged %d peer-of-peer row(s), want 1", peerRows)
	}

	// The completed sync stamps the peer row's last_sync — after the merge,
	// never between the fetch and it (GH#3547).
	if peerLastSyncIsNull(t, ctx, db, "peer") {
		t.Fatal("Sync completed but did not stamp last_sync")
	}
}

// TestFederationFetchDoesNotStampPeerLastSync covers the fetch half of
// GH#3547: a fetch is not a sync. Fetch — including the bd federation status
// probe, whose reachability check routes through it — must not write the
// versioned federation_peers table, while a completed transfer (PushTo,
// PullFrom) still stamps the peer row's last_sync after it lands.
func TestFederationFetchDoesNotStampPeerLastSync(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('stamp-a', 'stamp-a', '', '', '', '', 'open', 2, 'task')"); err != nil {
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
	// Raw push keeps last_sync NULL: PushTo stamps after a successful transfer.
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('peer', 'main')"); err != nil {
		t.Fatalf("raw push to peer remote: %v", err)
	}
	if !peerLastSyncIsNull(t, ctx, db, "peer") {
		t.Fatal("precondition: last_sync must be NULL before Fetch")
	}

	// A fetch only refreshes the remote-tracking refs; it must not write the
	// versioned federation_peers table (GH#3547).
	if err := store.Fetch(ctx, "peer"); err != nil {
		t.Fatalf("Fetch from peer: %v", err)
	}
	if !peerLastSyncIsNull(t, ctx, db, "peer") {
		t.Fatal("Fetch stamped last_sync; a fetch is not a sync (GH#3547)")
	}
}
