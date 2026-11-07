package dolt

import (
	"context"
	"crypto/rand"
	"database/sql"
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

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
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

	t.Run("pull from a credentialed s3 peer with ambient AWS credentials", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "peer "+s3URL)
		store, _ := serverEnvPeerStore(t, s3URL)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fresh-secret")

		conflicts, err := store.PullFrom(ctx, "peer")

		if len(conflicts) != 0 {
			t.Fatalf("PullFrom returned conflicts %v, want none", conflicts)
		}
		assertServerEnvNote(t, err, "failed to pull from peer peer", []string{
			"; dolt pull runs in the Dolt server's process, which takes", "AWS_*",
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

// serverEnvDefaultRemoteStore builds a server-mode store whose default
// remote "origin" sits at remoteURL. With withCLIDir, the CLI directory is
// materialized so credential and cloud-auth routing can take push/pull
// through the CLI; without it, every CLI gate is false and the SQL route
// runs, whose unroutable connection makes CALL DOLT_PUSH fail at once.
func serverEnvDefaultRemoteStore(t *testing.T, remoteURL string, withCLIDir bool) *DoltStore {
	t.Helper()
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)

	beadsDir := unmanagedWorkspace(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 8; i++ {
		mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
			WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("origin", remoteURL, nil))
	}

	store := &DoltStore{
		serverMode: true,
		readOnly:   true,
		beadsDir:   beadsDir,
		dbPath:     filepath.Join(beadsDir, "dolt"),
		database:   "beads",
		remote:     "origin",
		branch:     "main",
		db:         db,
		cfg:        &Config{ServerHost: "127.0.0.1", ServerPort: 3310},
		connStr:    unroutableConnStr,
	}
	if withCLIDir {
		// The served database and its .dolt directory are what make the CLI
		// handoff routes available; without them every CLI gate is false and
		// the store stays on the SQL route.
		serveDoltDatabase(t, store, store.dbPath, 3310)
		if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// TestDefaultRemoteTransferFailureNamesServerEnvironment pins the
// server-environment note on default-remote transfers (GH#5953): the SQL
// push's CALL DOLT_PUSH failure carries exactly one note, and the CLI routes
// cloud-auth routing selects carry the same note with the CLI verb named —
// the served-database handoff keeps those transfers in the server's process
// too.
func TestDefaultRemoteTransferFailureNamesServerEnvironment(t *testing.T) {
	const azURL = "az://bucket/beads"
	ctx := context.Background()

	t.Run("SQL push names the server environment once", func(t *testing.T) {
		store := serverEnvDefaultRemoteStore(t, azURL, false) // no CLI dir: every CLI gate is false
		t.Setenv("AZURE_STORAGE_ACCOUNT", "bdenvacct")

		err := store.Push(ctx)

		assertServerEnvNote(t, err, "failed to push to origin/main", []string{
			"; CALL DOLT_PUSH runs in the Dolt server's process, which takes", "AZURE_STORAGE_*",
		}, nil)
	})

	t.Run("cloud-auth CLI push keeps the note", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "origin "+azURL)
		store := serverEnvDefaultRemoteStore(t, azURL, true)
		t.Setenv("AZURE_STORAGE_ACCOUNT", "bdenvacct")

		err := store.Push(ctx)

		assertServerEnvNote(t, err, "dolt push failed", []string{
			"; dolt push runs in the Dolt server's process, which takes", "AZURE_STORAGE_*",
		}, nil)
	})

	t.Run("cloud-auth CLI pull keeps the note", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "origin "+azURL)
		store := serverEnvDefaultRemoteStore(t, azURL, true)
		t.Setenv("AZURE_STORAGE_ACCOUNT", "bdenvacct")

		err := store.Pull(ctx)

		assertServerEnvNote(t, err, "dolt pull failed", []string{
			"; dolt pull runs in the Dolt server's process, which takes", "AZURE_STORAGE_*",
		}, nil)
	})
}

