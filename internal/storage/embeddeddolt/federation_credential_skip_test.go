//go:build cgo

package embeddeddolt

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/peercred"
)

// catchFederationWarnings redirects the peer-credential diagnostics to a
// buffer for the duration of the test and returns it.
func catchFederationWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := federationWarnWriter
	federationWarnWriter = &buf
	t.Cleanup(func() { federationWarnWriter = prev })
	return &buf
}

func insertPeerRow(t *testing.T, store *EmbeddedDoltStore, name, url, username string, blob []byte) {
	t.Helper()
	err := store.withConn(t.Context(), true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `
			INSERT INTO federation_peers (name, remote_url, username, password_encrypted, sovereignty)
			VALUES (?, ?, ?, ?, 'T2')
		`, name, url, username, blob)
		return err
	})
	if err != nil {
		t.Fatalf("insert peer row %s: %v", name, err)
	}
}

func updatePeerURL(t *testing.T, store *EmbeddedDoltStore, name, url string) {
	t.Helper()
	err := store.withConn(t.Context(), true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(),
			`UPDATE federation_peers SET remote_url = ? WHERE name = ?`, url, name)
		return err
	})
	if err != nil {
		t.Fatalf("update peer row %s: %v", name, err)
	}
}

func assertSkippedPeer(t *testing.T, store *EmbeddedDoltStore, name, password string) {
	t.Helper()
	// The credential is withheld, classified, and never re-sealed.
	if _, err := store.GetFederationPeer(t.Context(), name); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("GetFederationPeer(%q) = %v, want ErrCredentialKeyMismatch", name, err)
	}
	if blob := storedPeerBlob(t, store, name); peercred.IsV2(blob) || string(blob) == password {
		t.Fatalf("skipped peer %s was re-sealed or now stores plaintext-adjacent data", name)
	}
}

// A legacy blob that appears under two peer rows — the replicated-row copy —
// is never rebound: its provenance is ambiguous, and re-sealing it would hand
// the password to whichever row holds it now (GH#7890 findings 32/38). Both
// peers get a safe re-add warning; no password from the blob is released.
func TestFederationLegacyCredentialMigrationSkipsDuplicatedLegacyBlobs(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatalf("ensureCredentialKey: %v", err)
	}
	warnings := catchFederationWarnings(t)

	// One legacy blob, copied into two differently-bound rows.
	dupBlob := sealLegacyPassword(t, store.credentialKey, "shared-secret")
	insertPeerRow(t, store, "victim", "https://victim.example/db", "victimuser", dupBlob)
	insertPeerRow(t, store, "attacker", "https://attacker.example/db", "", dupBlob)

	// Any credential operation triggers the migration; an unrelated peer's
	// read is the one that succeeds afterwards, because the skipped rows stay
	// legacy and every read of them must fail (pinned by
	// TestFederationPeerDecryptKeyMismatchNamesTheLocalKey).
	if _, err := store.GetFederationPeer(ctx, "unrelated"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetFederationPeer(unrelated) = %v, want ErrNotFound", err)
	}

	assertSkippedPeer(t, store, "victim", "shared-secret")
	assertSkippedPeer(t, store, "attacker", "shared-secret")

	// The migration completed despite the skips: it will not run again, so
	// the warning is emitted exactly once per affected peer.
	if _, err := store.GetFederationPeer(ctx, "victim"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("GetFederationPeer after completion = %v, want ErrCredentialKeyMismatch", err)
	}
	marker, err := os.ReadFile(filepath.Join(store.beadsDir, peercred.MarkerFile))
	if err != nil {
		t.Fatalf("read migration marker: %v", err)
	}
	if !peercred.MarkerMatches(marker, store.credentialKey) {
		t.Fatal("migration marker does not authenticate this machine's credential key")
	}

	out := warnings.String()
	if got := strings.Count(out, "Warning: bd will not rebind peer"); got != 2 {
		t.Fatalf("warnings = %d lines, want 2:\n%s", got, out)
	}
	for _, peer := range []string{"victim", "attacker"} {
		if !strings.Contains(out, `"`+peer+`"`) {
			t.Fatalf("warnings do not name peer %s:\n%s", peer, out)
		}
	}
	if !strings.Contains(out, "bd federation add-peer") {
		t.Fatalf("warnings do not suggest the re-add command:\n%s", out)
	}
	if strings.Contains(out, "shared-secret") || strings.Contains(out, "victim.example") {
		t.Fatalf("warnings leak credential-bearing material:\n%s", out)
	}
}

// A legacy blob whose row was edited after the credential was stored — the
// moved blob — is ambiguous through reachable history even though only one
// current row holds it: the pre-change binding proves the ciphertext once
// belonged to a different identity, so it is not rebound (GH#7890).
func TestFederationLegacyCredentialMigrationSkipsMovedLegacyBlob(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)
	if err := store.ensureCredentialKey(); err != nil {
		t.Fatalf("ensureCredentialKey: %v", err)
	}
	warnings := catchFederationWarnings(t)

	// The credential is stored under one URL and committed to history...
	movedBlob := sealLegacyPassword(t, store.credentialKey, "moved-secret")
	insertPeerRow(t, store, "moved", "https://old.example/db", "peeruser", movedBlob)
	if _, err := store.commitAll(ctx, "seed legacy credential", true); err != nil {
		t.Fatalf("commit legacy row: %v", err)
	}
	// ... then the row moves to a different URL, as a hostile or careless
	// replication edit would.
	updatePeerURL(t, store, "moved", "https://new.example/db")

	if _, err := store.GetFederationPeer(ctx, "moved"); !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Fatalf("GetFederationPeer on migrated store = %v, want ErrCredentialKeyMismatch", err)
	}

	assertSkippedPeer(t, store, "moved", "moved-secret")
	if !strings.Contains(warnings.String(), `peer "moved"`) {
		t.Fatalf("warnings do not name the moved peer:\n%s", warnings.String())
	}
	if strings.Contains(warnings.String(), "moved-secret") || strings.Contains(warnings.String(), "old.example") {
		t.Fatalf("warnings leak credential-bearing material:\n%s", warnings.String())
	}
}