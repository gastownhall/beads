//go:build cgo

package embeddeddolt

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

func execPinned(t *testing.T, store *EmbeddedDoltStore, stmts ...string) {
	t.Helper()
	ctx := t.Context()
	if err := store.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		for _, q := range stmts {
			if _, err := db.ExecContext(ctx, q); err != nil {
				return errors.New(q + ": " + err.Error())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A legacy blob is unbound, so its provenance is the only evidence of whose
// password it holds. Moving the victim's blob into the attacker's row and
// removing the original leaves it unique in the current table; the history
// still shows it under the victim's binding, so migration must not rebind it.
func TestFederationLegacyCredentialMoveIsNotRebound(t *testing.T) {
	cases := []struct {
		name string
		// sideOnly keeps the victim's row off main's own lineage.
		sideOnly bool
		move     func(t *testing.T, store *EmbeddedDoltStore, victimBlob []byte)
	}{
		{
			name: "copy and delete in one commit",
			move: func(t *testing.T, store *EmbeddedDoltStore, _ []byte) {
				execPinned(t, store,
					`UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
					 SET a.password_encrypted = v.password_encrypted, a.username = v.username
					 WHERE a.name = 'attacker'`,
					`DELETE FROM federation_peers WHERE name = 'victim'`,
					`CALL DOLT_COMMIT('-Am', 'move credential')`,
				)
			},
		},
		{
			name: "copy and overwrite in one commit",
			move: func(t *testing.T, store *EmbeddedDoltStore, _ []byte) {
				execPinned(t, store,
					`UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
					 SET a.password_encrypted = v.password_encrypted, a.username = v.username
					 WHERE a.name = 'attacker'`,
					`UPDATE federation_peers SET password_encrypted = NULL WHERE name = 'victim'`,
					`CALL DOLT_COMMIT('-Am', 'move credential')`,
				)
			},
		},
		{
			// The victim's row only ever existed on a merged branch, so the
			// original binding is reachable only through a merge parent.
			name:     "move arriving through a merge parent",
			sideOnly: true,
			move: func(t *testing.T, store *EmbeddedDoltStore, victimBlob []byte) {
				if err := store.withMutatingPinnedDBConn(t.Context(), func(db versioncontrolops.DBConn) error {
					if _, err := db.ExecContext(t.Context(), `CALL DOLT_CHECKOUT('-b', 'side')`); err != nil {
						return err
					}
					if _, err := db.ExecContext(t.Context(),
						`INSERT INTO federation_peers (name, remote_url, username, password_encrypted) VALUES ('victim', 'https://victim.example/db', 'victim-user', ?)`,
						victimBlob); err != nil {
						return err
					}
					for _, q := range []string{
						`CALL DOLT_COMMIT('-Am', 'add victim on side')`,
						`UPDATE federation_peers AS a JOIN federation_peers AS v ON v.name = 'victim'
						 SET a.password_encrypted = v.password_encrypted, a.username = v.username
						 WHERE a.name = 'attacker'`,
						`DELETE FROM federation_peers WHERE name = 'victim'`,
						`CALL DOLT_COMMIT('-Am', 'move credential on side')`,
						`CALL DOLT_CHECKOUT('main')`,
						`UPDATE federation_peers SET sovereignty = 'T1' WHERE name = 'team'`,
						`CALL DOLT_COMMIT('-Am', 'diverge main')`,
						`CALL DOLT_MERGE('side')`,
					} {
						if _, err := db.ExecContext(t.Context(), q); err != nil {
							return errors.New(q + ": " + err.Error())
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				var parents int
				if err := store.withMutatingPinnedDBConn(t.Context(), func(db versioncontrolops.DBConn) error {
					return db.QueryRowContext(t.Context(),
						`SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = HASHOF('HEAD')`).Scan(&parents)
				}); err != nil {
					t.Fatal(err)
				}
				if parents != 2 {
					t.Fatalf("HEAD has %d parents, want a merge commit", parents)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			peers := []*storage.FederationPeer{
				{Name: "attacker", RemoteURL: "https://attacker.example/db", Username: "attacker-user", Password: "attacker-secret", Sovereignty: "T2"},
				{Name: "team", RemoteURL: "https://team.example/db", Username: "team-user", Password: "team-secret"},
			}
			if !tc.sideOnly {
				peers = append(peers, &storage.FederationPeer{Name: "victim", RemoteURL: "https://victim.example/db", Username: "victim-user", Password: "victim-secret"})
			}
			_, store := legacyPeerStore(t, peers...)
			var victimBlob []byte
			if tc.sideOnly {
				if err := store.ensureCredentialKey(); err != nil {
					t.Fatal(err)
				}
				victimBlob = legacySealForTest(t, store.credentialKey, "victim-secret")
			} else {
				victimBlob = storedBlob(t, store, "victim")
			}
			tc.move(t, store, victimBlob)
			if got := storedBlob(t, store, "attacker"); !bytes.Equal(got, victimBlob) {
				t.Fatal("fixture did not move the victim's blob into the attacker's row")
			}

			var warn bytes.Buffer
			prev := federationWarnWriter
			federationWarnWriter = &warn
			t.Cleanup(func() { federationWarnWriter = prev })

			t.Setenv("DOLT_REMOTE_USER", "envuser")
			t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

			// An honest peer with a single historical binding still upgrades.
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
			if err := store.withPeerAuth(ctx, "attacker", func(string) error {
				called = true
				return nil
			}); err == nil || called {
				t.Fatalf("withPeerAuth(attacker) = %v, called = %v; want a credential error before the operation runs", err, called)
			}
			if blob := storedBlob(t, store, "attacker"); !bytes.Equal(blob, victimBlob) {
				t.Error("the moved legacy blob was rewritten")
			}

			out := warn.String()
			if n := strings.Count(out, "federation peer attacker"); n != 1 {
				t.Errorf("warning for attacker printed %d times, want once:\n%s", n, out)
			}
			for _, want := range []string{"bd federation add-peer attacker <url> --user victim-user --sovereignty T2", "rotate"} {
				if !strings.Contains(out, want) {
					t.Errorf("warning %q does not contain %q", out, want)
				}
			}
			for _, leak := range []string{"victim-secret", "attacker-secret", "attacker.example", "victim.example", "envpass"} {
				if strings.Contains(out, leak) {
					t.Errorf("warning %q leaks %q", out, leak)
				}
			}
		})
	}
}
