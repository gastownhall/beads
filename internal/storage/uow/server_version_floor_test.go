package uow

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// TestRequireServerDoltVersion pins the floor openAndInitSchema applies to
// the server behind the proxy before any schema work: refused below
// doltversion.RequiredMin, not refused when the version is unknown.
func TestRequireServerDoltVersion(t *testing.T) {
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: 40001}
	for _, tc := range []struct {
		name      string
		answer    string
		queryErr  error
		wantBelow bool
	}{
		{name: "current", answer: "2.6.0"},
		{name: "newer", answer: "2.6.3"},
		{name: "old", answer: "2.5.1", wantBelow: true},
		{name: "unparseable", answer: "SET_BY_INIT"},
		{name: "query fails", queryErr: errors.New("connection reset")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			q := mock.ExpectQuery(regexp.QuoteMeta("SELECT dolt_version()"))
			if tc.queryErr != nil {
				q.WillReturnError(tc.queryErr)
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"dolt_version()"}).AddRow(tc.answer))
			}

			err = requireServerDoltVersion(context.Background(), db, ep)
			if !tc.wantBelow {
				if err != nil {
					t.Fatalf("requireServerDoltVersion = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				t.Fatalf("requireServerDoltVersion = %v, want ErrBelowRequiredMin", err)
			}
			for _, want := range []string{ep.Address(), tc.answer, doltversion.RequiredMin.String()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}
