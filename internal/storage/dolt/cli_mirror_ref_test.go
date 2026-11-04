package dolt

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
	// The directory is the served one: no port variable names the server,
	// and a live server on the dialed port serves the Dolt root.
	clearServerPortEnv(t)
	root := t.TempDir()
	store := &DoltStore{serverMode: true, dbPath: root, database: "beads", cfg: &Config{ServerHost: "127.0.0.1", ServerPort: testServedPort}}
	serveDoltDatabase(t, store, root, testServedPort)
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
		for _, want := range []string{"does not own", "re-add it on that server " + coldStartReaddRemedy(runtime.GOOS, "origin", url, ref)} {
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

	// The refusal names the persisted URL and ref before its remedy. They
	// are named escaped, so a $( ) or a backtick in the URL is not live on
	// that line even where the remedy itself is a single-quoted bd sql
	// command; TestColdStartReaddRemedy pins the remedy.
	for _, tc := range []struct{ name, url, named string }{
		{"unowned server names a dollar and backtick URL escaped", "git+https://example.com/$HOME/`id`/repo.git",
			`remote "origin" ("git+https://example.com/\x24HOME/\x60id\x60/repo.git" on "` + ref + `")`},
		{"unowned server names an apostrophe and $( ) URL escaped", "git+https://example.com/o'hare/$(id)/repo.git",
			`remote "origin" ("git+https://example.com/o\x27hare/\x24(id)/repo.git" on "` + ref + `")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cliDir, _ := newStore(t)
			store.localActiveDatabaseDir = ""
			writeCLIRepoState(t, cliDir, "origin", tc.url, ref)
			_, err := store.prepareCLIRouteForGitProtocol(context.Background(), "origin")
			if err == nil {
				t.Fatal("an unowned server must not be written to; the route must fail")
			}
			head, tail, ok := strings.Cut(err.Error(), "re-add it on that server ")
			if !ok || tail != coldStartReaddRemedy(runtime.GOOS, "origin", tc.url, ref) {
				t.Fatalf("error does not end in the cold-start remedy: %v", err)
			}
			if !strings.HasPrefix(head, tc.named+" is persisted on disk") {
				t.Errorf("error does not name the remote, URL and ref escaped (%s): %v", tc.named, err)
			}
			if strings.ContainsAny(head, "$`") {
				t.Errorf("error names the URL with a $ or backtick a shell expands: %s", head)
			}
		})
	}

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

