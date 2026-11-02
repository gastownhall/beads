package dolt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/peercred"
)

// GH#7890: stored peer passwords are bound to the federation_peers row they
// were added for. sqlmock stands in for the server so these run without Dolt.

func bindingTestKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func newBindingTestStore(t *testing.T, key []byte) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db, credentialKey: key, beadsDir: t.TempDir()}, mock
}

// sealForTest is what AddFederationPeer stores for peer.
func sealForTest(t *testing.T, store *DoltStore, peer *storage.FederationPeer) []byte {
	t.Helper()
	blob, err := store.encryptPassword(peerIdentity(peer), peer.Password)
	if err != nil {
		t.Fatalf("encryptPassword() error = %v", err)
	}
	return blob
}

func expectPeerRow(mock sqlmock.Sqlmock, queried string, row *storage.FederationPeer, blob []byte) {
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
		WithArgs(queried).
		WillReturnRows(sqlmock.NewRows([]string{
			"name", "remote_url", "username", "password_encrypted",
			"sovereignty", "last_sync", "created_at", "updated_at",
		}).AddRow(row.Name, row.RemoteURL, row.Username, blob, "T2", nil, now, now))
}

// AC-1: the stored password opens under its key and the row's raw name,
// remote_url and username; changing any of them, or the key, fails.
func TestFederationPeerPasswordBindsToStoredIdentity(t *testing.T) {
	key := bindingTestKey(3)
	prod := &storage.FederationPeer{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser", Password: "prod-s3cret"}
	sealer, _ := newBindingTestStore(t, key)
	blob := sealForTest(t, sealer, prod)

	t.Run("original identity", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		expectPeerRow(mock, "prod", prod, blob)
		got, err := store.GetFederationPeer(t.Context(), "prod")
		if err != nil {
			t.Fatalf("GetFederationPeer() error = %v", err)
		}
		if got.Password != prod.Password {
			t.Fatalf("Password = %q, want %q", got.Password, prod.Password)
		}
	})

	changed := map[string]*storage.FederationPeer{
		"name":       {Name: "prod2", RemoteURL: prod.RemoteURL, Username: prod.Username},
		"remote_url": {Name: prod.Name, RemoteURL: "https://prod.example/db2", Username: prod.Username},
		"username":   {Name: prod.Name, RemoteURL: prod.RemoteURL, Username: "otheruser"},
	}
	for field, row := range changed {
		t.Run("changed "+field, func(t *testing.T) {
			store, mock := newBindingTestStore(t, key)
			expectPeerRow(mock, row.Name, row, blob)
			got, err := store.GetFederationPeer(t.Context(), row.Name)
			if err == nil {
				t.Fatalf("GetFederationPeer() decrypted %q for a row with a changed %s, want a failure", got.Password, field)
			}
			if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
				t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
			}
		})
	}

	t.Run("wrong key", func(t *testing.T) {
		store, mock := newBindingTestStore(t, bindingTestKey(4))
		expectPeerRow(mock, "prod", prod, blob)
		if got, err := store.GetFederationPeer(t.Context(), "prod"); err == nil {
			t.Fatalf("GetFederationPeer() with another key decrypted %q, want a failure", got.Password)
		}
	})
}

// AC-2: a replicated row naming another configured remote, carrying prod's
// username and ciphertext, must not release prod's password, and must refuse
// before the operation runs; prod itself keeps working.
func TestWithPeerCredentialsRefusesCopiedCredentialRow(t *testing.T) {
	key := bindingTestKey(5)
	prod := &storage.FederationPeer{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser", Password: "prod-s3cret"}
	sealer, _ := newBindingTestStore(t, key)
	prodBlob := sealForTest(t, sealer, prod)

	t.Run("copied row", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		alpha := &storage.FederationPeer{Name: "alpha", RemoteURL: "https://alpha.example/db", Username: prod.Username}
		expectPeerRow(mock, "alpha", alpha, prodBlob)
		called := false
		err := store.withPeerCredentials(t.Context(), "alpha", func(creds *remoteCredentials) error {
			called = true
			if creds != nil && creds.password == prod.Password {
				t.Errorf("withPeerCredentials(alpha) released prod's password")
			}
			return nil
		})
		if err == nil || called {
			t.Fatalf("withPeerCredentials(alpha) err = %v, called = %v; want a refusal before the operation", err, called)
		}
	})

	t.Run("original peer", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		expectPeerRow(mock, "prod", prod, prodBlob)
		expectLiveRemotes(mock, map[string]string{"prod": prod.RemoteURL})
		var got *remoteCredentials
		if err := store.withPeerCredentials(t.Context(), "prod", func(creds *remoteCredentials) error {
			got = creds
			return nil
		}); err != nil {
			t.Fatalf("withPeerCredentials(prod) error = %v", err)
		}
		if got == nil || got.username != prod.Username || got.password != prod.Password {
			t.Fatalf("withPeerCredentials(prod) creds = %+v, want prod's", got)
		}
	})
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

