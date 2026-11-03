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
