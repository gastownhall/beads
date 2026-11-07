package dolt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The tests in this file pin the note a server-mode push, pull or fetch adds
// to its failure (serverCredentialsNote, GH#8210): with a Dolt sql-server the
// transfer runs in the server's process, on the SQL route and on the CLI
// route alike (the dolt CLI in a served directory hands it to the server),
// and that process takes DOLT_REMOTE_PASSWORD and the cloud variables from
// the environment it was started with, never from bd's. They run the real
// push/pull/fetch wiring over sqlmock and a dolt on PATH that fails every
// transfer; the routing and served-directory proof stay real
// (serveDoltDatabase), so a test here is routing and wiring evidence, not
// proof of what a real server does with the credentials.

// serverNoteRule is the part of the note that states the execution rule.
const serverNoteRule = "from the environment it was started with, never from bd's"

// failingDoltOnPath puts a dolt on PATH that logs every call like
// fakeDoltOnPath, answers `dolt remote -v` with remotes ("name url" lines),
// and fails every push, pull and fetch with failure on stderr.
func failingDoltOnPath(t *testing.T, failure string, remotes ...string) string {
	t.Helper()
	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "calls.log")
	var list strings.Builder
	for _, r := range remotes {
		fmt.Fprintf(&list, "echo '%s'\n", r)
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$(pwd -P)|$*|DOLT_REMOTE_PASSWORD=${DOLT_REMOTE_PASSWORD:-}" >> '%s'
case "$1" in
remote)
	if [ "$2" = "-v" ]; then
%s	fi
	;;
push|pull|fetch)
	echo '%s' >&2
	exit 1
	;;
