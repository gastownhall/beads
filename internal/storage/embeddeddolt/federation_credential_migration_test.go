//go:build cgo

package embeddeddolt

import (
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

// sealLegacyPassword encrypts plaintext with AES-GCM under key with NO
// associated data — the pre-v2 federation credential format (GH#7890
// findings 32/38), which bound no peer identity and decrypted for any row.
func sealLegacyPassword(t *testing.T, key []byte, plaintext string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("generate nonce: %v", err)
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil)
}

// storedPeerBlob reads one peer row's raw stored ciphertext, uninterpreted.
func storedPeerBlob(t *testing.T, store *EmbeddedDoltStore, name string) []byte {
	t.Helper()
	var blob []byte
	err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(),
			`SELECT password_encrypted FROM federation_peers WHERE name = ?`, name).Scan(&blob)
	})
	if err != nil {
		t.Fatalf("read stored blob for %s: %v", name, err)
	}
	return blob
}

func storedPeerRowExists(t *testing.T, store *EmbeddedDoltStore, name string) bool {
	t.Helper()
	var n int
	err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM federation_peers WHERE name = ?`, name).Scan(&n)
	})
	if err != nil {
		t.Fatalf("read stored row for %s: %v", name, err)
	}
	return n > 0
}

// The first credential operation on a writable store commits eligible legacy
// credentials as identity-bound v2 and writes the key-bound completion marker
// (AC: GH#7890 credential migration).
func TestFederationLegacyCredentialsMigrateOnOpen(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatalf("ensureCredentialKey: %v", err)
	}

	// A pre-v2 credential: nil-AAD ciphertext under the store's own key,
	// written before this database ever saw a credential migration.
	legacyBlob := sealLegacyPassword(t, store.credentialKey, "legacy-secret")
	err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO federation_peers (name, remote_url, username, password_encrypted, sovereignty)
			VALUES ('legacy', 'https://peer.example/db', 'peeruser', ?, 'T2')
		`, legacyBlob)
		return err
	})
	if err != nil {
		t.Fatalf("insert legacy peer row: %v", err)
	}

	peer, err := store.GetFederationPeer(ctx, "legacy")
	if err != nil {
		t.Fatalf("GetFederationPeer after migration: %v", err)
	}
	if peer.Password != "legacy-secret" {
		t.Fatalf("migrated peer password = %q, want %q", peer.Password, "legacy-secret")
	}

	// The stored blob was re-sealed as v2, bound to this row's identity: it
	// still opens for the row it belongs to and no longer carries the
	// legacy nil-AAD framing.
	blob := storedPeerBlob(t, store, "legacy")
	if !peercred.IsV2(blob) {
		t.Fatalf("stored blob was not re-sealed as v2 (still %d legacy bytes)", len(blob))
	}
	if _, err := peercred.Open(blob, store.credentialKey, peercred.Binding{
		Name: "legacy", URL: "https://peer.example/db", Username: "peeruser",
	}); err != nil {
		t.Fatalf("re-sealed blob does not open under its row identity: %v", err)
	}

	// The completion marker is bound to this machine's key.
	marker, err := os.ReadFile(filepath.Join(store.beadsDir, peercred.MarkerFile))
	if err != nil {
		t.Fatalf("read migration marker: %v", err)
	}
	if !peercred.MarkerMatches(marker, store.credentialKey) {
		t.Fatal("migration marker does not authenticate this machine's credential key")
	}

	// A copy of the pre-migration legacy blob into another row stays legacy
	// (the migration is done), so it is refused rather than re-sealed.
	err = store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO federation_peers (name, remote_url, username, password_encrypted, sovereignty)
			VALUES ('other', 'https://other.example/db', 'otheruser', ?, 'T2')
		`, legacyBlob)
		return err
	})
	if err != nil {
		t.Fatalf("insert copied legacy row: %v", err)
	}
	if _, err := store.GetFederationPeer(ctx, "other"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("GetFederationPeer on a later legacy blob = %v, want ErrCredentialKeyMismatch", err)
	}
	if blob := storedPeerBlob(t, store, "other"); peercred.IsV2(blob) {
		t.Fatal("later legacy blob was re-sealed: the completed migration must reject, not reseal")
	}
}

// Once the migration's completion marker is in place, a legacy credential
// injected afterwards (a row replicated from a peer that has not upgraded) is
// rejected without resealing (AC: GH#7890 credential migration).
func TestFederationLegacyCredentialsRejectedAfterMigrationMarker(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatalf("ensureCredentialKey: %v", err)
	}

	// A credential-free peer row, committed to history, so the migration has
	// a real table state to walk.
	err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO federation_peers (name, remote_url, username, password_encrypted, sovereignty)
			VALUES ('late', 'https://peer.example/db', 'peeruser', NULL, 'T2')
		`)
		return err
	})
	if err != nil {
		t.Fatalf("insert peer row: %v", err)
	}

	// First credential operation on the store: the migration runs, finds
	// nothing to re-seal, and writes the completion marker.
	if _, err := store.ListFederationPeers(ctx); err != nil {
		t.Fatalf("ListFederationPeers: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(store.beadsDir, peercred.MarkerFile))
	if err != nil {
		t.Fatalf("read migration marker: %v", err)
	}
	if !peercred.MarkerMatches(marker, store.credentialKey) {
		t.Fatal("migration marker does not authenticate this machine's credential key")
	}

	// A legacy blob arrives after the marker (replicated row, or a stale
	// writer that never upgraded).
	lateBlob := sealLegacyPassword(t, store.credentialKey, "late-injected-secret")
	err = store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE federation_peers SET password_encrypted = ? WHERE name = 'late'`, lateBlob)
		return err
	})
	if err != nil {
		t.Fatalf("inject legacy blob: %v", err)
	}

	// The read refuses it — classified, not decrypted — and the row is not
	// re-sealed.
	if _, err := store.GetFederationPeer(ctx, "late"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("GetFederationPeer on injected legacy blob = %v, want ErrCredentialKeyMismatch", err)
	}
	if got := storedPeerBlob(t, store, "late"); peercred.IsV2(got) {
		t.Fatal("injected legacy blob was re-sealed: a completed migration must reject, not reseal")
	}
	if !storedPeerRowExists(t, store, "late") {
		t.Fatal("the rejected peer row was deleted")
	}
}
