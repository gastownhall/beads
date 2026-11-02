package dolt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

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

// GH#7890: a stored password that predates identity binding opens for any
// row that carries it, so a legacy blob is refused with the legacy sentinel
// (wrapped in the local-credential classification) and stays usable only
// after the password is re-entered. The blob's bytes are never rewritten on
// read — there is no migration, marker or otherwise.
func TestFederationPeerLegacyCredentialRefusedUntilReEntry(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, credentialKey: key, beadsDir: t.TempDir()}

	legacyBlob := legacySealPassword(t, "old-s3cret", key)

	peerRows := func() *sqlmock.Rows {
		now := time.Now()
		return sqlmock.NewRows([]string{
			"name", "remote_url", "username", "password_encrypted",
			"sovereignty", "last_sync", "created_at", "updated_at",
		}).AddRow("team", "https://peer.example/peerdb", "peeruser", legacyBlob, "T2", nil, now, now)
	}

	read := func(t *testing.T) error {
		t.Helper()
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
			WithArgs("team").
			WillReturnRows(peerRows())
		_, err := store.GetFederationPeer(t.Context(), "team")
		return err
	}

	t.Run("refused with both classifications", func(t *testing.T) {
		err := read(t)
		if err == nil {
			t.Fatal("GetFederationPeer succeeded on a legacy blob, want refusal")
		}
		if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
		}
		if !errors.Is(err, peercred.ErrLegacyCredential) {
			t.Errorf("error = %v, want errors.Is peercred.ErrLegacyCredential", err)
		}
		if want := "failed to decrypt password for peer team"; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the peer", err)
		}
		// No UPDATE may run: the legacy bytes are left exactly as stored.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("authentication fails closed, no credential installed", func(t *testing.T) {
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
			WithArgs("team").
			WillReturnRows(peerRows())
		called := false
		err := store.withPeerCredentials(t.Context(), "team", func(*remoteCredentials) error {
			called = true
			return nil
		})
		if called {
			t.Error("withPeerCredentials ran the operation, want fail-closed")
		}
		if err == nil || !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Fatalf("withPeerCredentials() error = %v, want the mismatch classification", err)
		}
	})

	t.Run("no migration marker file appears", func(t *testing.T) {
		if err := store.ensureCredentialKey(); err != nil {
			t.Fatalf("ensureCredentialKey: %v", err)
		}
		if err := read(t); err == nil {
			t.Fatal("legacy blob became usable without re-entry")
		}
		marker := filepath.Join(store.beadsDir, ".beads-credential-key.v2-migrated")
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("migration marker %s exists or stat errored differently (%v), want none", marker, err)
		}
	})
}

// GH#7890: generating a fresh key no longer re-encrypts stored passwords. The
// derived-key re-encryption could not prove which peer the enrollment was
// for, so initCredentialKey must not touch federation_peers at all.
func TestInitCredentialKeyDoesNotTouchPeerRows(t *testing.T) {
	beadsDir := t.TempDir()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, beadsDir: beadsDir, dbPath: filepath.Join(beadsDir, "dolt")}

	if err := store.initCredentialKey(); err != nil {
		t.Fatalf("initCredentialKey() error = %v", err)
	}
	// Every database access here would have shown up as an unmet (or
	// unexpected) sqlmock expectation.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("initCredentialKey touched the database: %v", err)
	}
	if len(store.credentialKey) != 32 {
		t.Fatalf("credentialKey length = %d, want 32", len(store.credentialKey))
	}
}