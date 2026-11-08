package dolt

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// serverEnvRemedy is the remedy every server-environment note ends with
// (bd-f68dud, GH#5953): in server mode both the SQL and the CLI route run the
// transfer in the Dolt server's process, so only the server's own environment
// can supply fresh credentials.
const serverEnvRemedy = "if the server lacks current credentials, set them in the environment the server starts from and restart it"

// serverEnvNoteHead is the sentence a note opens with for procedure, naming
// vars as the variables bd holds that the server reads instead.
func serverEnvNoteHead(procedure, vars string) string {
	return "CALL " + procedure + " runs in the Dolt server's process, which takes " + vars + " from its own environment, not from bd's"
}

// serverEnvNoteMarker is in every server-environment note and nowhere else.
const serverEnvNoteMarker = "runs in the Dolt server's process"

// envNoteOpts shapes envNoteStore.
type envNoteOpts struct {
	// embedded makes the store embedded rather than server mode.
	embedded bool
	// cli serves the store's CLI directory, so the CLI routes are taken.
	cli bool
	// readOnly skips the pre-pull commit and post-pull settlement writes.
	readOnly bool
	// peerUser stores a federation_peers row with this user.
	peerUser string
	// peerPassword gives that row a password the store's key file decrypts.
	peerPassword string
	// remoteUser and remotePassword are the default remote's credentials.
	remoteUser, remotePassword string
	// remotesErr makes the dolt_remotes listing fail.
	remotesErr bool
	// unreachable makes the SQL routes fail to reach the server at all
	// (unroutableConnStr), before any transfer statement runs.
	unreachable bool
	// replies overrides the reachable server's replies by statement prefix
	// (refusingSQLServerReplying).
	replies map[string]string
}

// envNoteStore is a store with one remote, name at url, whose SQL routes reach
// a server that refuses every transfer statement (refusingSQLServer), or none
// at all with o.unreachable, and whose dolt_remotes and federation_peers rows
// sqlmock serves. The returned mock takes further expectations.
func envNoteStore(t *testing.T, name, url string, o envNoteOpts) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	beadsDir := unmanagedWorkspace(t)
	var encPeerPassword []byte
	if o.peerPassword != "" {
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, credentialKeyFile), key, 0o600); err != nil {
			t.Fatal(err)
		}
		var err error
		if encPeerPassword, err = encryptWithKey(o.peerPassword, key); err != nil {
			t.Fatal(err)
		}
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 12; i++ {
		q := mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes")
		if o.remotesErr {
			q.WillReturnError(fmt.Errorf("dolt_remotes unavailable"))
		} else {
			q.WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow(name, url, nil))
		}
		peers := sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted", "sovereignty", "last_sync", "created_at", "updated_at"})
		if o.peerUser != "" {
			peers.AddRow(name, url, o.peerUser, encPeerPassword, "", nil, time.Now(), time.Now())
		}
		mock.ExpectQuery("FROM federation_peers WHERE name").WillReturnRows(peers)
	}
	connStr := unroutableConnStr
	if !o.unreachable {
		connStr = refusingSQLServerReplying(t, o.replies)
	}
	store := &DoltStore{
		serverMode:     !o.embedded,
		readOnly:       o.readOnly,
		beadsDir:       beadsDir,
		dbPath:         filepath.Join(beadsDir, "dolt"),
		database:       "beads",
		remote:         "origin",
		branch:         "main",
		remoteUser:     o.remoteUser,
		remotePassword: o.remotePassword,
		db:             db,
		cfg:            &Config{ServerHost: "127.0.0.1", ServerPort: 3310},
		connStr:        connStr,
	}
	if o.cli {
		serveDoltDatabase(t, store, store.dbPath, 3310)
		if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !store.hasCLIDatabase() {
			t.Fatal("precondition: the store's CLI directory does not count as the server's database")
		}
	}
	return store, mock
}