// AC-3: a legacy blob, even one the current key opens, cannot prove it was
// stored for the row carrying it, so it is rejected with both the legacy and
// the local-credential classification and never reaches an operation.
func TestFederationPeerLegacyCredentialIsRejected(t *testing.T) {
	key := bindingTestKey(6)
	prod := &storage.FederationPeer{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser"}
	legacy := legacyUnboundBlob(t, key, "prod-s3cret")

	assertLegacy := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("legacy credential was accepted, want a refusal")
		}
		if !errors.Is(err, peercred.ErrLegacyCredential) {
			t.Errorf("error = %v, want errors.Is peercred.ErrLegacyCredential", err)
		}
		if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
		}
	}

	t.Run("GetFederationPeer", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		expectPeerRow(mock, "prod", prod, legacy)
		_, err := store.GetFederationPeer(t.Context(), "prod")
		assertLegacy(t, err)
	})

	t.Run("withPeerCredentials", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		expectPeerRow(mock, "prod", prod, legacy)
		called := false
		err := store.withPeerCredentials(t.Context(), "prod", func(*remoteCredentials) error {
			called = true
			return nil
		})
		assertLegacy(t, err)
		if called {
			t.Error("withPeerCredentials ran the operation with a legacy credential, want fail-closed")
		}
	})
}

// AC-3: creating the random key no longer re-encrypts rows sealed under the
// old dbPath-derived key. That migration could not tell which row a blob was
// stored for, so it must leave the bytes alone (no SELECT, no UPDATE) and
// write no migration marker; the rows then fail as legacy until re-entry.
func TestInitCredentialKeyLeavesLegacyRowsUntouched(t *testing.T) {
	beadsDir := t.TempDir()
	dbPath := filepath.Join(beadsDir, "dolt")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, dbPath: dbPath, beadsDir: beadsDir}

	h := sha256.Sum256([]byte(dbPath + "beads-federation-key-v1"))
	derived := legacyUnboundBlob(t, h[:], "prod-s3cret")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, password_encrypted FROM federation_peers")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "password_encrypted"}).AddRow("prod", derived))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE federation_peers SET password_encrypted")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey() error = %v", err)
	}
	if mock.ExpectationsWereMet() == nil {
		t.Fatal("initCredentialKey read and re-encrypted the legacy federation_peers rows; their bytes must stay unchanged until re-entry")
	}
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != credentialKeyFile {
			t.Errorf("initCredentialKey wrote %s; want only %s (no migration marker)", e.Name(), credentialKeyFile)
		}
	}

	reader, readMock := newBindingTestStore(t, store.credentialKey)
	expectPeerRow(readMock, "prod", &storage.FederationPeer{Name: "prod", RemoteURL: "https://prod.example/db"}, derived)
	if _, err := reader.GetFederationPeer(t.Context(), "prod"); !errors.Is(err, peercred.ErrLegacyCredential) {
		t.Fatalf("GetFederationPeer(derived-key legacy row) error = %v, want peercred.ErrLegacyCredential", err)
	}
}

