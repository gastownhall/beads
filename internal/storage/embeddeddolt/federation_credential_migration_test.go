//go:build cgo

package embeddeddolt

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/peercred"
)

// legacySealForTest writes the pre-v2 unbound format: nonce || AES-GCM with
// no associated data, as every release before the binding fix stored it.
func legacySealForTest(t *testing.T, key []byte, password string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(nonce, nonce, []byte(password), nil)
}

func storedBlob(t *testing.T, store *EmbeddedDoltStore, peer string) []byte {
	t.Helper()
	var blob []byte
	if err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT password_encrypted FROM federation_peers WHERE name = ?", peer).Scan(&blob)
	}); err != nil {
		t.Fatalf("read blob for %s: %v", peer, err)
	}
	return blob
}

func setBlob(t *testing.T, store *EmbeddedDoltStore, peer string, blob []byte) {
	t.Helper()
	if err := store.withConn(t.Context(), true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE federation_peers SET password_encrypted = ? WHERE name = ?", blob, peer)
		return err
	}); err != nil {
		t.Fatalf("write blob for %s: %v", peer, err)
	}
}

// legacyPeerStore returns a store whose peers hold committed legacy
// credentials under the store's own key, as an upgraded workspace does, and
// a fresh store on the same database that has not yet migrated.
func legacyPeerStore(t *testing.T, peers ...*storage.FederationPeer) (beadsDir string, store *EmbeddedDoltStore) {
	t.Helper()
	ctx := t.Context()
	seed := newPeerAuthTestStore(t)
	for _, p := range peers {
		if err := seed.AddFederationPeer(ctx, p); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", p.Name, err)
		}
	}
	if err := seed.ensureCredentialKey(); err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		setBlob(t, seed, p.Name, legacySealForTest(t, seed.credentialKey, p.Password))
	}
	if _, err := seed.CommitAll(ctx, "legacy credentials"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	beadsDir = seed.beadsDir
	// Upgrading replaces the binary, not the store: drop any marker the seed
	// store may have written while adding the peers.
	_ = os.Remove(filepath.Join(beadsDir, credentialKeyFile+".v2-migrated"))
	seed.Close()

	store, err := Open(ctx, beadsDir, "fedauth", "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return beadsDir, store
}

func federationPeersDirty(t *testing.T, store *EmbeddedDoltStore) bool {
	t.Helper()
	var n int
	if err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'federation_peers'").Scan(&n)
	}); err != nil {
		t.Fatalf("dolt_status: %v", err)
	}
	return n > 0
}