// TestColdStartReaddRemedy pins the GH#2118 cold-start remedy for each
// platform, through the shared remedy printer (internal/remedy). On POSIX the
// statement is one single-quoted bd sql argument, with a dollar sign and a
// backtick in the URL literal inside it and a double quote or backslash
// escaped for the SQL literal. On Windows no quoting passes the statement's
// own double quotes and commas through cmd.exe or PowerShell to bd.exe as one
// argument (the single-quoted form reached bd.exe as eight arguments, and bd
// sql refused it), and on POSIX an apostrophe or backslash has no quoting
// every shell there agrees on, so the remedy names the statement on its own,
// never as a paste-ready bd sql command. It names it as it is, to run as
// printed in a Dolt SQL shell, when the remote, URL and ref are ordinary
// values, such as a git+https or git+ssh URL: one the remedy printer quotes
// on every platform. Otherwise it names it escaped (remedy.EscapedValue):
// printed as it is, a $( ) or a backtick in the URL sat inside the
// statement's double quotes, which PowerShell and Git Bash expand when the
// line is pasted. The escaped name still reads back (strconv.Unquote) as the
// statement. A value holding a carriage return or terminal escape is named
// escaped either way.
func TestColdStartReaddRemedy(t *testing.T) {
	const ref = "refs/dolt/units/team-1"
	const url = "git+https://example.com/org/repo.git"
	// statement is the statement for a URL literal spelled url.
	statement := func(url string) string {
		return `CALL DOLT_REMOTE("add", "--ref", "` + ref + `", "origin", "` + url + `")`
	}
	// escapedFor is the statement for a remote, ref and URL literal spelled
	// remote, ref and url, escaped as remedy.EscapedValue spells it.
	escapedFor := func(remote, ref, url string) string {
		return `"CALL DOLT_REMOTE(\x22add\x22\x2c \x22--ref\x22\x2c \x22` + ref + `\x22\x2c \x22` + remote + `\x22\x2c \x22` + url + `\x22)"`
	}
	stmt := statement(url)
	const statementOnly = "by running this SQL statement there, as the one argument of bd sql (quoted for your shell) or in a Dolt SQL shell: "
	// escapedStatement is the statement for a URL literal spelled url,
	// escaped as remedy.EscapedValue spells it: \x22 for each double quote,
	// \x2c for each comma.
	escapedStatement := func(url string) string { return escapedFor("origin", ref, url) }
	for _, tc := range []struct {
		name, goos, url, want string
	}{
		{"POSIX", "linux", url, "with: bd sql '" + stmt + "'"},
		{"POSIX dollar and backtick", "darwin", "git+https://example.com/$HOME/`id`/repo.git",
			`with: bd sql 'CALL DOLT_REMOTE("add", "--ref", "` + ref + `", "origin", "git+https://example.com/$HOME/` + "`id`" + `/repo.git")'`},
		{"POSIX apostrophe", "linux", "git+https://example.com/o'hare/repo.git",
			statementOnly + escapedStatement(`git+https://example.com/o\x27hare/repo.git`)},
		{"POSIX apostrophe, dollar and backtick", "linux", "git+https://example.com/o'hare/$(id)/`id`/repo.git",
			statementOnly + escapedStatement(`git+https://example.com/o\x27hare/\x24(id)/\x60id\x60/repo.git`)},
		{"POSIX double quote and backslash", "linux", `git+https://example.com/a"b\c.git`,
			statementOnly + escapedStatement(`git+https://example.com/a\\\x22b\\\\c.git`)},
		{"Windows", "windows", url, statementOnly + stmt},
		{"Windows ssh URL", "windows", "git+ssh://git@example.com:2222/org/repo.git",
			statementOnly + statement("git+ssh://git@example.com:2222/org/repo.git")},
		{"Windows dollar and backtick", "windows", "git+https://example.com/$HOME/`id`/repo.git",
			statementOnly + escapedStatement(`git+https://example.com/\x24HOME/\x60id\x60/repo.git`)},
		{"Windows percent", "windows", "git+https://example.com/org%20name/repo.git",
			statementOnly + escapedStatement(`git+https://example.com/org\x2520name/repo.git`)},
		{"Windows apostrophe", "windows", "git+https://example.com/o'hare/repo.git",
			statementOnly + escapedStatement(`git+https://example.com/o\x27hare/repo.git`)},
		{"Windows backslash", "windows", `git+https://example.com/a\b.git`,
			statementOnly + escapedStatement(`git+https://example.com/a\\\\b.git`)},
		{"terminal escape", "linux", "git+https://example.com/r.git\r\x1b[2K", ""},
		{"terminal escape on Windows", "windows", "git+https://example.com/r.git\r\x1b[2K", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := coldStartReaddRemedy(tc.goos, "origin", tc.url, ref)
			if tc.want != "" && got != tc.want {
				t.Fatalf("coldStartReaddRemedy(%q) =\n%s\nwant\n%s", tc.goos, got, tc.want)
			}
			if strings.ContainsAny(got, "\r\x1b") {
				t.Fatalf("coldStartReaddRemedy(%q) prints a carriage return or terminal escape raw: %q", tc.goos, got)
			}
			if tc.goos == "windows" && strings.Contains(got, "bd sql '") {
				t.Fatalf("coldStartReaddRemedy(windows) prints a POSIX-quoted bd sql command bd.exe splits: %s", got)
			}
			if strings.HasPrefix(got, "with: bd sql '") {
				return
			}
			named, ok := strings.CutPrefix(got, statementOnly)
			if !ok {
				t.Fatalf("coldStartReaddRemedy(%q) = %s, want the bd sql command or the statement on its own", tc.goos, got)
			}
			if named == statement(tc.url) {
				// Named as it is: only for a URL every shell's quoting
				// keeps literal, so nothing in it expands when pasted.
				if strings.ContainsAny(tc.url, "$`%!\"'\\") {
					t.Errorf("coldStartReaddRemedy(%q) names the statement as it is for a URL a shell acts on: %s", tc.goos, named)
				}
				return
			}
			if strings.ContainsAny(named, "$`") {
				t.Errorf("coldStartReaddRemedy(%q) names the statement with a $ or backtick a shell expands: %s", tc.goos, named)
			}
			if unquoted, err := strconv.Unquote(named); err != nil || !strings.HasPrefix(unquoted, `CALL DOLT_REMOTE("add", "--ref", "`+ref+`", "origin", "`) {
				t.Errorf("coldStartReaddRemedy(%q) names %s, which does not read back as the statement: %q, %v", tc.goos, named, unquoted, err)
			}
		})
	}
	// The ref and the remote name are repository data too: a shell-active
	// character in either keeps the Windows statement escaped even with an
	// ordinary URL.
	for _, tc := range []struct {
		name, remote, ref, want string
	}{
		{"Windows dollar in ref", "origin", "refs/dolt/$(id)",
			statementOnly + escapedFor("origin", `refs/dolt/\x24(id)`, url)},
		{"Windows backtick in ref", "origin", "refs/dolt/`id`",
			statementOnly + escapedFor("origin", `refs/dolt/\x60id\x60`, url)},
		{"Windows apostrophe in remote", "o'rigin", ref,
			statementOnly + escapedFor(`o\x27rigin`, ref, url)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := coldStartReaddRemedy("windows", tc.remote, url, tc.ref); got != tc.want {
				t.Fatalf("coldStartReaddRemedy(windows, %q, %q) =\n%s\nwant\n%s", tc.remote, tc.ref, got, tc.want)
			}
		})
	}
}

var (
	errRemoteAlreadyExistsForTest = &testSQLError{"error: remote already exists"}
	errSQLDownForTest             = &testSQLError{"driver: bad connection"}
)

type testSQLError struct{ msg string }

func (e *testSQLError) Error() string { return e.msg }
