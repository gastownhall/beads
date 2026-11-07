package dolt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
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
// peerURL whose stored credentials (user peeruser) are decryptable. With
// withCLIDir, the store's CLI database stands in for one bd's own server
// serves and the stored credentials take push/pull/fetch through the CLI
// route; without it, every CLI gate is false and transfers stay on the SQL
// route. The SQL remotes table lists the peer either way, and the peer row
// holds a password this test's key file can decrypt. The SQL route's
// connection is unroutable, so any statement that slips through fails at
// once instead of reaching a server.
func serverEnvPeerStore(t *testing.T, peerURL string, withCLIDir bool) (*DoltStore, sqlmock.Sqlmock) {
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
	if withCLIDir {
		serveDoltDatabase(t, store, store.dbPath, 3310)
		if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
			t.Fatal(err)
		}
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
		store, _ := serverEnvPeerStore(t, hostedURL, true)

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
		store, _ := serverEnvPeerStore(t, s3URL, true)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fresh-secret")

		err := store.Fetch(ctx, "peer")

		assertServerEnvNote(t, err, "fetch from peer peer failed", []string{
			"; dolt fetch runs in the Dolt server's process, which takes", "AWS_*",
		}, nil)
	})

	t.Run("pull from a credentialed s3 peer with ambient AWS credentials", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "peer "+s3URL)
		store, _ := serverEnvPeerStore(t, s3URL, true)
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
		store, _ := serverEnvPeerStore(t, s3URL, true)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")

		_, err := store.Sync(ctx, "peer", "")

		assertServerEnvNote(t, err, "fetch failed", []string{"AWS_*"}, nil)
	})

	t.Run("s3 peer push with no ambient credentials stays unexplained", func(t *testing.T) {
		fakeDoltOnPathFailingTransfers(t, "peer "+s3URL)
		store, _ := serverEnvPeerStore(t, s3URL, true)

		err := store.PushTo(ctx, "peer")

		if err == nil {
			t.Fatal("expected the transfer to fail")
		}
		if strings.Contains(err.Error(), "server's process") {
			t.Fatalf("s3 push with no held AWS variables %q must not claim the server takes a DOLT_REMOTE_PASSWORD it does not read for s3", err)
		}
	})
}

// TestServerEnvNoteNamesWhatTheServerReads pins which variables the note may
// name for a destination (GH#5953): the DOLT_REMOTE_PASSWORD pair only
// authenticates remotesapi destinations (DoltHub, Hosted Dolt), so a cloud
// object-store destination never has the pair named for it — its scheme's
// provider variables are all the server can read there.
func TestServerEnvNoteNamesWhatTheServerReads(t *testing.T) {
	peerCreds := &remoteCredentials{username: "peeruser", password: "peer-secret"}
	cases := []struct {
		name      string
		procedure string
		destURL   string
		creds     *remoteCredentials
		env       []string
		want      string
		unwant    string
	}{
		{
			name:      "hosted remotesapi with sent credentials",
			procedure: "dolt push",
			destURL:   "https://doltremoteapi.dolthub.com/org/repo",
			creds:     peerCreds,
			want:      "DOLT_REMOTE_PASSWORD",
		},
		{
			name:      "dolthub remotesapi with sent credentials",
			procedure: "CALL DOLT_PUSH",
			destURL:   "dolthub://org/repo",
			creds:     peerCreds,
			want:      "DOLT_REMOTE_PASSWORD",
		},
		{
			name:      "s3 destination with sent credentials names no password",
			procedure: "dolt push",
			destURL:   "s3://bucket/beads",
			creds:     peerCreds,
			want:      "",
		},
		{
			name:      "s3 destination with ambient AWS credentials",
			procedure: "dolt fetch",
			destURL:   "aws://bucket/beads",
			env:       []string{"AWS_ACCESS_KEY_ID=AKIAFRESH"},
			want:      "AWS_*",
			unwant:    "DOLT_REMOTE_PASSWORD",
		},
		{
			name:      "azure destination with ambient storage variables",
			procedure: "dolt pull",
			destURL:   "az://container/beads",
			env:       []string{"AZURE_STORAGE_ACCOUNT=bdenvacct"},
			want:      "AZURE_STORAGE_*",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note := buildServerEnvCredentialsNote(tc.procedure, tc.destURL, tc.creds, tc.env)
			if tc.want == "" {
				if note != "" {
					t.Fatalf("note %q should be empty: nothing the server reads is held", note)
				}
				return
			}
			if !strings.Contains(note, tc.want) {
				t.Fatalf("note %q should name %q", note, tc.want)
			}
			if tc.unwant != "" && strings.Contains(note, tc.unwant) {
				t.Fatalf("note %q should not name %q", note, tc.unwant)
			}
			if !strings.Contains(note, tc.procedure) {
				t.Fatalf("note %q should name the procedure %q", note, tc.procedure)
			}
		})
	}
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

