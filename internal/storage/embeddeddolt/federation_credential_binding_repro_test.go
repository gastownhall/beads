//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// GH#7890 findings 32/38: federation_peers rows replicate with the database,
// so a peer controlling its own row can copy another row's
// password_encrypted and username into it. The stored ciphertext must be
// bound to the identity of the row it was sealed for: reading the copied
// ciphertext under the attacker's identity must fail authentication and
// release nothing.
func TestFederationCopiedCiphertextIsNotDecryptedForAnotherPeer(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "victim", RemoteURL: "https://victim.example/db",
		Username: "victimuser", Password: "victim-secret",
	}); err != nil {
		t.Fatalf("add victim peer: %v", err)
	}
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "attacker", RemoteURL: "https://attacker.example/db",
	}); err != nil {
		t.Fatalf("add attacker peer: %v", err)
	}

	// The attacker's row replicates into the database and is edited to carry
	// the victim's ciphertext and username verbatim.
	err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
			 SET a.password_encrypted = v.password_encrypted, a.username = v.username
			 WHERE a.name = 'attacker'`)
		return err
	})
	if err != nil {
		t.Fatalf("copy victim ciphertext into attacker row: %v", err)
	}

	peer, err := store.GetFederationPeer(ctx, "attacker")
	if err == nil {
		t.Fatalf("GetFederationPeer returned %+v for the attacker row, want an authentication failure", peer)
	}
}

// A row's own credential still reads back: the binding must reject copies,
// not every stored credential.
func TestFederationPeerOwnCredentialStillDecrypts(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "victim", RemoteURL: "https://victim.example/db",
		Username: "victimuser", Password: "victim-secret",
	}); err != nil {
		t.Fatalf("add victim peer: %v", err)
	}
	peer, err := store.GetFederationPeer(ctx, "victim")
	if err != nil {
		t.Fatalf("GetFederationPeer: %v", err)
	}
	if peer.Password != "victim-secret" {
		t.Fatalf("decrypted password = %q, want %q", peer.Password, "victim-secret")
	}
}