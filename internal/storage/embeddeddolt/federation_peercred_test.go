//go:build cgo

package embeddeddolt

import (
	"database/sql"
	"errors"
	"os"
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

// AC-2: alpha's row, still naming alpha's live remote, is given prod's
// username and ciphertext. Authenticating as alpha must refuse before the
// operation runs, and prod must keep working.
func TestWithPeerAuth_CopiedCredentialRowFailsClosed(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	t.Setenv("DOLT_REMOTE_USER", "envuser")
	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

	for _, p := range []*storage.FederationPeer{
		{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser", Password: "prod-s3cret"},
		{Name: "alpha", RemoteURL: "https://alpha.example/db", Username: "alphauser", Password: "alpha-pass"},
	} {
		if err := store.AddFederationPeer(ctx, p); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", p.Name, err)
		}
	}
	execPeerSQL(t, store, `UPDATE federation_peers AS a JOIN federation_peers AS p ON p.name = 'prod'
		SET a.username = p.username, a.password_encrypted = p.password_encrypted WHERE a.name = 'alpha'`)

	called := false
	err := store.withPeerAuth(ctx, "alpha", func(string) error {
		called = true
		if os.Getenv("DOLT_REMOTE_PASSWORD") == "prod-s3cret" {
			t.Error("withPeerAuth(alpha) installed prod's password for alpha's remote")
		}
		return nil
	})
	if err == nil || called {
		t.Fatalf("withPeerAuth(alpha) err = %v, called = %v; want a refusal before the operation", err, called)
	}
	if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "envpass" {
		t.Errorf("DOLT_REMOTE_PASSWORD after refusal = %q, want untouched %q", got, "envpass")
	}

	var user, pwd string
	if err := store.withPeerAuth(ctx, "prod", func(u string) error {
		user, pwd = u, os.Getenv("DOLT_REMOTE_PASSWORD")
		return nil
	}); err != nil {
		t.Fatalf("withPeerAuth(prod): %v", err)
	}
	if user != "produser" || pwd != "prod-s3cret" {
		t.Fatalf("withPeerAuth(prod) user = %q, password = %q; want prod's", user, pwd)
	}
}
