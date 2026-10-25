package dolt

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/remedy"
)

// TestRequireServerDoltVersion pins the server-mode floor every
// openServerConnection path (gateway, direct connect, no-database init
// connection) runs before it reads or creates anything: a server below
// doltversion.RequiredMin is refused with an error naming the server, its
// version and the minimum; one whose version cannot be determined is not.
func TestRequireServerDoltVersion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       *Config
		answer    string
		queryErr  error
		wantBelow bool
		wantAddr  string
	}{
		{name: "current", cfg: &Config{ServerHost: "127.0.0.1", ServerPort: 3307}, answer: "2.6.0"},
		{name: "old tcp", cfg: &Config{ServerHost: "db.example", ServerPort: 3307}, answer: "2.5.1", wantBelow: true, wantAddr: "db.example:3307"},
		{name: "old socket", cfg: &Config{ServerSocket: "/tmp/dolt.sock"}, answer: "2.1.8", wantBelow: true, wantAddr: "/tmp/dolt.sock"},
		{name: "unparseable", cfg: &Config{ServerHost: "127.0.0.1", ServerPort: 3307}, answer: "custom-build"},
		{name: "no dolt_version", cfg: &Config{ServerHost: "gw.example", ServerPort: 443}, queryErr: errors.New("Error 1305: FUNCTION dolt_version does not exist")},
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

			err = requireServerDoltVersion(context.Background(), db, tc.cfg)
			if !tc.wantBelow {
				if err != nil {
					t.Fatalf("requireServerDoltVersion = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				t.Fatalf("requireServerDoltVersion = %v, want ErrBelowRequiredMin", err)
			}
			for _, want := range []string{"dolt sql-server at " + remedy.PrintedValue(tc.wantAddr), tc.answer, doltversion.RequiredMin.String(), "restart the server"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}