esac
exit 0
`, callLog, list.String(), failure)
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

// serverTransferStore is a server-mode store for beadsDir on 127.0.0.1:port
// whose SQL connection is sqlmock (listing remotes, each "name", "url";
// queries match with their whitespace collapsed) and whose long-timeout SQL
// connections reach nothing. With served set, its CLI
// directory is a database a live server on port serves (serveDoltDatabase),
// so the CLI routes run there; otherwise it has none and every transfer
// takes the SQL route.
func serverTransferStore(t *testing.T, beadsDir string, port int, served bool, user, password string, remotes ...[2]string) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	collapsed := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		if strings.Join(strings.Fields(expected), " ") != strings.Join(strings.Fields(actual), " ") {
			return fmt.Errorf("query %q is not %q", actual, expected)
		}
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(collapsed))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 32; i++ {
		rows := sqlmock.NewRows([]string{"name", "url", "params"})
		for _, r := range remotes {
			rows.AddRow(r[0], r[1], nil)
		}
		mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").WillReturnRows(rows)
	}
	store := &DoltStore{
		serverMode:     true,
		readOnly:       true, // no pre-pull commit: the pull goes straight to its transfer
		beadsDir:       beadsDir,
		dbPath:         filepath.Join(beadsDir, "dolt"),
		database:       "beads",
		remote:         "origin",
		branch:         "main",
		remoteUser:     user,
		remotePassword: password,
		db:             db,
		cfg:            &Config{ServerHost: "127.0.0.1", ServerPort: port, BeadsDir: beadsDir},
		connStr:        unroutableConnStr,
	}
	if served {
		serveDoltDatabase(t, store, store.dbPath, port)
		if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return store, mock
}

// requireServerNote asserts that err keeps the transfer's own failure and
// carries the note once, with every part of want, and none of the secrets.
func requireServerNote(t *testing.T, err error, cause string, want []string, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("transfer succeeded, want its failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, cause) {
		t.Fatalf("error %q lost the transfer's own failure %q", msg, cause)
	}
	if n := strings.Count(msg, serverNoteRule); n != 1 {
		t.Fatalf("error %q carries the server note %d times, want once", msg, n)
	}
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("error %q lacks %q", msg, w)
		}
	}
	for _, s := range secrets {
		if strings.Contains(msg, s) {
			t.Errorf("error %q reveals the secret %q", msg, s)
		}
	}
}

// requireNoServerNote asserts that err, a failure, carries no server note.
func requireNoServerNote(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded, want its failure")
	}
	if strings.Contains(err.Error(), serverNoteRule) {
		t.Fatalf("error %q carries the server note, want none", err)
	}
}

// TestServerRunTransferFailureNamesTheServersEnvironment pins the note on the
// default remote (AC-1): a server-mode push, force push or pull whose remote
// bd holds a user for, or whose scheme's cloud variables bd's environment
// has, that fails in its transfer, on the CLI route in a served directory or
// on the SQL route, keeps its cause and names the variables the server takes
// from its startup environment, never their values. Only the server's own
// missing-password error makes it say that variable is missing.
func TestServerRunTransferFailureNamesTheServersEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)
	const hostedURL = "https://doltremoteapi.dolthub.com/org/repo"
	const azURL = "az://account.blob.core.windows.net/container/beads"
	const refused = "could not access dolt url: connection refused"
	const missingPassword = "error: must set DOLT_REMOTE_PASSWORD environment variable to use --user param"

	for _, tc := range []struct {
		name       string
		url        string
		served     bool
		user, pass string
		cloud      bool
		op         string
		failure    string
		want       []string
	}{
		{
			name: "CLI push with a held user and password", url: hostedURL, served: true,
			user: "admin", pass: "client-secret", op: "push", failure: refused,
			want: []string{`push for remote "origin"`, "takes DOLT_REMOTE_PASSWORD", "the password it holds for remote \"origin\" is not forwarded"},
		},
		{
			name: "CLI force push with a held user and password", url: hostedURL, served: true,
			user: "admin", pass: "client-secret", op: "force-push", failure: refused,
			want: []string{`push for remote "origin"`, "takes DOLT_REMOTE_PASSWORD"},
		},
		{
			name: "CLI pull with a user only", url: hostedURL, served: true,
			user: "admin", op: "pull", failure: refused,
			want: []string{`pull for remote "origin"`, "server-mode pull uses the Dolt server's startup environment for DOLT_REMOTE_PASSWORD"},
		},
		{
			name: "CLI push the server refuses for its missing password", url: hostedURL, served: true,
			user: "admin", pass: "client-secret", op: "push", failure: missingPassword,
			want: []string{"the process that ran this transfer has no DOLT_REMOTE_PASSWORD"},
		},
		{
			name: "SQL push with a held user and password", url: hostedURL,
			user: "admin", pass: "client-secret", op: "push", failure: "failed to push to origin/main",
			want: []string{`push for remote "origin"`, "takes DOLT_REMOTE_PASSWORD"},
		},
		{
			name: "CLI push with cloud variables", url: azURL, served: true,
			cloud: true, op: "push", failure: refused,
			want: []string{"takes AZURE_STORAGE_* from the environment"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failingDoltOnPath(t, tc.failure, "origin "+tc.url)
			if tc.cloud {
				t.Setenv("AZURE_STORAGE_KEY", "cloud-secret")
			}
			store, _ := serverTransferStore(t, unmanagedWorkspace(t), 3310, tc.served, tc.user, tc.pass, [2]string{"origin", tc.url})
			ctx := context.Background()
			var err error
			switch tc.op {
			case "push":
				err = store.Push(ctx)
			case "force-push":
				err = store.ForcePush(ctx)
			case "pull":
				err = store.Pull(ctx)
			}
			requireServerNote(t, err, tc.failure, tc.want, "client-secret", "cloud-secret")
			if tc.failure != missingPassword && strings.Contains(err.Error(), "has no DOLT_REMOTE_PASSWORD") {
				t.Errorf("error %q says the password is missing, which a %q failure does not show", err, tc.failure)
			}
		})
	}
}

// expectFederationPeer answers GetFederationPeer for name with a peer row
// holding user and, encrypted with the store's credential key, password, or
// with no row when both are empty.
func expectFederationPeer(t *testing.T, store *DoltStore, mock sqlmock.Sqlmock, name, url, user, password string) {
	t.Helper()
	const query = `SELECT name, remote_url, username, password_encrypted, sovereignty, last_sync, created_at, updated_at FROM federation_peers WHERE name = ?`
	columns := []string{"name", "remote_url", "username", "password_encrypted", "sovereignty", "last_sync", "created_at", "updated_at"}
	if user == "" && password == "" {
		mock.ExpectQuery(query).WithArgs(name).WillReturnRows(sqlmock.NewRows(columns))
		return
	}
	var encrypted []byte
	if password != "" {
		store.credentialKey = make([]byte, 32)
		var err error
		if encrypted, err = store.encryptPassword(password); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	mock.ExpectQuery(query).WithArgs(name).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(name, url, user, encrypted, "T2", nil, now, now))
}

// TestServerRunPeerTransferFailureNamesThePeersServerEnvironment pins the
// note on a federation peer (AC-2): a server-mode push, filtered push, pull or
// fetch to a peer bd holds a user for (a password is optional), or a
// cloud-only peer whose scheme's cloud variables bd's environment has, that
// fails in its transfer names the peer and what the server takes from its
// startup environment, with the limit of one server-wide password. For a
// peer with a user only it never says bd holds a password, and it never
// borrows the default remote's identity.
func TestServerRunPeerTransferFailureNamesThePeersServerEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)
	const originURL = "https://doltremoteapi.dolthub.com/org/origin"
	const peerURL = "https://doltremoteapi.dolthub.com/org/peer"
	const azURL = "az://account.blob.core.windows.net/container/peer"
	const refused = "could not access dolt url: connection refused"
	const serverWide = "one server-wide DOLT_REMOTE_PASSWORD goes with every user bd passes, so it cannot supply different passwords for different peers"

	for _, tc := range []struct {
		name       string
		url        string
		served     bool
		user, pass string // the peer row; no user and no password: no row
		cloud      bool
		op         string
		failure    string
		want       []string
		notWant    []string
		cliCall    string
	}{
		{
			name: "CLI fetch with a user only", url: peerURL, served: true,
			user: "peer-user", op: "fetch", failure: refused,
			want:    []string{`fetch for peer "peer1"`, "takes DOLT_REMOTE_PASSWORD", "server-mode fetch uses the Dolt server's startup environment for DOLT_REMOTE_PASSWORD", serverWide},
			notWant: []string{"password it holds", "origin"},
			cliCall: "|fetch --user peer-user peer1|DOLT_REMOTE_PASSWORD=",
		},
		{
			name: "CLI push with a held user and password", url: peerURL, served: true,
			user: "peer-user", pass: "peer-secret", op: "push", failure: refused,
			want:    []string{`push for peer "peer1"`, `the password it holds for peer "peer1" is not forwarded`, serverWide},
			cliCall: "|push --user peer-user peer1 main|DOLT_REMOTE_PASSWORD=peer-secret",
		},
		{
			name: "CLI filtered push with a held user and password", url: peerURL, served: true,
			user: "peer-user", pass: "peer-secret", op: "filtered-push", failure: refused,
			want:    []string{`push for peer "peer1"`, serverWide},
			cliCall: "|push --user peer-user peer1 staging:main|",
		},
		{
			name: "CLI pull with a user only", url: peerURL, served: true,
			user: "peer-user", op: "pull", failure: refused,
			want:    []string{`pull for peer "peer1"`, "server-mode pull uses the Dolt server's startup environment for DOLT_REMOTE_PASSWORD"},
			notWant: []string{"password it holds"},
			cliCall: "|pull --user peer-user peer1 main|",
		},
		{
			name: "SQL fetch with a user only", url: peerURL,
			user: "peer-user", op: "fetch", failure: "failed to fetch from peer peer1",
			want:    []string{`fetch for peer "peer1"`, serverWide},
			notWant: []string{"password it holds"},
		},
		{
			name: "SQL push with a held user and password", url: peerURL,
			user: "peer-user", pass: "peer-secret", op: "push", failure: "failed to push to peer peer1",
			want: []string{`push for peer "peer1"`, "is not forwarded", serverWide},
		},
		{
			name: "SQL push to a cloud-only peer", url: azURL,
			cloud: true, op: "push", failure: "failed to push to peer peer1",
			want:    []string{`push for peer "peer1"`, "takes AZURE_STORAGE_* from the environment"},
			notWant: []string{"DOLT_REMOTE_PASSWORD"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callLog := failingDoltOnPath(t, tc.failure, "origin "+originURL, "peer1 "+tc.url)
			if tc.cloud {
				t.Setenv("AZURE_STORAGE_KEY", "cloud-secret")
			}
			store, mock := serverTransferStore(t, unmanagedWorkspace(t), 3310, tc.served, "origin-user", "origin-secret",
				[2]string{"origin", originURL}, [2]string{"peer1", tc.url})
			expectFederationPeer(t, store, mock, "peer1", tc.url, tc.user, tc.pass)
			ctx := context.Background()
			var err error
			switch tc.op {
			case "push":
				err = store.PushTo(ctx, "peer1")
			case "filtered-push":
				err = store.pushRefToPeer(ctx, "peer1", "staging:main")
			case "pull":
				_, err = store.PullFrom(ctx, "peer1")
			case "fetch":
				err = store.Fetch(ctx, "peer1")
			}
			requireServerNote(t, err, tc.failure, tc.want, "peer-secret", "origin-secret", "cloud-secret")
			for _, nw := range tc.notWant {
				if strings.Contains(err.Error(), nw) {
					t.Errorf("error %q says %q", err, nw)
				}
			}
			if tc.cliCall != "" {
				raw, readErr := os.ReadFile(callLog)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !strings.Contains(string(raw), tc.cliCall) || strings.Contains(string(raw), "origin-user") {
					t.Fatalf("dolt calls %q lack the peer's own transfer %q, or use the default remote's user", raw, tc.cliCall)
				}
			}
		})
	}
}

// TestServerRunTransferFailureGuidesByServerOwnership pins the remedy the
// note ends with (AC-3): only a server ManagedServerDir proves bd started
// for this workspace gets the operator-run `bd dolt stop` and `bd dolt
// start` from a shell exporting what the server needs; a server bd cannot
// show it started (none recorded, or a recorded process that is not a dolt
// sql-server although the database directory is served) gets directions to
// that server's own environment and no bd restart commands.
func TestServerRunTransferFailureGuidesByServerOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)
	const hostedURL = "https://doltremoteapi.dolthub.com/org/repo"
	const azURL = "az://account.blob.core.windows.net/container/beads"
	const refused = "could not access dolt url: connection refused"
	const external = "in the environment of the Dolt server at 127.0.0.1:3310"

	for _, tc := range []struct {
		name      string
		workspace func(t *testing.T) string
		url       string
		served    bool
		cloud     bool
		failure   string
		want      []string
		notWant   []string
	}{
		{
			name: "server bd manages, CLI route", workspace: func(t *testing.T) string { return doltManagedWorkspace(t, 3310) },
			url: hostedURL, served: true, failure: refused,
			want:    []string{"run `bd dolt stop`, then `bd dolt start` from a shell that exports DOLT_REMOTE_PASSWORD, then retry"},
			notWant: []string{external},
		},
		{
			name: "server bd manages, SQL route, cloud remote", workspace: func(t *testing.T) string { return doltManagedWorkspace(t, 3310) },
			url: azURL, cloud: true, failure: "failed to push to origin/main",
			want:    []string{"`bd dolt start` from a shell that exports AZURE_STORAGE_*"},
			notWant: []string{external},
		},
		{
			name: "no server recorded, SQL route", workspace: unmanagedWorkspace,
			url: hostedURL, failure: "failed to push to origin/main",
			want:    []string{"set DOLT_REMOTE_PASSWORD " + external + " and restart that server, then retry"},
			notWant: []string{"bd dolt stop", "bd dolt start"},
		},
		{
			name: "served directory, recorded process not a dolt sql-server", workspace: func(t *testing.T) string { return managedWorkspace(t, 3310) },
			url: hostedURL, served: true, failure: refused,
			want:    []string{external},
			notWant: []string{"bd dolt stop", "bd dolt start"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failingDoltOnPath(t, tc.failure, "origin "+tc.url)
			if tc.cloud {
				t.Setenv("AZURE_STORAGE_KEY", "cloud-secret")
			}
			user, pass := "admin", "client-secret"
			if tc.cloud {
				user, pass = "", ""
			}
			store, _ := serverTransferStore(t, tc.workspace(t), 3310, tc.served, user, pass, [2]string{"origin", tc.url})
			err := store.Push(context.Background())
			requireServerNote(t, err, tc.failure, tc.want, "client-secret", "cloud-secret")
			for _, nw := range tc.notWant {
				if strings.Contains(err.Error(), nw) {
					t.Errorf("error %q says %q", err, nw)
				}
			}
		})
	}
}