func expectLiveRemotes(mock sqlmock.Sqlmock, remotes map[string]string) {
	rows := sqlmock.NewRows([]string{"name", "url", "params"})
	for name, url := range remotes {
		rows.AddRow(name, url, nil)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, url, params FROM dolt_remotes")).WillReturnRows(rows)
}

// AC-4: before credentials reach the operation, the peer's live remote must
// be the stored destination or Dolt's read-back of it. SQL remotes decide;
// the persisted repo state is consulted only for a name SQL does not list.
func TestWithPeerCredentialsChecksLiveDestination(t *testing.T) {
	key := bindingTestKey(9)
	run := func(t *testing.T, store *DoltStore) (*remoteCredentials, bool, error) {
		t.Helper()
		var got *remoteCredentials
		called := false
		err := store.withPeerCredentials(t.Context(), "alpha", func(creds *remoteCredentials) error {
			called = true
			got = creds
			return nil
		})
		return got, called, err
	}
	peerAt := func(url string) *storage.FederationPeer {
		return &storage.FederationPeer{Name: "alpha", RemoteURL: url, Username: "alphauser", Password: "alpha-pass"}
	}

	accepted := map[string][2]string{
		"identical":           {"https://alpha.example/db", "https://alpha.example/db"},
		"scp read-back":       {"git@alpha.example:org/repo.git", "git+ssh://git@alpha.example/./org/repo.git"},
		"https git read-back": {"https://alpha.example/org/repo.git", "git+https://alpha.example/org/repo.git"},
	}
	for name, urls := range accepted {
		t.Run(name, func(t *testing.T) {
			store, mock := newBindingTestStore(t, key)
			peer := peerAt(urls[0])
			expectPeerRow(mock, "alpha", peer, sealForTest(t, store, peer))
			expectLiveRemotes(mock, map[string]string{"alpha": urls[1]})
			got, called, err := run(t, store)
			if err != nil || !called || got == nil || got.password != peer.Password {
				t.Fatalf("withPeerCredentials(alpha) creds = %+v, called = %v, err = %v; want alpha's credentials", got, called, err)
			}
		})
	}

	refused := map[string]map[string]string{
		"live remote diverged": {"alpha": "https://attacker.example/db"},
		"remote missing":       {"other": "https://alpha.example/db"},
	}
	for name, live := range refused {
		t.Run(name, func(t *testing.T) {
			store, mock := newBindingTestStore(t, key)
			peer := peerAt("https://alpha.example/db")
			expectPeerRow(mock, "alpha", peer, sealForTest(t, store, peer))
			expectLiveRemotes(mock, live)
			if _, called, err := run(t, store); err == nil || called {
				t.Fatalf("withPeerCredentials(alpha) err = %v, called = %v; want a refusal before the operation", err, called)
			}
		})
	}

	t.Run("listing remotes fails", func(t *testing.T) {
		store, mock := newBindingTestStore(t, key)
		peer := peerAt("https://alpha.example/db")
		expectPeerRow(mock, "alpha", peer, sealForTest(t, store, peer))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, url, params FROM dolt_remotes")).WillReturnError(errors.New("boom"))
		if _, called, err := run(t, store); err == nil || called {
			t.Fatalf("withPeerCredentials(alpha) err = %v, called = %v; want a refusal before the operation", err, called)
		}
	})

	persistedStore := func(t *testing.T, persistedURL string) (*DoltStore, sqlmock.Sqlmock) {
		t.Helper()
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		store, mock := newBindingTestStore(t, key)
		store.serverMode = true
		store.dbPath = filepath.Join(store.beadsDir, "dolt")
		store.database = "active"
		store.cfg = &Config{ServerHost: "127.0.0.1", ServerPort: testServedPort}
		if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		state := `{"remotes":{"alpha":{"name":"alpha","url":"` + persistedURL + `"}}}`
		if err := os.WriteFile(filepath.Join(store.CLIDir(), ".dolt", "repo_state.json"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
		standInForServedDatabase(t, store.CLIDir(), testServedPort)
		return store, mock
	}

	t.Run("persisted-only remote", func(t *testing.T) {
		store, mock := persistedStore(t, "https://alpha.example/db")
		peer := peerAt("https://alpha.example/db")
		expectPeerRow(mock, "alpha", peer, sealForTest(t, store, peer))
		expectLiveRemotes(mock, nil)
		if got, called, err := run(t, store); err != nil || !called || got == nil {
			t.Fatalf("withPeerCredentials(alpha) creds = %+v, called = %v, err = %v; want the persisted remote to verify", got, called, err)
		}
	})

	t.Run("persisted-only remote diverged", func(t *testing.T) {
		store, mock := persistedStore(t, "https://attacker.example/db")
		peer := peerAt("https://alpha.example/db")
		expectPeerRow(mock, "alpha", peer, sealForTest(t, store, peer))
		expectLiveRemotes(mock, nil)
		if _, called, err := run(t, store); err == nil || called {
			t.Fatalf("withPeerCredentials(alpha) err = %v, called = %v; want a refusal before the operation", err, called)
		}
	})

	t.Run("SQL remote conflicts with persisted", func(t *testing.T) {
		store, mock := persistedStore(t, "https://alpha.example/db")
		peer := peerAt("https://alpha.example/db")
		expectPeerRow(mock, "alpha", peer, sealForTest(t, store, peer))
		expectLiveRemotes(mock, map[string]string{"alpha": "https://attacker.example/db"})
		if _, called, err := run(t, store); err == nil || called {
			t.Fatalf("withPeerCredentials(alpha) err = %v, called = %v; want the live SQL remote to decide", err, called)
		}
	})
}
