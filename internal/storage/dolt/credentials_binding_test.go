package dolt

import (
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
)

// GH#7890: stored peer passwords are bound to the federation_peers row they
// were added for. sqlmock stands in for the server so these run without Dolt.

func bindingTestKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func newBindingTestStore(t *testing.T, key []byte) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db, credentialKey: key, beadsDir: t.TempDir()}, mock
}

// sealForTest is what AddFederationPeer stores for peer.
func sealForTest(t *testing.T, store *DoltStore, peer *storage.FederationPeer) []byte {
	t.Helper()
	blob, err := store.encryptPassword(peerIdentity(peer), peer.Password)
	if err != nil {
		t.Fatalf("encryptPassword() error = %v", err)
	}
	return blob
}

func expectPeerRow(mock sqlmock.Sqlmock, queried string, row *storage.FederationPeer, blob []byte) {
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
		WithArgs(queried).
		WillReturnRows(sqlmock.NewRows([]string{
			"name", "remote_url", "username", "password_encrypted",
			"sovereignty", "last_sync", "created_at", "updated_at",
		}).AddRow(row.Name, row.RemoteURL, row.Username, blob, "T2", nil, now, now))
}

// AC-1: the stored password opens under its key and the row's raw name,
// remote_url and username; changing any of them, or the key, fails.
func TestFederationPeerPasswordBindsToStoredIdentity(t *testing.T) {
	key := bindingTestKey(3)
	prod := &storage.FederationPeer{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser", Password: "prod-s3cret"}
	sealer, _ := newBindingTestStore(t, key)
	blob := sealForTest(t, sealer, prod)

	t.Run("original identity", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		expectPeerRow(mock, "prod", prod, blob)
		got, err := store.GetFederationPeer(t.Context(), "prod")
		if err != nil {
			t.Fatalf("GetFederationPeer() error = %v", err)
		}
		if got.Password != prod.Password {
			t.Fatalf("Password = %q, want %q", got.Password, prod.Password)
		}
	})

	changed := map[string]*storage.FederationPeer{
		"name":       {Name: "prod2", RemoteURL: prod.RemoteURL, Username: prod.Username},
		"remote_url": {Name: prod.Name, RemoteURL: "https://prod.example/db2", Username: prod.Username},
		"username":   {Name: prod.Name, RemoteURL: prod.RemoteURL, Username: "otheruser"},
	}
	for field, row := range changed {
		t.Run("changed "+field, func(t *testing.T) {
			store, mock := newBindingTestStore(t, key)
			expectPeerRow(mock, row.Name, row, blob)
			got, err := store.GetFederationPeer(t.Context(), row.Name)
			if err == nil {
				t.Fatalf("GetFederationPeer() decrypted %q for a row with a changed %s, want a failure", got.Password, field)
			}
			if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
				t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
			}
		})
	}

	t.Run("wrong key", func(t *testing.T) {
		store, mock := newBindingTestStore(t, bindingTestKey(4))
		expectPeerRow(mock, "prod", prod, blob)
		if got, err := store.GetFederationPeer(t.Context(), "prod"); err == nil {
			t.Fatalf("GetFederationPeer() with another key decrypted %q, want a failure", got.Password)
		}
	})
}
