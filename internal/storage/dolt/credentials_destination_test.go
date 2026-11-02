package dolt

import (
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
)

// GH#7890: a stored credential may only be installed for the destination it
// was enrolled for. The server store resolves the peer by name alone, and a
// federation_peers row travels with the database, so a credential-bearing
// row whose live remote points somewhere else — or has no live remote at all
// — must be refused before the operation runs. Mirrors embeddeddolt's
// TestWithPeerAuth_DivergedRemoteURLFailsClosed and _MissingRemoteFailsClosed.
func TestWithPeerCredentialsVerifiesDestination(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}

	prod := &storage.FederationPeer{
		Name:      "team",
		RemoteURL: "https://peer.example/peerdb",
		Username:  "peeruser",
		Password:  "peerpass",
	}

	newStore := func(t *testing.T) (*DoltStore, sqlmock.Sqlmock, []byte) {
		t.Helper()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store := &DoltStore{db: db, credentialKey: key, beadsDir: t.TempDir()}
		blob, err := store.encryptPeerPassword(prod)
		if err != nil {
			t.Fatalf("encryptPeerPassword() error = %v", err)
		}
		return store, mock, blob
	}

	expectPeerRow := func(mock sqlmock.Sqlmock, blob []byte) {
		now := time.Now()
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
			WithArgs("team").
			WillReturnRows(sqlmock.NewRows([]string{
				"name", "remote_url", "username", "password_encrypted",
				"sovereignty", "last_sync", "created_at", "updated_at",
			}).AddRow("team", prod.RemoteURL, prod.Username, blob, "T2", nil, now, now))
	}

	expectLiveURL := func(mock sqlmock.Sqlmock, url string, found bool) {
		rows := sqlmock.NewRows([]string{"name", "url", "params"})
		if found {
			rows = rows.AddRow("team", url, nil)
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, url, params FROM dolt_remotes")).
			WillReturnRows(rows)
	}

	t.Run("matching destination releases the credentials", func(t *testing.T) {
		store, mock, blob := newStore(t)
		expectPeerRow(mock, blob)
		expectLiveURL(mock, prod.RemoteURL, true)

		var got *remoteCredentials
		err := store.withPeerCredentials(t.Context(), "team", func(creds *remoteCredentials) error {
			got = creds
			return nil
		})
		if err != nil {
			t.Fatalf("withPeerCredentials() error = %v, want the credentials released", err)
		}
		if got == nil || got.username != prod.Username || got.password != prod.Password {
			t.Errorf("fn received %+v, want the stored credentials", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("diverged destination refuses before the operation", func(t *testing.T) {
		store, mock, blob := newStore(t)
		expectPeerRow(mock, blob)
		expectLiveURL(mock, "https://attacker.example/peerdb", true)

		called := false
		err := store.withPeerCredentials(t.Context(), "team", func(*remoteCredentials) error {
			called = true
			return nil
		})
		if called {
			t.Error("withPeerCredentials ran the operation, want fail-closed")
		}
		if err == nil {
			t.Fatal("withPeerCredentials succeeded on a diverged destination")
		}
		for _, want := range []string{
			"refusing to use stored credentials for peer team",
			"https://attacker.example/peerdb",
			prod.RemoteURL,
			"diverged",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("missing destination refuses before the operation", func(t *testing.T) {
		store, mock, blob := newStore(t)
		expectPeerRow(mock, blob)
		expectLiveURL(mock, "", false)

		called := false
		err := store.withPeerCredentials(t.Context(), "team", func(*remoteCredentials) error {
			called = true
			return nil
		})
		if called {
			t.Error("withPeerCredentials ran the operation, want fail-closed")
		}
		if err == nil {
			t.Fatal("withPeerCredentials succeeded without a destination")
		}
		if !strings.Contains(err.Error(), "no remote named team exists") {
			t.Errorf("error %q does not name the missing remote", err)
		}
	})

	t.Run("credential-free peer row skips the check", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store := &DoltStore{db: db, beadsDir: t.TempDir()}
		now := time.Now()
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
			WithArgs("open-peer").
			WillReturnRows(sqlmock.NewRows([]string{
				"name", "remote_url", "username", "password_encrypted",
				"sovereignty", "last_sync", "created_at", "updated_at",
			}).AddRow("open-peer", "https://peer.example/peerdb", nil, nil, "T2", nil, now, now))

		called := false
		err = store.withPeerCredentials(t.Context(), "open-peer", func(creds *remoteCredentials) error {
			called = true
			if creds != nil {
				t.Errorf("fn received creds %+v, want nil for a credential-free row", creds)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withPeerCredentials() error = %v, want credential-free passthrough", err)
		}
		if !called {
			t.Error("withPeerCredentials did not run the operation for a credential-free row")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}

// remoteURLMatches accepts only identical nonempty URLs: equivalences are
// not assumed, so a pair that is not byte-identical fails closed (GH#7890).
func TestRemoteURLMatches(t *testing.T) {
	cases := []struct {
		stored, live string
		want         bool
	}{
		{"https://peer.example/db", "https://peer.example/db", true},
		{"https://peer.example/db", "https://peer.example/db/", false},
		{"https://peer.example/db", "https://peer.example/db.git", false},
		{"https://peer.example/db", "https://PEER.example/db", false},
		{"https://peer.example/db", "https://peer.example:443/db", false},
		{"https://peer.example/db", "http://peer.example/db", false},
		{"https://peer.example/db", "https://peer.example/other", false},
		{"", "https://peer.example/db", false},
		{"https://peer.example/db", "", false},
		{"", "", false},
		{"git@host:org/repo", "git@host:org/repo", true},
	}
	for _, tc := range cases {
		if got := remoteURLMatches(tc.stored, tc.live); got != tc.want {
			t.Errorf("remoteURLMatches(%q, %q) = %v, want %v", tc.stored, tc.live, got, tc.want)
		}
	}
}