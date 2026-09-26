package dolt

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// writeCLIRepoState gives cliDir a .dolt/repo_state.json with one remote,
// the state a served database directory holds, without a working Dolt
// repository behind it: `dolt remote -v` there fails or lists nothing, which
// is also what the proxied CLI reports at a server's cold start.
func writeCLIRepoState(t *testing.T, cliDir, name, url, ref string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cliDir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	params := "{}"
	if ref != "" {
		params = `{"git_ref":"` + ref + `"}`
	}
	body := `{"head":"refs/heads/main","remotes":{"` + name + `":{"name":"` + name + `","url":"` + url + `","fetch_specs":["refs/heads/*:refs/remotes/` + name + `/*"],"params":` + params + `}},"backups":{},"branches":{}}`
	if err := os.WriteFile(filepath.Join(cliDir, ".dolt", "repo_state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newServedStoreForCLIMirror(t *testing.T) (*DoltStore, string) {
	t.Helper()
	// These tests stand in for a proxied CLI that lists nothing; they must
	// not depend on what a real dolt binary does in a directory that holds
	// only a state file, so no dolt is reachable from PATH here.
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	store := &DoltStore{serverMode: true, dbPath: root, database: "beads"}
	cliDir := store.CLIDir()
	if cliDir != filepath.Join(root, "beads") {
		t.Fatalf("CLIDir() = %q, want %q", cliDir, filepath.Join(root, "beads"))
	}
	return store, cliDir
}

// When the CLI in cliDir lists nothing, the mirror check for a remote on a
// custom ref consults the state file in cliDir: matching URL and ref is a
// match and nothing is re-materialized; a different ref is not a match. A
// remote on the default ref keeps the previous behavior and does not consult
// the file. Both ref shapes.
func TestHasMatchingCLIRemoteFallsBackToPersistedStateForRef(t *testing.T) {
	const url = "git+https://example.com/repo.git"
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			store, cliDir := newServedStoreForCLIMirror(t)
			writeCLIRepoState(t, cliDir, "origin", url, ref)

			if !store.hasMatchingCLIRemote("origin", url, ref) {
				t.Fatal("same URL and ref recorded in cliDir should match")
			}
			if store.hasMatchingCLIRemote("origin", url, "refs/heads/elsewhere") {
				t.Fatal("a different ref must not match")
			}
			if store.hasMatchingCLIRemote("origin", "git+https://example.com/other.git", ref) {
				t.Fatal("a different URL must not match")
			}
			if err := store.ensureMatchingCLIRemote("origin", url, ref); err != nil {
				t.Fatalf("ensureMatchingCLIRemote on a matching mirror should not touch dolt: %v", err)
			}
		})
	}

	t.Run("default ref does not consult the state file", func(t *testing.T) {
		store, cliDir := newServedStoreForCLIMirror(t)
		writeCLIRepoState(t, cliDir, "origin", url, "")
		if store.hasMatchingCLIRemote("origin", url, "") {
			t.Fatal("without a ref the check must stay on the CLI listing, which is empty here")
		}
	})
}

const listRemotesQuery = "SELECT name, url, params FROM dolt_remotes"

// The git-protocol route decider carries the ref from the SQL listing into
// the mirror check: a served directory whose state file records the same
// URL and ref routes over the CLI; the same URL on another ref does not
// (the re-materialization then needs the dolt binary, absent here, and the
// error says so instead of a silent default). Dropping the ref at the call
// site would pass the first case on the default ref and fail this test.
func TestPrepareCLIRouteForGitProtocolCarriesRef(t *testing.T) {
	const url = "git+ssh://git@example.com/org/repo.git"
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			store, cliDir := newServedStoreForCLIMirror(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store.db = db
			store.remote = "origin"
			mock.MatchExpectationsInOrder(false)
			// One listing per prepareCLIRouteForGitProtocol call below.
			for i := 0; i < 2; i++ {
				mock.ExpectQuery(regexp.QuoteMeta(listRemotesQuery)).WillReturnRows(
					sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("origin", url, `{"git_ref":"`+ref+`"}`))
			}

			writeCLIRepoState(t, cliDir, "origin", url, ref)
			useCLI, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
			if err != nil || !useCLI {
				t.Fatalf("matching state file: useCLI=%v err=%v, want true, nil", useCLI, err)
			}

			writeCLIRepoState(t, cliDir, "origin", url, "refs/heads/elsewhere")
			useCLI, err = store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
			if err == nil || useCLI {
				t.Fatalf("state file on another ref: useCLI=%v err=%v, want an error from the re-materialization", useCLI, err)
			}
		})
	}
}

