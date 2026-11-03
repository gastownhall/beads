//go:build cgo

package embeddeddolt

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// federation_peers rows replicate with the database, so a peer can author a
// row for its own URL that carries another row's ciphertext and username. The
// copied credential must not decrypt for the attacker's row, and no remote
// operation may run with it (GH#7890).
func TestFederationCredentialBindingRejectsCopiedCiphertext(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	t.Setenv("DOLT_REMOTE_USER", "envuser")
	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

	for _, p := range []*storage.FederationPeer{
		{Name: "victim", RemoteURL: "https://victim.example/db", Username: "victim-user", Password: "victim-secret"},
		{Name: "attacker", RemoteURL: "https://attacker.example/db", Username: "attacker-user", Password: "attacker-secret"},
	} {
		if err := store.AddFederationPeer(ctx, p); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", p.Name, err)
		}
	}

	// The attacker row keeps its own URL, which matches its live remote, so
	// the remote-URL check cannot catch the copy.
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
			SET a.password_encrypted = v.password_encrypted, a.username = v.username
			WHERE a.name = 'attacker'`)
		return err
	}); err != nil {
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
	// The remedy names the row's peer and username but takes a literal <url>:
	// the copied row's URL is exactly the one not to trust or print.
	if !strings.Contains(err.Error(), "re-run `bd federation add-peer attacker <url> --user victim-user` on this machine") {
		t.Errorf("error %q does not name the peer-specific add-peer remedy", err)
	}
	if strings.Contains(err.Error(), "attacker.example") {
		t.Errorf("error %q prints the stored URL", err)
	}

	called := false
	err = store.withPeerAuth(ctx, "attacker", func(string) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("withPeerAuth(attacker) = %v, called = %v; want a credential error before the operation runs", err, called)
	}
	if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("withPeerAuth error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}

	// The victim's own row is untouched and still authenticates.
	victim, err := store.GetFederationPeer(ctx, "victim")
	if err != nil {
		t.Fatalf("GetFederationPeer(victim): %v", err)
	}
	if victim.Password != "victim-secret" {
		t.Errorf("victim password = %q, want %q", victim.Password, "victim-secret")
	}
}

// Each binding field (name, exact stored URL, username) is part of the
// credential's identity; sovereignty and sync timestamps are not.
func TestFederationCredentialBindingCoversEachField(t *testing.T) {
	cases := []struct {
		name    string
		update  string
		peer    string
		wantErr bool
	}{
		{name: "unchanged row", update: "", peer: "team"},
		{name: "sovereignty change", update: "UPDATE federation_peers SET sovereignty = 'T1' WHERE name = 'team'", peer: "team"},
		{name: "last sync change", update: "UPDATE federation_peers SET last_sync = CURRENT_TIMESTAMP WHERE name = 'team'", peer: "team"},
		{name: "url change", update: "UPDATE federation_peers SET remote_url = 'https://other.example/db' WHERE name = 'team'", peer: "team", wantErr: true},
		{name: "url authority case change", update: "UPDATE federation_peers SET remote_url = 'https://PEER.example/db' WHERE name = 'team'", peer: "team", wantErr: true},
		{name: "username change", update: "UPDATE federation_peers SET username = 'other-user' WHERE name = 'team'", peer: "team", wantErr: true},
		{name: "username cleared", update: "UPDATE federation_peers SET username = NULL WHERE name = 'team'", peer: "team", wantErr: true},
		{name: "name change", update: "UPDATE federation_peers SET name = 'other' WHERE name = 'team'", peer: "other", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newPeerAuthTestStore(t)
			if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
				Name: "team", RemoteURL: "https://peer.example/db",
				Username: "peeruser", Password: "peerpass", Sovereignty: "T2",
			}); err != nil {
				t.Fatalf("AddFederationPeer: %v", err)
			}
			if tc.update != "" {
				if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, tc.update)
					return err
				}); err != nil {
					t.Fatalf("update: %v", err)
				}
			}

			got, err := store.GetFederationPeer(ctx, tc.peer)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("GetFederationPeer: %v", err)
				}
				if got.Password != "peerpass" {
					t.Errorf("password = %q, want %q", got.Password, "peerpass")
				}
				return
			}
			if err == nil {
				t.Fatalf("GetFederationPeer returned password %q, want a credential error", got.Password)
			}
			if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
				t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
			}
		})
	}
}
