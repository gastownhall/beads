//go:build cgo

package embeddeddolt

import (
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

// legacySealPassword produces a blob exactly the way the pre-binding codec
// did (GH#7890): nonce + AES-GCM seal with nil associated data and no version
// prefix. It is the planted historical artifact both backends must refuse.
func legacySealPassword(t *testing.T, password string, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("legacy seal: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("legacy seal: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("legacy seal: %v", err)
	}
	return gcm.Seal(nonce, nonce, []byte(password), nil)
}

// GH#7890: a password stored by the pre-binding codec opens for any row that
// carries it, so a legacy blob is refused with the legacy sentinel (wrapped
// in the local-credential classification) on every read and auth path, its
// bytes stay exactly as stored until the operator re-enters the password, and
// re-entry seals a bound v2 blob that works.
func TestLegacyPeerCredentialRefusedUntilReEntry(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "interim-s3cret",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	// Plant the historical artifact: the row's password as the old codec
	// sealed it, under the store's own key.
	legacyBlob := legacySealPassword(t, "old-s3cret", store.credentialKey)
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE federation_peers SET password_encrypted = ? WHERE name = 'team'", legacyBlob)
		return err
	}); err != nil {
		t.Fatalf("plant legacy blob: %v", err)
	}

	t.Run("read refuses with both classifications", func(t *testing.T) {
		_, err := store.GetFederationPeer(ctx, "team")
		if err == nil {
			t.Fatal("GetFederationPeer succeeded on a legacy blob, want refusal")
		}
		if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
		}
		if !errors.Is(err, peercred.ErrLegacyCredential) {
			t.Errorf("error = %v, want errors.Is peercred.ErrLegacyCredential", err)
		}
	})

	t.Run("auth fails closed before install", func(t *testing.T) {
		called := false
		err := store.withPeerAuth(ctx, "team", func(string) error {
			called = true
			return nil
		})
		if called {
			t.Error("withPeerAuth ran the operation, want fail-closed")
		}
		if err == nil || !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Fatalf("withPeerAuth() error = %v, want the mismatch classification", err)
		}
	})

	t.Run("stored bytes stay unchanged, no marker", func(t *testing.T) {
		var stored []byte
		if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx,
				"SELECT password_encrypted FROM federation_peers WHERE name = 'team'").Scan(&stored)
		}); err != nil {
			t.Fatalf("read stored blob: %v", err)
		}
		if string(stored) != string(legacyBlob) {
			t.Fatal("the legacy blob was rewritten by a read, want the bytes untouched")
		}
		marker := filepath.Join(store.beadsDir, ".beads-credential-key.v2-migrated")
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("migration marker %s exists or stat errored differently (%v), want none", marker, err)
		}
	})

	t.Run("explicit re-entry seals a usable v2 credential", func(t *testing.T) {
		if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
			Name: "team", RemoteURL: "https://peer.example/peerdb",
			Username: "peeruser", Password: "new-s3cret",
		}); err != nil {
			t.Fatalf("re-enter password: %v", err)
		}

		var stored []byte
		if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx,
				"SELECT password_encrypted FROM federation_peers WHERE name = 'team'").Scan(&stored)
		}); err != nil {
			t.Fatalf("read re-entered blob: %v", err)
		}
		if len(stored) == 0 || stored[0] != 'v' || !hasV2Prefix(stored) {
			t.Fatal("re-entry did not seal a v2 blob")
		}

		peer, err := store.GetFederationPeer(ctx, "team")
		if err != nil {
			t.Fatalf("GetFederationPeer after re-entry: %v", err)
		}
		if peer.Password != "new-s3cret" {
			t.Fatalf("GetFederationPeer after re-entry = %q, want the re-entered password", peer.Password)
		}
	})
}

func hasV2Prefix(blob []byte) bool {
	return len(blob) >= 3 && blob[0] == 'v' && blob[1] == '2' && blob[2] == ':'
}