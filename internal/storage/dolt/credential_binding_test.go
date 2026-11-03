package dolt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// newCredentialBindingTestStore opens a server-mode store on its own database
// so credential rows from one test never reach another.
func newCredentialBindingTestStore(t *testing.T, suffix string) *DoltStore {
	t.Helper()
	skipIfNoServer(t)

	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dbName := fmt.Sprintf("test_credbind_%s_%d", suffix, testServerPort)

	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	store, err := New(ctx, &Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A peer can author a federation_peers row for its own URL that carries
// another row's ciphertext and username; the row replicates with the
// database. The copy must not decrypt, and no remote operation may receive
// the victim's password (GH#7890).
func TestServerCredentialBindingRejectsCopiedCiphertext(t *testing.T) {
	store := newCredentialBindingTestStore(t, "copy")
	ctx := context.Background()

	for _, p := range []*storage.FederationPeer{
		{Name: "victim", RemoteURL: "file:///tmp/credbind-victim", Username: "victim-user", Password: "victim-secret"},
		{Name: "attacker", RemoteURL: "file:///tmp/credbind-attacker", Username: "attacker-user", Password: "attacker-secret"},
	} {
		if err := store.AddFederationPeer(ctx, p); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", p.Name, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
		SET a.password_encrypted = v.password_encrypted, a.username = v.username
		WHERE a.name = 'attacker'`); err != nil {
		t.Fatalf("copy ciphertext: %v", err)
	}

	got, err := store.GetFederationPeer(ctx, "attacker")
	if err == nil {
		t.Fatalf("GetFederationPeer(attacker) returned password %q, want a credential error", got.Password)
	}
	if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	if strings.Contains(err.Error(), "victim-secret") {
		t.Errorf("error %q leaks the victim's password", err)
	}

	called := false
	err = store.withPeerCredentials(ctx, "attacker", func(*remoteCredentials) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("withPeerCredentials(attacker) = %v, called = %v; want a credential error before the operation runs", err, called)
	}

	victim, err := store.GetFederationPeer(ctx, "victim")
	if err != nil {
		t.Fatalf("GetFederationPeer(victim): %v", err)
	}
	if victim.Password != "victim-secret" {
		t.Errorf("victim password = %q, want %q", victim.Password, "victim-secret")
	}

	// A sovereignty or sync-time change is not an identity change.
	if _, err := store.db.ExecContext(ctx,
		"UPDATE federation_peers SET sovereignty = 'T1', last_sync = CURRENT_TIMESTAMP WHERE name = 'victim'"); err != nil {
		t.Fatalf("update sovereignty: %v", err)
	}
	if victim, err = store.GetFederationPeer(ctx, "victim"); err != nil || victim.Password != "victim-secret" {
		t.Fatalf("GetFederationPeer(victim) after sovereignty change = %v, %v; want the password", victim, err)
	}
	// A URL change is: the stored bytes are the binding.
	if _, err := store.db.ExecContext(ctx,
		"UPDATE federation_peers SET remote_url = 'file:///tmp/credbind-other' WHERE name = 'victim'"); err != nil {
		t.Fatalf("update url: %v", err)
	}
	if _, err := store.GetFederationPeer(ctx, "victim"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("GetFederationPeer(victim) after URL change error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
}
