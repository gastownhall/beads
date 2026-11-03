package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The two ref shapes every ref test in this change covers: a branch, for a
// git host that only accepts pushes under refs/heads/, and a ref elsewhere
// in the namespace, for several databases kept in one repository.
var gitDataRefShapes = []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"}

// Without a ref the add is the same two-argument DOLT_REMOTE call every
// existing remote was created with.
func TestAddRemoteWithoutRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE('add', ?, ?)")).
		WithArgs("origin", "git+https://example.com/repo.git").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := AddRemote(context.Background(), db, "origin", "git+https://example.com/repo.git", "  "); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// With a ref the add carries --ref, and the value reaches Dolt verbatim for
// both shapes.
func TestAddRemoteWithRef(t *testing.T) {
	for _, ref := range gitDataRefShapes {
		t.Run(ref, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE('add', '--ref', ?, ?, ?)")).
				WithArgs(ref, "origin", "git+https://example.com/repo.git").
				WillReturnResult(sqlmock.NewResult(0, 1))

			if err := AddRemote(context.Background(), db, "origin", "git+https://example.com/repo.git", ref); err != nil {
				t.Fatalf("AddRemote: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// ListRemotes reads the ref back out of the params column. A NULL, the text
// "null" a sql-server sends for a remote without parameters, an empty
// object, and an object without git_ref all list as the default (empty Ref);
// a recorded git_ref comes back verbatim.
func TestListRemotesReadsGitRefParam(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"name", "url", "params"}).
		AddRow("null-params", "file:///tmp/a", nil).
		AddRow("null-text", "file:///tmp/b", "null").
		AddRow("empty-params", "dolthub://org/repo", "{}").
		AddRow("other-params", "aws://bucket/repo", `{"aws-region":"us-east-1"}`).
		AddRow("branch", "git+https://example.com/repo.git", `{"git_ref":"refs/heads/issue-data"}`).
		AddRow("unit", "git+file:///srv/ledgers", `{"git_ref":"refs/dolt/units/team-12542"}`)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, url, params FROM dolt_remotes")).WillReturnRows(rows)

	remotes, err := ListRemotes(context.Background(), db)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	want := map[string]string{
		"null-params":  "",
		"null-text":    "",
		"empty-params": "",
		"other-params": "",
		"branch":       "refs/heads/issue-data",
		"unit":         "refs/dolt/units/team-12542",
	}
	if len(remotes) != len(want) {
		t.Fatalf("ListRemotes returned %d remotes, want %d: %+v", len(remotes), len(want), remotes)
	}
	for _, r := range remotes {
		if r.Ref != want[r.Name] {
			t.Errorf("remote %s: Ref = %q, want %q", r.Name, r.Ref, want[r.Name])
		}
	}
	if remotes[0].URL != "file:///tmp/a" {
		t.Errorf("URL still scanned: got %q", remotes[0].URL)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A params value that is not a JSON object is an error naming the remote,
// never a silent fall-back to the default ref: routing a custom-ref remote
// through the default would push its data to the wrong ref.
func TestListRemotesRejectsMalformedParams(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"name", "url", "params"}).
		AddRow("origin", "git+https://example.com/repo.git", "not-json")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, url, params FROM dolt_remotes")).WillReturnRows(rows)

	_, err = ListRemotes(context.Background(), db)
	if err == nil {
		t.Fatal("ListRemotes with malformed params = nil error, want error")
	}
	if !strings.Contains(err.Error(), "origin") || !strings.Contains(err.Error(), "params") {
		t.Errorf("error should name the remote and the params column: %v", err)
	}
}

// pullAheadBehindQuery matches LocalAheadBehind's count against origin/main.
const pullAheadBehindQuery = `dolt_log AS OF 'origin/main'`

// GH#6368: when origin/main has no commits local lacks, Pull must not merge.
// Dolt's stomp check runs before its "nothing to merge" exit and ignores
// dolt_ignore, so a table the remote still tracks and local HEAD has dropped
// fails a merge that would change nothing, and bd sync never reaches the push
// that clears it. The reported state was 430 ahead, 0 behind.
func TestPullSkipsMergeWhenRemoteHasNothingNew(t *testing.T) {
	for _, tt := range []struct {
		name          string
		ahead, behind int
	}{
		{"remote is an ancestor", 430, 0},
		{"level", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_FETCH(?, ?)")).
				WithArgs("origin", "main").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(regexp.QuoteMeta(pullAheadBehindQuery)).
				WillReturnRows(sqlmock.NewRows([]string{"ahead", "behind"}).AddRow(tt.ahead, tt.behind))

			if err := PullWithStrategy(context.Background(), db, "origin", "main", "", ""); err != nil {
				t.Fatalf("PullWithStrategy: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Pull still merges when the remote has commits local lacks, and when the
// count itself fails.
func TestPullMergesWhenRemoteHasNewCommits(t *testing.T) {
	errMergeStarted := errors.New("merge step reached")
	errCount := errors.New("count failed")
	for _, tt := range []struct {
		name          string
		ahead, behind int
		countErr      error
	}{
		{name: "remote one ahead", ahead: 0, behind: 1},
		{name: "diverged", ahead: 2, behind: 1},
		{name: "count fails", countErr: errCount},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_FETCH(?, ?)")).
				WithArgs("origin", "main").WillReturnResult(sqlmock.NewResult(0, 0))
			count := mock.ExpectQuery(regexp.QuoteMeta(pullAheadBehindQuery))
			if tt.countErr != nil {
				count.WillReturnError(tt.countErr)
			} else {
				count.WillReturnRows(sqlmock.NewRows([]string{"ahead", "behind"}).AddRow(tt.ahead, tt.behind))
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dolt_status LIMIT 1")).
				WillReturnRows(sqlmock.NewRows([]string{"1"}))
			mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
				WillReturnError(errMergeStarted)

			err = PullWithStrategy(context.Background(), db, "origin", "main", "", "")
			if !errors.Is(err, errMergeStarted) {
				t.Fatalf("PullWithStrategy = %v, want the merge step to run", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