// refusingDoltOnPath puts a dolt on PATH that records each call, lists the
// remote name at url, and fails each subcommand in refuse the way a remote
// that rejects stale credentials does. It returns the call log.
func refusingDoltOnPath(t *testing.T, name, url string, refuse ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "calls.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
if [ "$1 $2" = "remote -v" ]; then echo '%s %s'; fi
for c in %s; do
	if [ "$1" = "$c" ]; then echo "fake dolt: $1 refused: AccessDenied" >&2; exit 1; fi
done
exit 0
`, callLog, name, url, strings.Join(refuse, " "))
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

// checkServerEnvNote asserts that err carries exactly one server-environment
// note for procedure naming exactly vars, followed by the remedy, and that the
// note itself repeats no destination URL and no credential value; vars ""
// means err must carry no note at all.
func checkServerEnvNote(t *testing.T, err error, procedure, vars, url string) {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded, want a failure")
	}
	msg := err.Error()
	if vars == "" {
		if strings.Contains(msg, serverEnvNoteMarker) {
			t.Fatalf("error = %q, want no server-environment note", msg)
		}
		return
	}
	if n := strings.Count(msg, serverEnvNoteMarker); n != 1 {
		t.Fatalf("error = %q carries %d server-environment notes, want 1", msg, n)
	}
	head := serverEnvNoteHead(procedure, vars)
	if !strings.Contains(msg, head+"; "+serverEnvRemedy) {
		t.Fatalf("error = %q, want the note %q followed by the remedy %q", msg, head, serverEnvRemedy)
	}
	note := msg[strings.Index(msg, "CALL "+procedure+" runs"):]
	for _, leak := range []string{url, "sentinel"} {
		if leak != "" && strings.Contains(note, leak) {
			t.Fatalf("note %q repeats %q", note, leak)
		}
	}
}

// TestServerEnvNoteNamesWhatTheServerReads pins which variables a failed
// server-mode transfer's note names (bd-f68dud, GH#5953): the cloud variable
// families of the destination's scheme that bd holds, and
// DOLT_REMOTE_PASSWORD only for a remotesapi destination the transfer sent an
// explicit user to while bd holds the password. The pinned Dolt reads the
// password from the environment only for an explicit user, so an ambient
// DOLT_REMOTE_* pair, a user with no password, or a cloud scheme says nothing
// about it. bd's own AWS_RESPONSE_CHECKSUM_VALIDATION override is not a
// credential. The note repeats no value and no URL.
func TestServerEnvNoteNamesWhatTheServerReads(t *testing.T) {
	const hosted = "https://doltremoteapi.dolthub.com/org/repo"
	for _, tc := range []struct {
		name           string
		url            string
		user, password string
		env            map[string]string
		want           string
	}{
		{name: "s3 with AWS_ and an explicit user", url: "s3://bucket/beads", user: "u", password: "sentinel-pw",
			env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}, want: "AWS_*"},
		{name: "aws scheme", url: "aws://[table:bucket]/beads",
			env: map[string]string{"AWS_SECRET_ACCESS_KEY": "sentinel-key"}, want: "AWS_*"},
		{name: "hosted with an explicit user and a held password", url: hosted, user: "u", password: "sentinel-pw",
			env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}, want: "DOLT_REMOTE_PASSWORD"},
		{name: "dolthub with an explicit user and a held password", url: "dolthub://org/repo", user: "u", password: "sentinel-pw",
			want: "DOLT_REMOTE_PASSWORD"},
		{name: "hosted with a user but no password", url: hosted, user: "u"},
		{name: "hosted with only an ambient password", url: hosted,
			env: map[string]string{"DOLT_REMOTE_USER": "u", "DOLT_REMOTE_PASSWORD": "sentinel-pw"}},
		{name: "gs with GOOGLE_ and GCS_", url: "gs://bucket/beads",
			env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/sentinel/key.json", "GCS_ENDPOINT": "sentinel"}, want: "GOOGLE_* and GCS_*"},
		{name: "gs with only GCS_", url: "gs://bucket/beads",
			env: map[string]string{"GCS_ENDPOINT": "sentinel"}, want: "GCS_*"},
		{name: "az with AZURE_STORAGE_", url: "az://account.blob.core.windows.net/container/beads",
			env: map[string]string{"AZURE_STORAGE_KEY": "sentinel"}, want: "AZURE_STORAGE_*"},
		{name: "az with only AWS_", url: "az://account.blob.core.windows.net/container/beads",
			env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "oci with OCI_", url: "oci://bucket/beads",
			env: map[string]string{"OCI_CLI_KEY_FILE": "/sentinel"}, want: "OCI_*"},
		{name: "s3 with only bd's checksum override", url: "s3://bucket/beads",
			env: map[string]string{awsResponseChecksumValidationEnv: "when_required"}},
		{name: "file remote", url: "file:///tmp/remote",
			env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "git protocol remote", url: "git+ssh://git@example.com/org/repo.git",
			env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPredicateEnv(t)
			clearCloudAuthEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			store, _ := envNoteStore(t, "origin", tc.url, envNoteOpts{remoteUser: tc.user, remotePassword: tc.password})

			err := store.Push(context.Background())

			if err == nil || !strings.Contains(err.Error(), "push to origin/main") {
				t.Fatalf("Push = %v, want the SQL route's CALL DOLT_PUSH failure", err)
			}
			checkServerEnvNote(t, err, "DOLT_PUSH", tc.want, tc.url)
		})
	}
}
