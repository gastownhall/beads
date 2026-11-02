//go:build cgo

package embeddeddolt

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// GH#7890: stored peer passwords are bound to the federation_peers row they
// were added for, so a replicated row cannot borrow another peer's ciphertext.

func execPeerSQL(t *testing.T, store *EmbeddedDoltStore, query string, args ...any) {
	t.Helper()
	ctx := t.Context()
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// AC-1: the password add-peer stored opens under its key and the row's raw
// name, remote_url and username; changing any of them, or the key, fails.
func TestFederationPeerPasswordBindsToStoredIdentity(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "prod", RemoteURL: "https://prod.example/db",
		Username: "produser", Password: "prod-s3cret",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	got, err := store.GetFederationPeer(ctx, "prod")
	if err != nil || got.Password != "prod-s3cret" {
		t.Fatalf("GetFederationPeer(prod) = %+v, %v; want the stored password", got, err)
	}

	changes := []struct {
		field, set, undo, read string
	}{
		{"remote_url", "UPDATE federation_peers SET remote_url = 'https://prod.example/db2' WHERE name = 'prod'",
			"UPDATE federation_peers SET remote_url = 'https://prod.example/db' WHERE name = 'prod'", "prod"},
		{"username", "UPDATE federation_peers SET username = 'otheruser' WHERE name = 'prod'",
			"UPDATE federation_peers SET username = 'produser' WHERE name = 'prod'", "prod"},
		{"null username", "UPDATE federation_peers SET username = NULL WHERE name = 'prod'",
			"UPDATE federation_peers SET username = 'produser' WHERE name = 'prod'", "prod"},
		{"name", "UPDATE federation_peers SET name = 'prod2' WHERE name = 'prod'",
			"UPDATE federation_peers SET name = 'prod' WHERE name = 'prod2'", "prod2"},
	}
	for _, c := range changes {
		t.Run("changed "+c.field, func(t *testing.T) {
			execPeerSQL(t, store, c.set)
			defer execPeerSQL(t, store, c.undo)
			got, err := store.GetFederationPeer(ctx, c.read)
			if err == nil {
				t.Fatalf("GetFederationPeer(%s) decrypted %q after changing %s, want a failure", c.read, got.Password, c.field)
			}
			if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
				t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
			}
		})
	}

	t.Run("wrong key", func(t *testing.T) {
		rotateCredentialKey(t, store)
		if got, err := store.GetFederationPeer(ctx, "prod"); err == nil {
			t.Fatalf("GetFederationPeer(prod) with another key decrypted %q, want a failure", got.Password)
		}
	})
}
