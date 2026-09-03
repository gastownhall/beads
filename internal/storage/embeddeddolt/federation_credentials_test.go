//go:build cgo

package embeddeddolt

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func newPeerAuthTestStore(t *testing.T) *EmbeddedDoltStore {
	t.Helper()
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := Open(ctx, beadsDir, "fedauth", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// Regression test for GH#5080: credentials stored by add-peer must be the
// ones presented when syncing with that peer, overriding the environment
// pair as a unit; remotes without a stored peer keep the environment
// fallback.
func TestWithPeerAuth(t *testing.T) {
	cases := []struct {
		name       string
		peer       *storage.FederationPeer
		remote     string
		wantUser   string
		wantPwd    string
		wantPwdSet bool
		wantUsrEnv string
		wantUsrSet bool
	}{
		{
			name: "stored peer credentials win",
			peer: &storage.FederationPeer{
				Name:      "team",
				RemoteURL: "https://peer.example/peerdb",
				Username:  "peeruser",
				Password:  "peerpass",
			},
			remote:   "team",
			wantUser: "peeruser",
			wantPwd:  "peerpass", wantPwdSet: true,
			wantUsrEnv: "peeruser", wantUsrSet: true,
		},
		{
			name: "stored empty password does not inherit ambient password",
			peer: &storage.FederationPeer{
				Name:      "open-pwd",
				RemoteURL: "https://peer.example/peerdb",
				Username:  "peeruser",
			},
			remote:   "open-pwd",
			wantUser: "peeruser",
			wantPwd:  "", wantPwdSet: false,
			wantUsrEnv: "peeruser", wantUsrSet: true,
		},
		{
			name: "stored password with empty username does not inherit ambient user",
			peer: &storage.FederationPeer{
				Name:      "open-usr",
				RemoteURL: "https://peer.example/peerdb",
				Password:  "peerpass",
			},
			remote:   "open-usr",
			wantUser: "",
			wantPwd:  "peerpass", wantPwdSet: true,
			wantUsrEnv: "", wantUsrSet: false,
		},
		{
			name:     "unknown remote falls back to env",
			remote:   "not-a-peer",
			wantUser: "envuser",
			wantPwd:  "envpass", wantPwdSet: true,
			wantUsrEnv: "envuser", wantUsrSet: true,
		},
		{
			name: "credential-free peer falls back to env",
			peer: &storage.FederationPeer{
				Name:      "open-peer",
				RemoteURL: "https://peer.example/peerdb",
			},
			remote:   "open-peer",
			wantUser: "envuser",
			wantPwd:  "envpass", wantPwdSet: true,
			wantUsrEnv: "envuser", wantUsrSet: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newPeerAuthTestStore(t)

			t.Setenv("DOLT_REMOTE_USER", "envuser")
			t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

			if tc.peer != nil {
				if err := store.AddFederationPeer(ctx, tc.peer); err != nil {
					t.Fatalf("AddFederationPeer: %v", err)
				}
			}

			var gotUser, gotPwd, gotUsrEnv string
			var gotPwdSet, gotUsrSet bool
			err := store.withPeerAuth(ctx, tc.remote, func(user string) error {
				gotUser = user
				gotPwd, gotPwdSet = os.LookupEnv("DOLT_REMOTE_PASSWORD")
				gotUsrEnv, gotUsrSet = os.LookupEnv("DOLT_REMOTE_USER")
				return nil
			})
			if err != nil {
				t.Fatalf("withPeerAuth: %v", err)
			}
			if gotUser != tc.wantUser {
				t.Errorf("user = %q, want %q", gotUser, tc.wantUser)
			}
			if gotPwdSet != tc.wantPwdSet || gotPwd != tc.wantPwd {
				t.Errorf("DOLT_REMOTE_PASSWORD during fn = %q (set=%v), want %q (set=%v)", gotPwd, gotPwdSet, tc.wantPwd, tc.wantPwdSet)
			}
			if gotUsrSet != tc.wantUsrSet || gotUsrEnv != tc.wantUsrEnv {
				t.Errorf("DOLT_REMOTE_USER during fn = %q (set=%v), want %q (set=%v)", gotUsrEnv, gotUsrSet, tc.wantUsrEnv, tc.wantUsrSet)
			}
			if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "envpass" {
				t.Errorf("DOLT_REMOTE_PASSWORD after fn = %q, want restored %q", got, "envpass")
			}
			if got := os.Getenv("DOLT_REMOTE_USER"); got != "envuser" {
				t.Errorf("DOLT_REMOTE_USER after fn = %q, want restored %q", got, "envuser")
			}
		})
	}
}

func TestWithPeerAuth_RestoresEnvWhenCallbackFails(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	sentinel := errors.New("boom")
	err := store.withPeerAuth(ctx, "team", func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("withPeerAuth error = %v, want sentinel", err)
	}
	if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "envpass" {
		t.Errorf("DOLT_REMOTE_PASSWORD after failed fn = %q, want restored %q", got, "envpass")
	}
}

func TestWithPeerAuth_RestoresAbsentEnv(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	t.Setenv("DOLT_REMOTE_PASSWORD", "scratch")
	_ = os.Unsetenv("DOLT_REMOTE_PASSWORD")
	t.Setenv("DOLT_REMOTE_USER", "scratch")
	_ = os.Unsetenv("DOLT_REMOTE_USER")

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	if err := store.withPeerAuth(ctx, "team", func(string) error { return nil }); err != nil {
		t.Fatalf("withPeerAuth: %v", err)
	}
	if v, set := os.LookupEnv("DOLT_REMOTE_PASSWORD"); set {
		t.Errorf("DOLT_REMOTE_PASSWORD after fn = %q, want unset", v)
	}
	if v, set := os.LookupEnv("DOLT_REMOTE_USER"); set {
		t.Errorf("DOLT_REMOTE_USER after fn = %q, want unset", v)
	}
}

// rotateCredentialKey simulates machine B: the peer row arrived with the
// database, the machine-local key file did not, so this machine holds a key
// that cannot decrypt the stored password.
func rotateCredentialKey(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate replacement key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.beadsDir, credentialKeyFile), key, 0600); err != nil {
		t.Fatalf("write replacement key: %v", err)
	}
	store.credentialKey = nil
}

