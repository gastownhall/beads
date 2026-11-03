package dolt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// reopenReadOnlyCredentialTestStore is reopenCredentialTestStore for a
// read-only command.
func reopenReadOnlyCredentialTestStore(t *testing.T, prev *DoltStore) *DoltStore {
	t.Helper()
	store, err := New(context.Background(), &Config{
		Path:         prev.dbPath,
		BeadsDir:     prev.beadsDir,
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     prev.database,
		MaxOpenConns: 1,
		ReadOnly:     true,
	})
	if err != nil {
		t.Fatalf("reopen read-only: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// removeMarker drops the completion marker the seed store wrote while adding
// its peers: the upgrade replaces the binary, not the store.
func removeMarker(t *testing.T, store *DoltStore) {
	t.Helper()
	if err := os.Remove(filepath.Join(store.beadsDir, credentialKeyFile+".v2-migrated")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
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
	removeMarker(t, seed)
	markerPath := filepath.Join(seed.beadsDir, credentialKeyFile+".v2-migrated")

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

// A read-only store does not upgrade a legacy credential, and its error names
// the remedy: bd federation status without --readonly. That command fetches
// from each peer, and the fetch's credential read upgrades the row, after
// which the read-only store reads the password.
func TestServerReadOnlyLegacyCredentialRemedyUpgradesIt(t *testing.T) {
	seed := newCredentialBindingTestStore(t, "rofix")
	ctx := context.Background()

	// A peer directory with no database: the fetch fails after the upgrade.
	peer := &storage.FederationPeer{Name: "team", RemoteURL: "file://" + t.TempDir(), Username: "team-user", Password: "team-secret"}
	if err := seed.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	if err := seed.ensureCredentialKey(ctx); err != nil {
		t.Fatal(err)
	}
	setServerBlob(t, seed, "team", legacyServerBlob(t, seed.credentialKey, "team-secret"))
	if err := seed.doltAddAndCommit(ctx, []string{"federation_peers"}, "legacy credential"); err != nil {
		t.Fatalf("commit legacy row: %v", err)
	}
	removeMarker(t, seed)

	readOnly := reopenReadOnlyCredentialTestStore(t, seed)
	_, err := readOnly.GetFederationPeer(ctx, "team")
	if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("read-only GetFederationPeer error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	if !strings.Contains(err.Error(), "run bd federation status without --readonly") {
		t.Fatalf("read-only error does not name the remedy: %v", err)
	}

	// What bd federation status runs for the peer on a writable store.
	_ = reopenCredentialTestStore(t, seed).Fetch(ctx, "team")

	got, err := readOnly.GetFederationPeer(ctx, "team")
	if err != nil {
		t.Fatalf("read-only GetFederationPeer after the remedy: %v", err)
	}
	if got.Password != "team-secret" {
		t.Errorf("password = %q, want %q", got.Password, "team-secret")
	}
}

// The upgrade runs on its own connection, which starts on the database's
// default branch, yet it must rewrite the branch the store has checked out:
// later credential reads open that branch's rows. The default branch keeps
// its legacy row.
func TestServerLegacyCredentialMigrationRewritesTheStoresBranch(t *testing.T) {
	seed := newCredentialBindingTestStore(t, "branchmig")
	ctx := context.Background()

	peer := &storage.FederationPeer{Name: "team", RemoteURL: "file:///tmp/credmig-branch", Username: "team-user", Password: "team-secret"}
	if err := seed.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	if err := seed.ensureCredentialKey(ctx); err != nil {
		t.Fatal(err)
	}
	legacy := legacyServerBlob(t, seed.credentialKey, "team-secret")
	setServerBlob(t, seed, "team", legacy)
	if err := seed.doltAddAndCommit(ctx, []string{"federation_peers"}, "legacy credential"); err != nil {
		t.Fatalf("commit legacy row: %v", err)
	}
	if err := seed.Branch(ctx, "side"); err != nil {
		t.Fatalf("Branch: %v", err)
	}
	removeMarker(t, seed)

	store := reopenCredentialTestStore(t, seed)
	if err := store.Checkout(ctx, "side"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	got, err := store.GetFederationPeer(ctx, "team")
	if err != nil {
		t.Fatalf("GetFederationPeer on the checked-out branch: %v", err)
	}
	if got.Password != "team-secret" {
		t.Errorf("password = %q, want %q", got.Password, "team-secret")
	}
	if blob := serverBlob(t, store, "team"); !bytes.HasPrefix(blob, []byte("v2:")) {
		t.Error("the checked-out branch's row was not resealed as v2")
	}
	var mainBlob []byte
	if err := store.db.QueryRowContext(ctx,
		"SELECT password_encrypted FROM `"+store.database+"/main`.federation_peers WHERE name = ?", "team").Scan(&mainBlob); err != nil {
		t.Fatalf("read the default branch's row: %v", err)
	}
	if !bytes.Equal(mainBlob, legacy) {
		t.Error("the upgrade rewrote the default branch's row")
	}
}
