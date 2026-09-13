package db

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// The proxied-server remote repository passes a ref as DOLT_REMOTE's --ref,
// verbatim, for both ref shapes; without a ref the call is the same
// three-argument add as before.
func TestRemoteSQLRepositoryAddRemoteWithRef(t *testing.T) {
	const url = "git+https://example.com/repo.git"
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()

			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE(?, ?, ?, ?, ?)")).
				WithArgs("add", "--ref", ref, "origin", url).
				WillReturnResult(sqlmock.NewResult(0, 1))

			repo := NewRemoteSQLRepository(db)
			require.NoError(t, repo.AddRemoteWithRef(context.Background(), "origin", url, ref))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}

	t.Run("empty ref is a plain add", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_REMOTE(?, ?, ?)")).
			WithArgs("add", "origin", url).
			WillReturnResult(sqlmock.NewResult(0, 1))

		repo := NewRemoteSQLRepository(db)
		require.NoError(t, repo.AddRemoteWithRef(context.Background(), "origin", url, "  "))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// ListRemotes reads the ref back from the params column; remotes without
// the parameter list the default.
func TestRemoteSQLRepositoryListRemotesReadsRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{"name", "url", "params"}).
		AddRow("plain", "file:///tmp/p", nil).
		AddRow("empty", "dolthub://org/repo", "{}").
		AddRow("branch", "git+https://example.com/repo.git", `{"git_ref":"refs/heads/issue-data"}`).
		AddRow("unit", "git+file:///srv/ledgers", `{"git_ref":"refs/dolt/units/team-12542"}`)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, url, params FROM dolt_remotes")).WillReturnRows(rows)

	remotes, err := NewRemoteSQLRepository(db).ListRemotes(context.Background())
	require.NoError(t, err)
	want := map[string]string{
		"plain":  "",
		"empty":  "",
		"branch": "refs/heads/issue-data",
		"unit":   "refs/dolt/units/team-12542",
	}
	require.Len(t, remotes, len(want))
	for _, r := range remotes {
		require.Equal(t, want[r.Name], r.Ref, "remote %s", r.Name)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
