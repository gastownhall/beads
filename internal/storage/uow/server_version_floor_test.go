package uow

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestRequireServerDoltVersion pins the floor openAndInitSchema applies to
// the server behind the proxy before any schema work: refused below
// doltversion.RequiredMin, not refused when the version is unknown. The
// refusal names the external server behind the proxy when there is one,
// the server an operator has to upgrade, and prints both addresses the way
// remedy values are printed.
func TestRequireServerDoltVersion(t *testing.T) {
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: 40001}
	for _, tc := range []struct {
		name      string
		backend   string
		answer    string
		queryErr  error
		wantBelow bool
		wantNames []string
	}{
		{name: "current", answer: "2.6.0"},
		{name: "newer", answer: "2.6.3"},
		{name: "old", answer: "2.5.1", wantBelow: true, wantNames: []string{"behind the bd proxy at " + ep.Address()}},
		{name: "old external", backend: "db.example:3306", answer: "2.5.1", wantBelow: true,
			wantNames: []string{"the dolt sql-server at db.example:3306 (behind the bd proxy at " + ep.Address() + ")"}},
		{name: "old external, escaped", backend: "db.example\r:3306", answer: "2.5.1", wantBelow: true,
			wantNames: []string{"the dolt sql-server at " + remedy.PrintedValue("db.example\r:3306")}},
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

			err = requireServerDoltVersion(context.Background(), db, ep, tc.backend)
			if !tc.wantBelow {
				if err != nil {
					t.Fatalf("requireServerDoltVersion = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				t.Fatalf("requireServerDoltVersion = %v, want ErrBelowRequiredMin", err)
			}
			for _, want := range append(tc.wantNames, tc.answer, doltversion.RequiredMin.String()) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestOpenAndInitSchemaRefusesBelowRequiredMin pins requireServerDoltVersion
// at both of openAndInitSchema's paths against a real server: with
// doltversion.RequiredMin raised above the test server's version, the
// single-session open of an existing database and the no-database init
// connection of a fresh one are both refused before any schema work, and the
// fresh database is not created.
func TestOpenAndInitSchemaRefusesBelowRequiredMin(t *testing.T) {
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := sql.Open("mysql", buildDSN(ep, "", "root", "", "", false))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE floor_existing"); err != nil {
		t.Fatalf("create the existing database: %v", err)
	}

	prev := doltversion.RequiredMin
	doltversion.RequiredMin = doltversion.MustParse("99.0.0")
	t.Cleanup(func() { doltversion.RequiredMin = prev })
	for _, database := range []string{"floor_existing", "floor_fresh"} {
		provider, err := openAndInitSchema(ctx, ep, "", database, "root", "", "", false, false, "", providerOptions{})
		if provider != nil {
			_ = provider.Close(ctx)
			t.Errorf("%s: openAndInitSchema returned a provider along with its refusal", database)
		}
		if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
			t.Errorf("%s: openAndInitSchema = %v, want ErrBelowRequiredMin", database, err)
		}
	}

	var name string
	switch err := admin.QueryRowContext(ctx, "SELECT schema_name FROM information_schema.schemata WHERE schema_name = 'floor_fresh'").Scan(&name); {
	case err == nil:
		t.Error("floor_fresh exists after the refused open")
	case !errors.Is(err, sql.ErrNoRows):
		t.Fatalf("look for floor_fresh: %v", err)
	}
}

// TestExternalBackendAddress pins what the external proxied-server refusal
// names as the server behind the proxy: its socket, or its host and port.
func TestExternalBackendAddress(t *testing.T) {
	for _, tc := range []struct {
		external configfile.ExternalDoltConfig
		want     string
	}{
		{external: configfile.ExternalDoltConfig{Host: "db.example", Port: 3306}, want: "db.example:3306"},
		{external: configfile.ExternalDoltConfig{Host: "::1", Port: 3306}, want: "[::1]:3306"},
		{external: configfile.ExternalDoltConfig{Host: "[::1]", Port: 3306}, want: "[::1]:3306"},
		{external: configfile.ExternalDoltConfig{Socket: "/run/dolt/dolt.sock"}, want: "/run/dolt/dolt.sock"},
	} {
		if got := externalBackendAddress(tc.external); got != tc.want {
			t.Errorf("externalBackendAddress(%+v) = %q, want %q", tc.external, got, tc.want)
		}
	}
}