// wantKeyMismatchClause is the enrichment storage.CredentialKeyMismatchError
// emits, pinned verbatim. The machine-local key file reads as context inside a
// parenthetical rather than as the asserted cause, because an AES-GCM open also
// fails on a tampered blob and on a row still under the legacy key; re-adding
// the peer is the fix in all three cases (GH#5085 review).
const wantKeyMismatchClause = "stored peer credentials cannot be decrypted with this machine's credential key " +
	"(the key file " + credentialKeyFile + " is machine-local and does not replicate with the database); " +
	"re-run 'bd federation add-peer <name> <url> --user <user>' on this machine"

// Pins the decrypt-failure branch (GH#5085 review): a peer row whose password
// was encrypted with a different machine's key must fail with the local context
// and the fix, not a bare cipher error, on every read path.
func TestDecryptPassword_KeyMismatchNamesTheLocalKey(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	rotateCredentialKey(t, store)

	wantFragments := []string{
		// Both read paths name the peer, so the operator knows which one to re-add.
		"for peer team",
		wantKeyMismatchClause,
		// The cipher error stays wrapped so the raw cause is still readable.
		"cipher: message authentication failed",
	}

	t.Run("GetFederationPeer", func(t *testing.T) {
		_, err := store.GetFederationPeer(ctx, "team")
		if err == nil {
			t.Fatal("GetFederationPeer succeeded, want decrypt failure")
		}
		if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
		}
		for _, want := range wantFragments {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("ListFederationPeers", func(t *testing.T) {
		_, err := store.ListFederationPeers(ctx)
		if err == nil {
			t.Fatal("ListFederationPeers succeeded, want decrypt failure")
		}
		if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
		}
		for _, want := range wantFragments {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})
}

// Pins the short-ciphertext branch (GH#5214 review): a stored blob shorter
// than the GCM nonce is local corruption of the credential row, so it must
// classify through the same sentinel as a failed authentication rather than
// surface as a bare cipher error that federation status reads as an
// unreachable peer.
func TestDecryptPassword_ShortCiphertextClassifiesAsKeyMismatch(t *testing.T) {
	store := newPeerAuthTestStore(t)

	_, err := store.decryptPassword([]byte("short"))
	if err == nil {
		t.Fatal("decryptPassword succeeded, want short-ciphertext failure")
	}
	if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	for _, want := range []string{wantKeyMismatchClause, "ciphertext too short"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A remote verb keeps failing closed on an undecryptable password: the
// operation must not run under ambient credentials, which could present the
// wrong identity to the peer.
func TestWithPeerAuth_KeyMismatchFailsClosed(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	t.Setenv("DOLT_REMOTE_USER", "envuser")
	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	rotateCredentialKey(t, store)

	called := false
	err := store.withPeerAuth(ctx, "team", func(string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("withPeerAuth succeeded, want decrypt failure")
	}
	if called {
		t.Error("withPeerAuth ran the operation, want fail-closed")
	}
	if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
	}
	// The message a remote verb prints, wrap included.
	for _, want := range []string{
		"resolve peer credentials:",
		"decrypt password for peer team:",
		wantKeyMismatchClause,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestWithPeerAuth_ConcurrentPeersDoNotMixCredentials(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	peers := map[string]string{"alpha": "alphapass", "beta": "betapass"}
	for name, pwd := range peers {
		if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
			Name: name, RemoteURL: "https://peer.example/" + name,
			Username: name + "-user", Password: pwd,
		}); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", name, err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for range 10 {
		for name, pwd := range peers {
			wg.Add(1)
			go func(name, pwd string) {
				defer wg.Done()
				errs <- store.withPeerAuth(ctx, name, func(user string) error {
					if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != pwd {
						t.Errorf("peer %s observed DOLT_REMOTE_PASSWORD %q, want %q", name, got, pwd)
					}
					if want := name + "-user"; user != want {
						t.Errorf("peer %s got user %q, want %q", name, user, want)
					}
					return nil
				})
			}(name, pwd)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("withPeerAuth: %v", err)
		}
	}
}

// rebindRemote repoints the Dolt remote name at url without touching the
// federation peer row, simulating a same-name remote that no longer matches
// the URL the credentials were stored for (GH#5085 review).
func rebindRemote(t *testing.T, store *EmbeddedDoltStore, name, url string) {
	t.Helper()
	ctx := t.Context()
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "CALL DOLT_REMOTE('remove', ?)", name); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "CALL DOLT_REMOTE('add', ?, ?)", name, url)
		return err
	}); err != nil {
		t.Fatalf("rebind remote %s: %v", name, err)
	}
}