// In the GH#2118 cold-start window (SQL lists nothing, the state file has the
// remote) a remote on a custom ref is re-added over bd's SQL connection with
// --ref before the CLI route is taken, so the server the proxied transfer
// delegates to knows the remote; "already exists" from the server is the
// state wanted. That write happens only when this process owns the served
// directory (localActiveDatabaseDir is the CLI directory); for any other
// server the route fails with the remedy instead of registering a remote
// read from a client-local file. A remote on the default ref keeps the
// previous path and issues no SQL add.
func TestPrepareCLIRouteForGitProtocolColdStartReaddsRefRemoteOverSQL(t *testing.T) {
	const url = "git+ssh://git@example.com/org/repo.git"
	const ref = "refs/dolt/units/team-12542"
	newStore := func(t *testing.T) (*DoltStore, string, sqlmock.Sqlmock) {
		t.Helper()
		store, cliDir := newServedStoreForCLIMirror(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store.db = db
		store.remote = "origin"
		store.localActiveDatabaseDir = cliDir
		mock.MatchExpectationsInOrder(false)
		// One listing per prepareCLIRouteForGitProtocol call.
		mock.ExpectQuery(regexp.QuoteMeta(listRemotesQuery)).WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}))

		return store, cliDir, mock
	}

	t.Run("ref remote is re-added with --ref", func(t *testing.T) {
		store, cliDir, mock := newStore(t)
		writeCLIRepoState(t, cliDir, "origin", url, ref)
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE('add', '--ref', ?, ?, ?)")).
			WithArgs(ref, "origin", url).WillReturnResult(sqlmock.NewResult(0, 1))
		useCLI, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
		if err != nil || !useCLI {
			t.Fatalf("useCLI=%v err=%v, want true, nil", useCLI, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("already exists is accepted", func(t *testing.T) {
		store, cliDir, mock := newStore(t)
		writeCLIRepoState(t, cliDir, "origin", url, ref)
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE('add', '--ref', ?, ?, ?)")).
			WithArgs(ref, "origin", url).WillReturnError(errRemoteAlreadyExistsForTest)
		useCLI, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
		if err != nil || !useCLI {
			t.Fatalf("useCLI=%v err=%v, want true, nil", useCLI, err)
		}
	})

	t.Run("unowned server is not written to", func(t *testing.T) {
		store, cliDir, mock := newStore(t)
		store.localActiveDatabaseDir = ""
		writeCLIRepoState(t, cliDir, "origin", url, ref)
		// No ExpectExec: an add would surface as a sqlmock error wrapped as
		// the SQL re-add failure, which the assertion below rejects.
		_, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
		if err == nil {
			t.Fatal("an unowned server must not be written to; the route must fail")
		}
		// The remedy must be something that works at this commit and
		// repairs the condition: the procedure call over bd's SQL
		// connection to that server.
		for _, want := range []string{"does not own", `bd sql 'CALL DOLT_REMOTE("add", "--ref", "` + ref + `", "origin", "` + url + `")'`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should contain %q: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "over SQL") {
			t.Fatalf("no SQL add may be attempted for an unowned server: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("other SQL failure is reported", func(t *testing.T) {
		store, cliDir, mock := newStore(t)
		writeCLIRepoState(t, cliDir, "origin", url, ref)
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE('add', '--ref', ?, ?, ?)")).
			WithArgs(ref, "origin", url).WillReturnError(errSQLDownForTest)
		_, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
		if err == nil || !strings.Contains(err.Error(), "GH#2118") || !strings.Contains(err.Error(), ref) {
			t.Fatalf("err = %v, want the cold-start explanation naming the ref", err)
		}
	})

	t.Run("default ref issues no SQL add", func(t *testing.T) {
		store, cliDir, mock := newStore(t)
		writeCLIRepoState(t, cliDir, "origin", url, "")
		// No ExpectExec: an unexpected add comes back from sqlmock as an
		// error that the code would wrap as the SQL re-add failure. The
		// error seen must instead be the CLI re-materialization failing
		// (no dolt repository behind the state file), the previous behavior.
		_, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
		if err == nil {
			t.Fatal("without a usable CLI mirror the route must fail")
		}
		if strings.Contains(err.Error(), "over SQL") {
			t.Fatalf("a default-ref remote must not be re-added over SQL: %v", err)
		}
		if !strings.Contains(err.Error(), "CLI") {
			t.Fatalf("expected the CLI materialization failure, got: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// The remedy is pasted into a shell, so user data in it must not change the
// command: an apostrophe in the URL is escaped for the shell, a dollar sign
// and a backtick sit inside single quotes, and a double quote or backslash in
// a value is escaped for the SQL literal.
func TestColdStartReaddRemedyQuotes(t *testing.T) {
	got := coldStartReaddRemedy("origin", "git+https://example.com/o'hare/$HOME/`id`/repo.git", "refs/heads/issue-data")
	want := `bd sql 'CALL DOLT_REMOTE("add", "--ref", "refs/heads/issue-data", "origin", "git+https://example.com/o'\''hare/$HOME/` + "`id`" + `/repo.git")'`
	if got != want {
		t.Fatalf("coldStartReaddRemedy =\n%s\nwant\n%s", got, want)
	}
	if !strings.HasPrefix(got, "bd sql '") || !strings.HasSuffix(got, "'") {
		t.Fatalf("the statement must be one single-quoted shell word: %s", got)
	}
	got = coldStartReaddRemedy("origin", `git+https://example.com/a"b\c.git`, "refs/dolt/units/team-12542")
	if !strings.Contains(got, `"git+https://example.com/a\"b\\c.git"`) {
		t.Fatalf("a double quote and a backslash must be escaped inside the SQL literal: %s", got)
	}
}

var (
	errRemoteAlreadyExistsForTest = &testSQLError{"error: remote already exists"}
	errSQLDownForTest             = &testSQLError{"driver: bad connection"}
)

type testSQLError struct{ msg string }

func (e *testSQLError) Error() string { return e.msg }
