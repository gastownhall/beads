//go:build cgo

package embeddeddolt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/peercred"
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

// legacyUnboundBlob is the pre-v2 format: AES-GCM over the password with no
// identity binding and no version prefix.
func legacyUnboundBlob(t *testing.T, key []byte, password string) []byte {
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
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(nonce, nonce, []byte(password), nil)
}

func storedPeerBlob(t *testing.T, store *EmbeddedDoltStore, name string) []byte {
	t.Helper()
	ctx := t.Context()
	var blob []byte
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT password_encrypted FROM federation_peers WHERE name = ?", name).Scan(&blob)
	}); err != nil {
		t.Fatalf("read password_encrypted for %s: %v", name, err)
	}
	return blob
}

// AC-3: a legacy blob, even one this machine's key opens, cannot prove it
// was stored for the row carrying it. It is rejected with the legacy and
// local-credential classifications, its bytes stay unchanged across reopen
// with no migration marker, and re-entering the password with add-peer works.
func TestFederationPeerLegacyCredentialRejectedUntilReentry(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	t.Setenv("DOLT_REMOTE_USER", "envuser")
	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")
	peer := &storage.FederationPeer{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser", Password: "prod-s3cret"}
	if err := store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	legacy := legacyUnboundBlob(t, store.credentialKey, peer.Password)
	execPeerSQL(t, store, "UPDATE federation_peers SET password_encrypted = ? WHERE name = 'prod'", legacy)

	assertRefused := func(t *testing.T, store *EmbeddedDoltStore) {
		t.Helper()
		_, err := store.GetFederationPeer(ctx, "prod")
		if !errors.Is(err, peercred.ErrLegacyCredential) || !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("GetFederationPeer(legacy) error = %v, want ErrLegacyCredential and ErrCredentialKeyMismatch", err)
		}
		called := false
		err = store.withPeerAuth(ctx, "prod", func(string) error {
			called = true
			return nil
		})
		if err == nil || called {
			t.Errorf("withPeerAuth(legacy) err = %v, called = %v; want a refusal before the operation", err, called)
		}
		if !bytes.Equal(storedPeerBlob(t, store, "prod"), legacy) {
			t.Error("legacy password_encrypted bytes changed, want them untouched until re-entry")
		}
	}
	assertRefused(t, store)

	beadsDir := store.beadsDir
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(ctx, beadsDir, "fedauth", "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	assertRefused(t, reopened)

	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "migrated") {
			t.Errorf("found migration marker %s, want none", e.Name())
		}
	}

	if err := reopened.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("re-entry AddFederationPeer: %v", err)
	}
	var pwd string
	if err := reopened.withPeerAuth(ctx, "prod", func(string) error {
		pwd = os.Getenv("DOLT_REMOTE_PASSWORD")
		return nil
	}); err != nil {
		t.Fatalf("withPeerAuth after re-entry: %v", err)
	}
	if pwd != peer.Password {
		t.Fatalf("password after re-entry = %q, want %q", pwd, peer.Password)
	}
}

// AC-4: credentials install only for the stored destination or the URL
// Dolt's DOLT_REMOTE('add') recorded for it. Each form is added through
// add-peer, so the live URL is Dolt's actual read-back, not an assumption.
func TestWithPeerAuth_DestinationReadBackEquivalents(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	forms := map[string]string{
		"plainhttps": "https://peer.example/db",
		"httpsgit":   "https://peer.example/org/repo.git",
		"sshgit":     "ssh://git@peer.example:2222/org/repo.git",
		"scprel":     "git@peer.example:org/repo.git",
		"scpabs":     "git@peer.example:/srv/repo.git",
		"filegit":    "file://" + t.TempDir() + "/repo.git",
		"native":     "org/repo",
	}
	for name, url := range forms {
		t.Run(name, func(t *testing.T) {
			if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
				Name: name, RemoteURL: url, Username: "peeruser", Password: "peerpass",
			}); err != nil {
				t.Fatalf("AddFederationPeer(%s): %v", url, err)
			}
			var live string
			if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, "SELECT url FROM dolt_remotes WHERE name = ?", name).Scan(&live)
			}); err != nil {
				t.Fatalf("read back remote %s: %v", name, err)
			}
			t.Logf("add-peer %q reads back as %q", url, live)
			var pwd string
			if err := store.withPeerAuth(ctx, name, func(string) error {
				pwd = os.Getenv("DOLT_REMOTE_PASSWORD")
				return nil
			}); err != nil {
				t.Fatalf("withPeerAuth(%s) for %q read back as %q: %v", name, url, live, err)
			}
			if pwd != "peerpass" {
				t.Fatalf("withPeerAuth(%s) password = %q, want the stored one", name, pwd)
			}
		})
	}

	t.Run("live remote moved", func(t *testing.T) {
		execPeerSQL(t, store, "CALL DOLT_REMOTE('remove', 'httpsgit')")
		execPeerSQL(t, store, "CALL DOLT_REMOTE('add', 'httpsgit', 'https://peer.example:8443/org/repo.git')")
		called := false
		err := store.withPeerAuth(ctx, "httpsgit", func(string) error {
			called = true
			return nil
		})
		if err == nil || called {
			t.Fatalf("withPeerAuth(httpsgit) err = %v, called = %v; want a refusal for the moved remote", err, called)
		}
	})
}
