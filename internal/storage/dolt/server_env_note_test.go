package dolt

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// fakeDoltOnPathFailingTransfers puts a dolt on PATH that appends
// "$(pwd)|args" to the returned log for every call, answers `dolt remote -v`
// with the given lines (each "name url"), and fails push/pull/fetch
// transfers with exit 1 while every other subcommand (version, remote
// add/remove, fsck) succeeds. The transfer failures are what make the CLI
// route's transfer error carry the server-environment note; the fake tests
// bd's diagnostic plumbing, not Dolt credential transport.
func fakeDoltOnPathFailingTransfers(t *testing.T, remoteLines ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "calls.log")
	var listing strings.Builder
	for _, line := range remoteLines {
		listing.WriteString("echo '" + line + "'\n")
	}
	script := "#!/bin/sh\n" +
		"echo \"$(pwd -P)|$*\" >> '" + callLog + "'\n" +
		"if [ \"$1 $2\" = \"remote -v\" ]; then\n" +
		listing.String() +
		"exit 0\n" +
		"fi\n" +
		"case \"$1\" in push|pull|fetch) echo 'fake dolt: transfer failed'; exit 1;; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

// serverEnvPeerStore builds a server-mode store with one federation peer at
// peerURL whose stored credentials (user peeruser) take push/pull/fetch
// through the CLI route: the store's CLI database stands in for one bd's own
// server serves, the SQL remotes table lists the peer, and the peer row holds
// a password this test's key file can decrypt. The SQL route's connection is
// unroutable, so any query that slips through fails at once instead of
// reaching a server.
func serverEnvPeerStore(t *testing.T, peerURL string) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)

	beadsDir := unmanagedWorkspace(t)
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".beads-credential-key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	encPeerSecret, err := encryptWithKey("peer-secret", key)
	if err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	now := time.Now()
	for i := 0; i < 8; i++ {
		mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
			WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("peer", peerURL, nil))
		mock.ExpectQuery("FROM federation_peers WHERE name").
			WillReturnRows(sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted", "sovereignty", "last_sync", "created_at", "updated_at"}).
				AddRow("peer", peerURL, "peeruser", encPeerSecret, "T2", nil, now, now))
	}

	store := &DoltStore{
		serverMode: true,
		readOnly:   true, // skips the auto-commit and recompute around pulls
		beadsDir:   beadsDir,
		dbPath:     filepath.Join(beadsDir, "dolt"),
		database:   "beads",
		remote:     "origin",
		branch:     "main",
		db:         db,
		cfg:        &Config{ServerHost: "127.0.0.1", ServerPort: 3310},
		connStr:    unroutableConnStr,
	}
	serveDoltDatabase(t, store, store.dbPath, 3310)
	if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
		t.Fatal(err)
	}
	return store, mock
}

// assertServerEnvNote checks the one server-environment note a failed
// transfer's error should carry: the transfer's own context is still there,
// exactly one note sentence is present, it names each wanted variable family
// and none of the unwanted ones, and the startup-environment/restart remedy
// follows.
func assertServerEnvNote(t *testing.T, err error, wantContext string, want, unwanted []string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the transfer to fail")
	}
	msg := err.Error()
	if wantContext != "" && !strings.Contains(msg, wantContext) {
		t.Fatalf("error %q should keep its own context %q", msg, wantContext)
	}
	if n := strings.Count(msg, "runs in the Dolt server's process"); n != 1 {
		t.Fatalf("error %q carries %d server-environment notes, want exactly 1", msg, n)
	}
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Fatalf("error %q should name %q", msg, w)
		}
	}
	for _, w := range unwanted {
		if strings.Contains(msg, w) {
			t.Fatalf("error %q should not name %q", msg, w)
		}
	}
	if !strings.Contains(msg, "restart it") {
		t.Fatalf("error %q should end with the startup-environment/restart remedy", msg)
	}
}

// TestPeerTransferFailureNamesServerEnvironment pins the server-environment
// note on federation peer transfers (GH#5953): when a server-mode bd holds
// credentials a peer transfer needs and the transfer fails, the error keeps
// its own context and says, exactly once, that the transfer runs in the Dolt
// server's process and takes those credentials from the server's environment
// — plus the remedy of setting them in the server's startup environment and
// restarting it. The CLI route's served-database handoff means the subprocess
// still executes the transfer in the server's process, so it gets the same
// note as the SQL route.
func TestPeerTransferFailureNamesServerEnvironment(t *testing.T) {
	const hostedURL = "https://doltremoteapi.dolthub.com/org/repo"
	const s3URL = "s3://bucket/beads"
	ctx := context.Background()

	t.Run("push to a credentialed hosted peer", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "peer "+hostedURL)
		store, _ := serverEnvPeerStore(t, hostedURL)

		err := store.PushTo(ctx, "peer")

		assertServerEnvNote(t, err, "push to peer peer failed", []string{
			"; dolt push runs in the Dolt server's process, which takes DOLT_REMOTE_PASSWORD from its own environment, not from bd's",
		}, nil)
		// The note is appended with %w, so the subprocess failure the note
		// explains stays identifiable through it.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("error %q should still unwrap to the dolt subprocess exit error", err)
		}
	})

	t.Run("fetch from a credentialed s3 peer with ambient AWS credentials", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "peer "+s3URL)
		store, _ := serverEnvPeerStore(t, s3URL)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fresh-secret")

		err := store.Fetch(ctx, "peer")

		assertServerEnvNote(t, err, "fetch from peer peer failed", []string{
			"; dolt fetch runs in the Dolt server's process, which takes", "AWS_*",
		}, nil)
	})

	t.Run("sync surfaces the fetch note through its own wrap", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "peer "+s3URL)
		store, _ := serverEnvPeerStore(t, s3URL)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")

		_, err := store.Sync(ctx, "peer", "")

		assertServerEnvNote(t, err, "fetch failed", []string{"AWS_*"}, nil)
	})
}