// TestSettleMergeInTxNamesServerEnvironment pins where the SQL pull's
// server-environment note attaches inside the settle machinery (GH#5953): to
// the pull statement's own error wherever settlement surfaces it, and never
// through the conflicts-as-data contract — a declined table's conflicts are
// data for the operator, and MergeErr keeps the pull's own error unannotated
// so PullFrom's conflict reporting is unchanged.
func TestSettleMergeInTxNamesServerEnvironment(t *testing.T) {
	ctx := context.Background()
	const s3URL = "s3://bucket/beads"
	pullFailed := errors.New("error 1105: pull failed mid-transfer")
	plan := serverEnvNotePlan{
		procedure: "CALL DOLT_PULL",
		remote:    "peer",
		env:       []string{"AWS_ACCESS_KEY_ID=AKIAFRESH", "AWS_SECRET_ACCESS_KEY=fresh"},
	}

	// newSettleCase builds a server-mode store around a fresh sqlmock and a
	// begun tx the settle queries run on; the dolt_remotes row names an s3
	// peer so the note names AWS_*.
	newSettleCase := func(t *testing.T) (*DoltStore, sqlmock.Sqlmock, *sql.Tx) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.MatchExpectationsInOrder(false)
		mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
			WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("peer", s3URL, nil))
		store := &DoltStore{serverMode: true, db: db}
		mock.ExpectBegin()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		return store, mock, tx
	}

	t.Run("plain transfer failure carries one note", func(t *testing.T) {
		store, mock, tx := newSettleCase(t)
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"})) // resolver pre-screen
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"})) // declined-set capture
		mock.ExpectQuery("FROM dolt_constraint_violations").WillReturnRows(sqlmock.NewRows([]string{"table", "num_violations"}))
		mock.ExpectRollback()

		err := store.settleMergeInTx(ctx, tx, pullFailed, plan)

		assertServerEnvNote(t, err, "error 1105: pull failed mid-transfer", []string{
			"; CALL DOLT_PULL runs in the Dolt server's process, which takes", "AWS_*",
		}, nil)
	})

	t.Run("operator conflicts stay data with no note", func(t *testing.T) {
		store, mock, tx := newSettleCase(t)
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("roadmaps", 3)) // resolver pre-screen declines roadmaps
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("roadmaps", 3)) // captured before the rollback
		mock.ExpectRollback()

		err := store.settleMergeInTx(ctx, tx, pullFailed, plan)

		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("settle = %v, want MergeConflictsError", err)
		}
		if len(mce.Conflicts) != 1 || mce.Conflicts[0].Field != "roadmaps" || mce.Conflicts[0].Count != 3 {
			t.Fatalf("conflicts = %+v, want the roadmaps row", mce.Conflicts)
		}
		if mce.MergeErr != pullFailed {
			t.Fatalf("MergeErr = %v, want the pull's own error unannotated", mce.MergeErr)
		}
		if strings.Contains(err.Error(), "server's process") {
			t.Fatalf("conflicts-as-data error %q must not carry the credential note", err)
		}
	})
}

// TestFinishPeerPullFailureCarriesNoCredentialNote pins that a peer pull whose
// transfer succeeded but whose post-merge is_blocked recompute failed keeps
// the recompute's error clean (GH#5953): the note explains transfer
// credentials, not derived-state recomputes. The unroutable connStr makes the
// recompute's long-timeout connection fail at once.
func TestFinishPeerPullFailureCarriesNoCredentialNote(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{serverMode: true, readOnly: false, db: db, connStr: unroutableConnStr}

	conflicts, err := store.finishPeerPull(context.Background(), nil, nil, "")

	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if err == nil || !strings.Contains(err.Error(), "pull succeeded but is_blocked recompute failed") {
		t.Fatalf("err = %v, want the recompute failure", err)
	}
	if strings.Contains(err.Error(), "server's process") {
		t.Fatalf("recompute error %q must not carry the credential note", err)
	}
}