// TestBackupSyncFailureNamesServerEnvironment pins the backup-sync note
// (GH#5953): the helper names the destination's variable family from the
// server's own dolt_backups row and stays silent for an unknown destination
// or an empty environment, and the store's BackupSync leaves a failure to
// even reach the server unexplained — the destination lookup that builds the
// note cannot answer through a connection that never opened.
func TestBackupSyncFailureNamesServerEnvironment(t *testing.T) {
	ctx := context.Background()
	const s3URL = "s3://bucket/beads"

	t.Run("helper names the destination's variables", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("SELECT url FROM dolt_backups").
			WillReturnRows(sqlmock.NewRows([]string{"url"}).AddRow(s3URL))

		note := BackupSyncServerEnvNote(ctx, db, "default", []string{"AWS_ACCESS_KEY_ID=AKIAFRESH"})

		if !strings.Contains(note, "; CALL DOLT_BACKUP runs in the Dolt server's process, which takes AWS_* from its own environment, not from bd's") {
			t.Fatalf("note %q should name the procedure and the AWS family", note)
		}
		if !strings.Contains(note, "restart it") {
			t.Fatalf("note %q should end with the startup-environment remedy", note)
		}
	})

	t.Run("unknown destination stays silent", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("SELECT url FROM dolt_backups").
			WillReturnRows(sqlmock.NewRows([]string{"url"})) // no row: destination not registered

		if note := BackupSyncServerEnvNote(ctx, db, "default", []string{"AWS_ACCESS_KEY_ID=AKIAFRESH"}); note != "" {
			t.Fatalf("unknown destination should carry no note, got %q", note)
		}
	})

	t.Run("nothing held stays silent", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("SELECT url FROM dolt_backups").
			WillReturnRows(sqlmock.NewRows([]string{"url"}).AddRow(s3URL))

		if note := BackupSyncServerEnvNote(ctx, db, "default", nil); note != "" {
			t.Fatalf("nothing held should carry no note, got %q", note)
		}
	})

	t.Run("connection failure carries no note", func(t *testing.T) {
		store := &DoltStore{serverMode: true, connStr: unroutableConnStr}

		err := store.BackupSync(ctx, "default")

		if err == nil || !strings.Contains(err.Error(), "sync backup default") {
			t.Fatalf("err = %v, want the sync's own failure", err)
		}
		if strings.Contains(err.Error(), "server's process") {
			t.Fatalf("a failure to reach the server %q must not be explained as its environment", err)
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

// TestPeerTransferSetupFailuresStayUnexplained pins where the peer SQL
// route's note attaches (GH#5953): only to the transfer statement's own
// failure. The SQL route's CALL runs on a one-shot connection, and that
// connection's setup — DSN parse, dial, BEGIN — happens before the server
// has run anything, so a setup failure carries no credential explanation:
// no statement ever executed in the server's environment. The fixture's
// unroutable connection string makes the setup fail at the dial, with bd
// holding the AWS variables an aws:// peer's note would otherwise name.
func TestPeerTransferSetupFailuresStayUnexplained(t *testing.T) {
	const awsURL = "aws://bucket/beads"
	ctx := context.Background()

	assertNoNote := func(t *testing.T, err error, wantContext string) {
		t.Helper()
		if err == nil {
			t.Fatal("expected the transfer to fail")
		}
		if !strings.Contains(err.Error(), wantContext) {
			t.Fatalf("error %q should keep its own context %q", err, wantContext)
		}
		if strings.Contains(err.Error(), "server's process") {
			t.Fatalf("connection setup never reached the server, so error %q must not carry a credential note", err)
		}
	}

	t.Run("push to an unreachable server", func(t *testing.T) {
		store, _ := serverEnvPeerStore(t, awsURL, false) // no CLI dir: SQL route
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fresh-secret")

		err := store.PushTo(ctx, "peer")

		assertNoNote(t, err, "failed to push to peer peer")
	})

	t.Run("fetch from an unreachable server", func(t *testing.T) {
		store, _ := serverEnvPeerStore(t, awsURL, false)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fresh-secret")

		err := store.Fetch(ctx, "peer")

		assertNoNote(t, err, "failed to fetch from peer peer")
	})
}

// TestServerEnvNoteLeavesUnrelatedFailuresUnchanged pins the note's guard
// rails (GH#5953): embedded mode, destinations whose scheme names no variable
// family bd can hold, and destinations the server cannot report all leave the
// error untouched, and an annotated error still unwraps to the original —
// the note is appended, never a replacement.
func TestServerEnvNoteLeavesUnrelatedFailuresUnchanged(t *testing.T) {
	ctx := context.Background()
	transferFailed := errors.New("error 1105: transfer failed mid-push")
	peerCreds := &remoteCredentials{username: "peeruser", password: "peer-secret"}
	awsEnv := []string{"PATH=/usr/bin", "AWS_ACCESS_KEY_ID=AKIAFRESH", "AWS_SECRET_ACCESS_KEY=fresh"}

	// newGuardStore returns a server-mode store whose dolt_remotes row lists
	// the peer at remoteURL (lookup fails when remoteErr is non-nil).
	newGuardStore := func(t *testing.T, remoteURL string, remoteErr error) *DoltStore {
		t.Helper()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.MatchExpectationsInOrder(false)
		q := mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes")
		if remoteErr != nil {
			q.WillReturnError(remoteErr)
		} else {
			q.WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("peer", remoteURL, nil))
		}
		return &DoltStore{serverMode: true, db: db}
	}

	// unchanged asserts err came back exactly as it went in.
	unchanged := func(t *testing.T, err error) {
		t.Helper()
		if err != transferFailed {
			t.Fatalf("error should be returned unchanged, got %q", err)
		}
	}

	t.Run("embedded mode adds no note", func(t *testing.T) {
		// The git-protocol peer routes through the CLI subprocess even in
		// embedded mode, and the fake dolt fails the transfer itself — so a
		// missing note here is the embedded suppression, not a setup skip.
		gitURL := "git@github.com:example/beads-peer.git"
		callLog := fakeDoltOnPathFailingTransfers(t, "peer "+gitURL)
		store, _ := serverEnvPeerStore(t, gitURL, true)
		store.serverMode = false

		err := store.PushTo(ctx, "peer")

		if err == nil {
			t.Fatal("expected the transfer to fail")
		}
		calls, readErr := os.ReadFile(callLog)
		if readErr != nil {
			t.Fatalf("read fake dolt call log: %v", readErr)
		}
		if !strings.Contains(string(calls), "push") {
			t.Fatalf("expected the CLI route to run dolt push, log says %q", calls)
		}
		if strings.Contains(err.Error(), "server's process") {
			t.Fatalf("embedded bd runs the subprocess itself; error %q must not explain a server environment it does not have", err)
		}
	})

	t.Run("unavailable destination metadata leaves the error unchanged", func(t *testing.T) {
		store := newGuardStore(t, "", errors.New("dolt_remotes: server gone away"))

		out := store.annotateServerEnvTransferError(ctx, serverEnvNotePlan{
			procedure: "CALL DOLT_PUSH", remote: "peer", creds: peerCreds, env: awsEnv,
		}, transferFailed)

		unchanged(t, out)
	})

	t.Run("unsupported scheme leaves the error unchanged", func(t *testing.T) {
		store := newGuardStore(t, "file:///srv/beads-remote", nil)

		out := store.annotateServerEnvTransferError(ctx, serverEnvNotePlan{
			procedure: "CALL DOLT_PUSH", remote: "peer", creds: peerCreds, env: awsEnv,
		}, transferFailed)

		unchanged(t, out)
	})

	t.Run("no relevant credentials leave the error unchanged", func(t *testing.T) {
		store := newGuardStore(t, "s3://bucket/beads", nil)

		out := store.annotateServerEnvTransferError(ctx, serverEnvNotePlan{
			procedure: "CALL DOLT_PUSH", remote: "peer", creds: peerCreds,
			env: []string{"PATH=/usr/bin", "HOME=/tmp"},
		}, transferFailed)

		unchanged(t, out)
	})

	t.Run("annotated errors keep errors.Is and errors.As", func(t *testing.T) {
		store := newGuardStore(t, "s3://bucket/beads", nil)
		type authFailure struct{ error }
		target := authFailure{errors.New("error 1105: auth handler failed")}
		wrapped := fmt.Errorf("push to peer peer: %w", target)

		out := store.annotateServerEnvTransferError(ctx, serverEnvNotePlan{
			procedure: "CALL DOLT_PUSH", remote: "peer", creds: peerCreds, env: awsEnv,
		}, wrapped)

		if !errors.Is(out, target) {
			t.Fatalf("errors.Is must see the original through the note: %q", out)
		}
		var got authFailure
		if !errors.As(out, &got) {
			t.Fatalf("errors.As must see the original through the note: %q", out)
		}
		if !strings.HasPrefix(out.Error(), wrapped.Error()+"; ") || strings.Count(out.Error(), "server's process") != 1 {
			t.Fatalf("annotated error %q should be the original %q with the note appended once", out, wrapped)
		}
	})
}
