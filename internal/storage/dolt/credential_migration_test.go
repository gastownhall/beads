package dolt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// reopenCredentialTestStore opens a second store on the same database and
// beads directory, as the next bd process after an upgrade does.
func reopenCredentialTestStore(t *testing.T, prev *DoltStore) *DoltStore {
	t.Helper()
	store, err := New(context.Background(), &Config{
		Path:         prev.dbPath,
		BeadsDir:     prev.beadsDir,
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     prev.database,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func serverBlob(t *testing.T, store *DoltStore, peer string) []byte {
	t.Helper()
	var blob []byte
	if err := store.db.QueryRowContext(context.Background(),
		"SELECT password_encrypted FROM federation_peers WHERE name = ?", peer).Scan(&blob); err != nil {
		t.Fatalf("read blob for %s: %v", peer, err)
	}
	return blob
}

func setServerBlob(t *testing.T, store *DoltStore, peer string, blob []byte) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(),
		"UPDATE federation_peers SET password_encrypted = ? WHERE name = ?", blob, peer); err != nil {
		t.Fatalf("write blob for %s: %v", peer, err)
	}
}

func legacyServerBlob(t *testing.T, key []byte, password string) []byte {
	t.Helper()
	blob, err := encryptWithKey(password, key)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// An upgraded server-mode workspace holds legacy credentials sealed under its
// random key and, from before that key existed, under the dbPath-derived key.
// Both are resealed as bound v2 and committed on the first credential
// operation; once the marker records completion a legacy blob is refused and
// left as it is.
func TestServerLegacyCredentialsMigrateOnce(t *testing.T) {
	seed := newCredentialBindingTestStore(t, "migrate")
	ctx := context.Background()

	for _, p := range []*storage.FederationPeer{
		{Name: "team", RemoteURL: "file:///tmp/credmig-team", Username: "team-user", Password: "team-secret"},
		{Name: "derived", RemoteURL: "file:///tmp/credmig-derived", Username: "derived-user", Password: "derived-secret"},
		{Name: "other", RemoteURL: "file:///tmp/credmig-other", Username: "other-user", Password: "other-secret"},
	} {
		if err := seed.AddFederationPeer(ctx, p); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", p.Name, err)
		}
	}
	if err := seed.ensureCredentialKey(ctx); err != nil {
		t.Fatal(err)
	}
	setServerBlob(t, seed, "team", legacyServerBlob(t, seed.credentialKey, "team-secret"))
	setServerBlob(t, seed, "derived", legacyServerBlob(t, seed.legacyEncryptionKey(), "derived-secret"))
	setServerBlob(t, seed, "other", legacyServerBlob(t, seed.credentialKey, "other-secret"))
	if err := seed.doltAddAndCommit(ctx, []string{"federation_peers"}, "legacy credentials"); err != nil {
		t.Fatalf("commit legacy rows: %v", err)
	}
	markerPath := filepath.Join(seed.beadsDir, credentialKeyFile+".v2-migrated")
	_ = os.Remove(markerPath)

	store := reopenCredentialTestStore(t, seed)
	for peer, want := range map[string]string{"team": "team-secret", "derived": "derived-secret"} {
		got, err := store.GetFederationPeer(ctx, peer)
		if err != nil {
			t.Fatalf("GetFederationPeer(%s) after upgrade: %v", peer, err)
		}
		if got.Password != want {
			t.Errorf("%s password = %q, want %q", peer, got.Password, want)
		}
	}
	for _, peer := range []string{"team", "derived", "other"} {
		if blob := serverBlob(t, store, peer); !bytes.HasPrefix(blob, []byte("v2:")) {
			t.Errorf("peer %s blob was not resealed as v2", peer)
		}
	}
	var dirty int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'federation_peers'").Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Error("migrated federation_peers rows are not committed")
	}
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("migration marker: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("marker mode = %o, want 600", mode)
	}

	injected := legacyServerBlob(t, store.credentialKey, "injected")
	setServerBlob(t, store, "other", injected)
	if err := store.doltAddAndCommit(ctx, []string{"federation_peers"}, "inject legacy credential"); err != nil {
		t.Fatal(err)
	}
	later := reopenCredentialTestStore(t, store)
	if _, err := later.GetFederationPeer(ctx, "other"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("GetFederationPeer(other) with injected legacy blob error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	if blob := serverBlob(t, later, "other"); !bytes.Equal(blob, injected) {
		t.Errorf("injected legacy blob was rewritten after migration completed (%d bytes)", len(blob))
	}
}