// An upgraded workspace's legacy credentials are resealed as bound v2 on the
// first credential operation, committed, and recorded by a key-bound marker;
// after that, a legacy credential written into the table is refused rather
// than resealed.
func TestFederationLegacyCredentialsMigrateOnce(t *testing.T) {
	ctx := context.Background()
	beadsDir, store := legacyPeerStore(t,
		&storage.FederationPeer{Name: "team", RemoteURL: "https://peer.example/db", Username: "peeruser", Password: "peerpass"},
		&storage.FederationPeer{Name: "other", RemoteURL: "https://other.example/db", Username: "otheruser", Password: "otherpass"},
	)

	got, err := store.GetFederationPeer(ctx, "team")
	if err != nil {
		t.Fatalf("GetFederationPeer after upgrade: %v", err)
	}
	if got.Password != "peerpass" {
		t.Errorf("password = %q, want %q", got.Password, "peerpass")
	}
	for _, peer := range []string{"team", "other"} {
		if blob := storedBlob(t, store, peer); !bytes.HasPrefix(blob, []byte("v2:")) {
			t.Errorf("peer %s blob was not resealed as v2", peer)
		}
	}
	if federationPeersDirty(t, store) {
		t.Error("migrated federation_peers rows are not committed")
	}
	markerPath := filepath.Join(beadsDir, credentialKeyFile+".v2-migrated")
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("migration marker: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("marker mode = %o, want 600", mode)
	}

	// A legacy credential arriving after completion (a pull, a direct write)
	// is never rebound, by this store or by a later process.
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatal(err)
	}
	injected := legacySealForTest(t, store.credentialKey, "injected")
	setBlob(t, store, "other", injected)
	if _, err := store.CommitAll(ctx, "inject legacy credential"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	store.Close()
	later, err := Open(ctx, beadsDir, "fedauth", "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer later.Close()

	if _, err := later.GetFederationPeer(ctx, "other"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("GetFederationPeer(other) with injected legacy blob error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	if blob := storedBlob(t, later, "other"); !bytes.Equal(blob, injected) {
		t.Error("injected legacy blob was rewritten after migration completed")
	}
	if got, err := later.GetFederationPeer(ctx, "team"); err != nil || got.Password != "peerpass" {
		t.Errorf("GetFederationPeer(team) = %v, %v; want the migrated password", got, err)
	}
}

func markerValidForStore(t *testing.T, store *EmbeddedDoltStore) bool {
	t.Helper()
	ok, err := peercred.MarkerValid(filepath.Join(store.beadsDir, credentialKeyFile), store.credentialKey)
	if err != nil {
		t.Fatalf("MarkerValid: %v", err)
	}
	return ok
}

// The first credential operation establishes completion even when no row
// holds a credential and no key exists yet, so a legacy credential that
// arrives afterwards is rejected rather than resealed (GH#7890).
func TestFederationLegacyCredentialsRejectedAfterMigrationMarker(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)

	// A credential-free peer row: the migration scans it and finds nothing.
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO federation_peers (name, remote_url, username, password_encrypted, sovereignty)
			VALUES ('late', 'https://peer.example/db', 'peeruser', NULL, 'T2')`)
		return err
	}); err != nil {
		t.Fatalf("insert peer row: %v", err)
	}

	if _, err := store.ListFederationPeers(ctx); err != nil {
		t.Fatalf("ListFederationPeers: %v", err)
	}
	if store.credentialKey == nil {
		t.Fatal("the first credential operation did not establish a credential key")
	}
	if !markerValidForStore(t, store) {
		t.Fatal("an empty scan did not write a completion marker for this machine's key")
	}

	injected := legacySealForTest(t, store.credentialKey, "injected")
	setBlob(t, store, "late", injected)
	if _, err := store.GetFederationPeer(ctx, "late"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("GetFederationPeer on a later legacy blob = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	if blob := storedBlob(t, store, "late"); !bytes.Equal(blob, injected) {
		t.Fatal("a legacy blob arriving after completion was resealed")
	}
}

// A pass that resealed rows and then failed to commit them leaves v2 rows in
// the working set and no marker. The next process reseals nothing, yet it
// must commit those rows before it records completion.
func TestFederationCredentialMigrationCommitsAnEarlierPassesRebinds(t *testing.T) {
	ctx := context.Background()
	beadsDir, store := legacyPeerStore(t,
		&storage.FederationPeer{Name: "team", RemoteURL: "https://peer.example/db", Username: "peeruser", Password: "peerpass"},
	)
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatal(err)
	}
	sealed, err := peercred.Seal(store.credentialKey,
		peercred.Binding{Name: "team", URL: "https://peer.example/db", Username: "peeruser"}, "peerpass")
	if err != nil {
		t.Fatal(err)
	}
	setBlob(t, store, "team", sealed)
	if !federationPeersDirty(t, store) {
		t.Fatal("setup: the resealed row is not pending")
	}
	store.Close()

	reopened, err := Open(ctx, beadsDir, "fedauth", "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.GetFederationPeer(ctx, "team")
	if err != nil || got.Password != "peerpass" {
		t.Fatalf("GetFederationPeer = %v, %v; want the resealed password", got, err)
	}
	if federationPeersDirty(t, reopened) {
		t.Error("the earlier pass's resealed rows are still uncommitted after completion")
	}
	if !markerValidForStore(t, reopened) {
		t.Error("completion marker missing after the pending rows were committed")
	}
}

// A marker the store cannot read fails the operation closed: nothing is
// rebound and nothing is recorded, and the next operation, once the marker
// path is readable, completes the migration.
func TestFederationCredentialMigrationMarkerFailureIsRetried(t *testing.T) {
	ctx := context.Background()
	beadsDir, store := legacyPeerStore(t,
		&storage.FederationPeer{Name: "team", RemoteURL: "https://peer.example/db", Username: "peeruser", Password: "peerpass"},
	)
	before := storedBlob(t, store, "team")
	// A non-empty directory at the marker path can be neither read nor
	// replaced.
	markerPath := filepath.Join(beadsDir, credentialKeyFile+".v2-migrated")
	if err := os.MkdirAll(filepath.Join(markerPath, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetFederationPeer(ctx, "team"); err == nil {
		t.Fatal("GetFederationPeer succeeded although the completion marker could not be read")
	}
	if blob := storedBlob(t, store, "team"); !bytes.Equal(blob, before) {
		t.Error("a row was rebound although completion could not be checked")
	}

	if err := os.RemoveAll(markerPath); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetFederationPeer(ctx, "team")
	if err != nil || got.Password != "peerpass" {
		t.Fatalf("GetFederationPeer after the marker path cleared = %v, %v; want the migrated password", got, err)
	}
	if federationPeersDirty(t, store) {
		t.Error("the retried migration left its rows uncommitted")
	}
	if !markerValidForStore(t, store) {
		t.Error("the retried migration did not write its marker")
	}
}

// A marker written under another key is not completion evidence for this
// machine's key, so the store still migrates its legacy rows.
func TestFederationCredentialMigrationIgnoresAnotherKeysMarker(t *testing.T) {
	ctx := context.Background()
	beadsDir, store := legacyPeerStore(t,
		&storage.FederationPeer{Name: "team", RemoteURL: "https://peer.example/db", Username: "peeruser", Password: "peerpass"},
	)
	otherKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, otherKey); err != nil {
		t.Fatal(err)
	}
	if err := peercred.WriteMarker(filepath.Join(beadsDir, credentialKeyFile), otherKey); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetFederationPeer(ctx, "team")
	if err != nil || got.Password != "peerpass" {
		t.Fatalf("GetFederationPeer = %v, %v; want the migrated password", got, err)
	}
	if blob := storedBlob(t, store, "team"); !bytes.HasPrefix(blob, []byte("v2:")) {
		t.Error("legacy row was not resealed under a foreign key's marker")
	}
	if !markerValidForStore(t, store) {
		t.Error("the marker was not rewritten for this machine's key")
	}
}

// A legacy blob's random nonce can begin with the v2 prefix bytes. Framing
// alone does not classify it: the migration still opens and reseals it.
func TestFederationCredentialMigrationResealsLegacyBlobWithV2PrefixedNonce(t *testing.T) {
	ctx := context.Background()
	_, store := legacyPeerStore(t,
		&storage.FederationPeer{Name: "team", RemoteURL: "https://peer.example/db", Username: "peeruser", Password: "peerpass"},
	)
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(store.credentialKey)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	copy(nonce, "v2:")
	legacy := gcm.Seal(nonce, nonce, []byte("peerpass"), nil)
	setBlob(t, store, "team", legacy)
	if _, err := store.CommitAll(ctx, "legacy credential with a v2-looking nonce"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	got, err := store.GetFederationPeer(ctx, "team")
	if err != nil || got.Password != "peerpass" {
		t.Fatalf("GetFederationPeer = %v, %v; want the migrated password", got, err)
	}
	if blob := storedBlob(t, store, "team"); bytes.Equal(blob, legacy) {
		t.Error("the v2-prefixed legacy blob was left in place instead of resealed")
	}
}
