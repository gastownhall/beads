package dolt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

// raiseRequiredMin raises doltversion.RequiredMin above any Dolt the tests
// run against, so the test server stands in for one below the floor, and
// restores it when t ends. Its caller must not run in parallel.
func raiseRequiredMin(t *testing.T) {
	t.Helper()
	prev := doltversion.RequiredMin
	doltversion.RequiredMin = doltversion.MustParse("99.0.0")
	t.Cleanup(func() { doltversion.RequiredMin = prev })
}

// TestOpenServerConnectionRefusesBelowRequiredMin pins requireServerDoltVersion
// at each of openServerConnection's three paths against a real server: with
// the floor raised above the test server's version, the open of an existing
// database, the CreateIfMissing open of a missing one, and a gateway open
// (which skips the init connection) are all refused, and the missing
// database is not created.
func TestOpenServerConnectionRefusesBelowRequiredMin(t *testing.T) {
	skipIfNoServer(t)
	raiseRequiredMin(t)
	existing := fmt.Sprintf("test_floor_existing_%d", testServerPort)
	createTestDatabase(t, testServerPort, existing)
	missing := fmt.Sprintf("test_floor_missing_%d", testServerPort)
	assertDatabaseNotExists(t, testServerPort, missing)

	for _, tc := range []struct {
		name            string
		database        string
		createIfMissing bool
		gateway         bool
	}{
		{name: "existing database", database: existing},
		{name: "missing database with CreateIfMissing", database: missing, createIfMissing: true},
		{name: "gateway", database: existing, gateway: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Path:            t.TempDir(),
				ServerHost:      "127.0.0.1",
				ServerPort:      testServerPort,
				ServerUser:      "root",
				Database:        tc.database,
				CreateIfMissing: tc.createIfMissing,
				Gateway:         tc.gateway,
				MaxOpenConns:    1,
			}
			db, _, _, err := openServerConnection(context.Background(), cfg)
			if db != nil {
				_ = db.Close()
				t.Error("openServerConnection returned a pool along with its refusal")
			}
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				t.Fatalf("openServerConnection = %v, want ErrBelowRequiredMin", err)
			}
		})
	}
	assertDatabaseNotExists(t, testServerPort, missing)
}

// TestNewServerModeAutoStartRefusalHasNoStartAdvice: an auto-start that
// refuses a dolt below doltversion.RequiredMin is reported with the refusal,
// which names the binary and how to upgrade it, and without the advice an
// auto-start failure otherwise gets: `bd dolt start` refuses the same binary,
// and turning auto-start off does not help.
func TestNewServerModeAutoStartRefusalHasNoStartAdvice(t *testing.T) {
	isolateUnresolvedPortEnv(t)
	refusal := doltversion.CheckRequired(doltversion.MustParse("2.5.1"), `dolt binary "/usr/local/bin/dolt"`, false)
	orig := ensureRunningDetailed
	t.Cleanup(func() { ensureRunningDetailed = orig })
	ensureRunningDetailed = func(string) (int, bool, error) { return 0, false, refusal }
	beadsDir := t.TempDir()
	_, err := newServerMode(context.Background(), &Config{Database: "beads", BeadsDir: beadsDir,
		Path: filepath.Join(beadsDir, "dolt"), ServerHost: "127.0.0.1", AutoStart: true})
	if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
		t.Fatalf("newServerMode = %v, want the auto-start's ErrBelowRequiredMin", err)
	}
	if !strings.Contains(err.Error(), refusal.Error()) {
		t.Errorf("newServerMode = %q, want it to carry the refusal %q", err, refusal)
	}
	for _, advice := range []string{"bd dolt start", "dolt.auto-start"} {
		if strings.Contains(err.Error(), advice) {
			t.Errorf("newServerMode = %q, want no %q advice for a refused dolt", err, advice)
		}
	}
}
