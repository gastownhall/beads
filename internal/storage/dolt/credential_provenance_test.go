package dolt

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// A victim's legacy blob moved into the attacker's row, with the original
// row deleted in the same commit, is unique in the current table. Its history
// still shows the victim's binding, so migration must leave it unusable.
func TestServerLegacyCredentialMoveIsNotRebound(t *testing.T) {
	seed := newCredentialBindingTestStore(t, "provenance")
	ctx := context.Background()

	for _, p := range []*storage.FederationPeer{
		{Name: "victim", RemoteURL: "file:///tmp/credprov-victim", Username: "victim-user", Password: "victim-secret"},
		{Name: "attacker", RemoteURL: "file:///tmp/credprov-attacker", Username: "attacker-user", Password: "attacker-secret"},
		{Name: "team", RemoteURL: "file:///tmp/credprov-team", Username: "team-user", Password: "team-secret"},
	} {
		if err := seed.AddFederationPeer(ctx, p); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", p.Name, err)
		}
	}
	if err := seed.ensureCredentialKey(ctx); err != nil {
		t.Fatal(err)
	}
	victimBlob := legacyServerBlob(t, seed.credentialKey, "victim-secret")
	setServerBlob(t, seed, "victim", victimBlob)
	setServerBlob(t, seed, "attacker", legacyServerBlob(t, seed.credentialKey, "attacker-secret"))
	setServerBlob(t, seed, "team", legacyServerBlob(t, seed.credentialKey, "team-secret"))
	if err := seed.doltAddAndCommit(ctx, []string{"federation_peers"}, "legacy credentials"); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.db.ExecContext(ctx, `
		UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
		SET a.password_encrypted = v.password_encrypted, a.username = v.username
		WHERE a.name = 'attacker'`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.db.ExecContext(ctx, "DELETE FROM federation_peers WHERE name = 'victim'"); err != nil {
		t.Fatal(err)
	}
	if err := seed.doltAddAndCommit(ctx, []string{"federation_peers"}, "move credential"); err != nil {
		t.Fatal(err)
	}
	removeMarker(t, seed)

	store := reopenCredentialTestStore(t, seed)
	var warn bytes.Buffer
	prevWarn := credentialWarnWriter
	credentialWarnWriter = &warn
	t.Cleanup(func() { credentialWarnWriter = prevWarn })

	if got, err := store.GetFederationPeer(ctx, "team"); err != nil || got.Password != "team-secret" {
		t.Fatalf("GetFederationPeer(team) = %v, %v; want the migrated password", got, err)
	}
	got, err := store.GetFederationPeer(ctx, "attacker")
	if err == nil {
		t.Fatalf("GetFederationPeer(attacker) returned password %q, want a credential error", got.Password)
	}
	if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	called := false
	if err := store.withPeerCredentials(ctx, "attacker", func(*remoteCredentials) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatalf("withPeerCredentials(attacker) = %v, called = %v; want a credential error before the operation runs", err, called)
	}
	if blob := serverBlob(t, store, "attacker"); !bytes.Equal(blob, victimBlob) {
		t.Error("the moved legacy blob was rewritten")
	}
	out := warn.String()
	if n := strings.Count(out, "federation peer attacker"); n != 1 {
		t.Errorf("warning for attacker printed %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "bd federation add-peer attacker <url> --user victim-user") {
		t.Errorf("warning %q does not carry the re-add command", out)
	}
	for _, leak := range []string{"victim-secret", "credprov-"} {
		if strings.Contains(out, leak) {
			t.Errorf("warning %q leaks %q", out, leak)
		}
	}
}
