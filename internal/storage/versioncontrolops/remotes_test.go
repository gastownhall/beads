package versioncontrolops

import (
	"context"
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