// Stored credentials are bound to the peer's canonical remote URL, not only
// to its name: a same-name remote redirected to another URL must fail closed
// before any credential is installed (GH#5085 review).
func TestWithPeerAuth_DivergedRemoteURLFailsClosed(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	t.Setenv("DOLT_REMOTE_USER", "envuser")
	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	rebindRemote(t, store, "team", "https://attacker.example/peerdb")

	called := false
	err := store.withPeerAuth(ctx, "team", func(string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("withPeerAuth succeeded, want diverged-URL refusal")
	}
	if called {
		t.Error("withPeerAuth ran the operation, want fail-closed")
	}
	for _, want := range []string{"https://attacker.example/peerdb", "https://peer.example/peerdb", "diverged"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "envpass" {
		t.Errorf("DOLT_REMOTE_PASSWORD after refusal = %q, want untouched %q", got, "envpass")
	}
}

// A peer row with stored credentials but no live remote has no verified
// destination to authenticate against, so it fails closed the same way.
func TestWithPeerAuth_MissingRemoteFailsClosed(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CALL DOLT_REMOTE('remove', ?)", "team")
		return err
	}); err != nil {
		t.Fatalf("remove remote: %v", err)
	}

	called := false
	err := store.withPeerAuth(ctx, "team", func(string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("withPeerAuth succeeded, want missing-remote refusal")
	}
	if called {
		t.Error("withPeerAuth ran the operation, want fail-closed")
	}
	if !strings.Contains(err.Error(), "no remote named team") {
		t.Errorf("error %q does not name the missing remote", err)
	}
}

// The environment fallback serializes with stored-peer operations (GH#5085
// review): the in-process Dolt engine reads DOLT_REMOTE_PASSWORD from the
// process environment, so a plain-remote operation running concurrently with
// a stored-peer operation would otherwise observe the peer's temporarily
// installed password.
func TestWithPeerAuth_EnvFallbackDoesNotObservePeerCredentials(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	t.Setenv("DOLT_REMOTE_USER", "envuser")
	t.Setenv("DOLT_REMOTE_PASSWORD", "envpass")

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/peerdb",
		Username: "peeruser", Password: "peerpass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	peerEntered := make(chan struct{})
	releasePeer := make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- store.withPeerAuth(ctx, "team", func(string) error {
			close(peerEntered)
			<-releasePeer
			return nil
		})
	}()
	<-peerEntered

	var gotUser, gotPwd string
	fallbackDone := make(chan error, 1)
	go func() {
		fallbackDone <- store.withPeerAuth(ctx, "not-a-peer", func(user string) error {
			gotUser = user
			gotPwd = os.Getenv("DOLT_REMOTE_PASSWORD")
			return nil
		})
	}()

	select {
	case err := <-fallbackDone:
		t.Fatalf("fallback ran while a stored-peer operation held the credentials (err=%v), want it to wait", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releasePeer)
	if err := <-peerDone; err != nil {
		t.Fatalf("stored-peer withPeerAuth: %v", err)
	}
	if err := <-fallbackDone; err != nil {
		t.Fatalf("fallback withPeerAuth: %v", err)
	}
	if gotUser != "envuser" {
		t.Errorf("fallback user = %q, want ambient %q", gotUser, "envuser")
	}
	if gotPwd != "envpass" {
		t.Errorf("fallback observed DOLT_REMOTE_PASSWORD %q, want ambient %q", gotPwd, "envpass")
	}
}
