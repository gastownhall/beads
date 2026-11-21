package uow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// forcedRemoteBackedOpen runs one provider open of a remote-backed database one
// migration behind, with forced consent (the `--force` override when forced,
// otherwise AllowRemoteMigrateEnv=1), up to the first migration statement, and
// returns what the gate printed.
func forcedRemoteBackedOpen(t *testing.T, proxied, forced bool) string {
	t.Helper()
	t.Cleanup(func() {
		schema.SetSharedMigrateConsent(false)
		schema.SetForceAllowRemoteMigrate(false)
	})
	t.Setenv(schema.SmartGateEnv, "0")
	if forced {
		t.Setenv(schema.AllowRemoteMigrateEnv, "0")
		schema.SetForceAllowRemoteMigrate(true)
	} else {
		t.Setenv(schema.AllowRemoteMigrateEnv, "1")
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
	expectSharedGateProbe(mock, 1, 1)
	expectVersionQuery(mock, 1) // forward drift, asked once the gate passes
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(errors.New("reached the first migration statement"))
	expectLockRelease(mock, lockName)

	p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", proxiedServerMode: proxied}
	var initErr error
	stderr := captureStderr(t, func() {
		initErr = p.initSchema(context.Background(), "beads")
	})
	if initErr == nil || !strings.Contains(initErr.Error(), "reached the first migration statement") {
		t.Fatalf("initSchema() error = %v, want the consented open to migrate", initErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	return stderr
}

// TestForcedRemoteMigrateWarning covers the warning the gate prints when forced
// consent lets a remote-backed migration through (bd-5hl9dg). Off proxied
// mode it is pinned byte for byte. On a proxied-server workspace the front
// door refuses `bd dolt push` and the bare `bd migrate`, so the warning must
// neither prescribe that push here nor name `bd migrate --force` as the
// consent when the operator typed `bd migrate schema --force`.
func TestForcedRemoteMigrateWarning(t *testing.T) {
	pending := schema.LatestVersion() - 1
	for _, tc := range []struct {
		name   string
		forced bool
		how    string
	}{
		{name: "--force override", forced: true, how: "bd migrate --force"},
		{name: "env hatch", how: schema.AllowRemoteMigrateEnv + "=1"},
	} {
		t.Run("server mode unchanged: "+tc.name, func(t *testing.T) {
			got := forcedRemoteBackedOpen(t, false, tc.forced)
			want := fmt.Sprintf("Warning: applying %d pending schema migration(s) to a remote-backed database (%s); only one clone should migrate, then `bd dolt push`\n", pending, tc.how)
			if !strings.Contains(got, want) {
				t.Errorf("server-mode warning changed.\n got:\n%s\nwant line:\n%s", got, want)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		forced bool
		how    string
	}{
		{name: "--force override", forced: true, how: "(" + schema.SharedConsentCommandForced + ")"},
		{name: "env hatch", how: "(" + schema.AllowRemoteMigrateEnv + "=1)"},
	} {
		t.Run("proxied: "+tc.name, func(t *testing.T) {
			got := forcedRemoteBackedOpen(t, true, tc.forced)
			for _, want := range []string{
				"to a remote-backed database " + tc.how,
				"only one clone of that remote should migrate",
				"from a workspace with direct access to the database",
				"this proxied-server workspace refuses it",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("proxied warning missing %q:\n%s", want, got)
				}
			}
			for _, refused := range []string{"(bd migrate --force)", "then `bd dolt push`\n"} {
				if strings.Contains(got, refused) {
					t.Errorf("proxied warning still says %q, which this workspace cannot run:\n%s", refused, got)
				}
			}
		})
	}
}

// TestSchemaBehindHintOnProxiedRemote covers the schema-mismatch hint a read
// gets when it cannot query a remote-backed database's current schema
// (bd-5hl9dg, the reviewer's `bd list` on a default proxied init). Its stock
// remedy, the bare `bd migrate schema`, refuses on a remote-backed database;
// on a proxied workspace it has to name the forced verb and the one-clone
// rule. Off proxied mode, and on a proxied database with no remote, the text
// is pinned unchanged.
func TestSchemaBehindHintOnProxiedRemote(t *testing.T) {
	behind := schema.LatestVersion() - 1
	stock := fmt.Sprintf("schema version mismatch: database is at v%d, binary expects v%d; bd does not migrate a database without explicit consent — run `bd migrate schema` in that workspace to migrate, keep using a bd release that matches schema v%d, or set BD_IGNORE_SCHEMA_SKEW=1 to open it anyway (queries that need what the pending migrations add still fail)",
		behind, schema.LatestVersion(), behind)

	read := func(t *testing.T, gateErr *schema.RemoteMigrateGateError) string {
		t.Helper()
		gateErr.CurrentVersion, gateErr.LatestVersion, gateErr.Pending = behind, schema.LatestVersion(), 1
		gateErr.Shared = true
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()
		ctx := context.Background()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin connection: %v", err)
		}
		defer conn.Close()
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		expectIssuesProjection(mock, behind, "arrival_seq")
		var readErr error
		_ = captureStderr(t, func() {
			readErr = readThroughRefusedMigration(ctx, conn, "beads", gateErr)
		})
		var behindErr *schema.SchemaBehindError
		if !errors.As(readErr, &behindErr) {
			t.Fatalf("readThroughRefusedMigration() = %v, want *schema.SchemaBehindError", readErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		return readErr.Error()
	}

	t.Run("server mode unchanged", func(t *testing.T) {
		if got := read(t, &schema.RemoteMigrateGateError{FallbackReason: "shared-store"}); got != stock {
			t.Errorf("server-mode hint changed.\n got: %s\nwant: %s", got, stock)
		}
	})
	t.Run("proxied without a remote unchanged", func(t *testing.T) {
		got := read(t, &schema.RemoteMigrateGateError{Decision: "shared-no-remote", Proxied: true})
		if got != stock {
			t.Errorf("proxied no-remote hint changed.\n got: %s\nwant: %s", got, stock)
		}
	})
	// A smart-gate stop or the data-behind stop keeps the stock remedy: the
	// bare verb reaches the gate and prints the stop, while `--force` is
	// checked before the gate decides and would skip it.
	for _, tc := range []struct {
		name    string
		gateErr *schema.RemoteMigrateGateError
	}{
		{"adopt", &schema.RemoteMigrateGateError{Decision: "adopt", Proxied: true}},
		{"adopt-ff", &schema.RemoteMigrateGateError{Decision: "adopt-ff", Proxied: true}},
		{"fork-skew", &schema.RemoteMigrateGateError{Decision: "fork-skew", Proxied: true, SkewVersions: []int{schema.LatestVersion()}}},
		{"data-behind", &schema.RemoteMigrateGateError{FallbackReason: "data-behind", Proxied: true}},
	} {
		t.Run("proxied "+tc.name+" stop keeps the stock remedy", func(t *testing.T) {
			got := read(t, tc.gateErr)
			if strings.Contains(got, schema.SharedConsentCommandForced) {
				t.Errorf("%s stop: read hint prescribes %q, which skips this stop:\n%s",
					tc.name, schema.SharedConsentCommandForced, got)
			}
		})
	}
	t.Run("proxied with a remote names the forced verb", func(t *testing.T) {
		got := read(t, &schema.RemoteMigrateGateError{FallbackReason: "shared-store", Proxied: true})
		for _, want := range []string{
			"`" + schema.SharedConsentCommandForced + "`",
			"ONE clone of that remote",
			"proxied-server",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("proxied remote-backed hint missing %q:\n%s", want, got)
			}
		}
		for _, refused := range []string{
			"run `bd migrate schema` in that workspace",
			// The front door refuses the env hatch on the bare verb (#6656).
			schema.AllowRemoteMigrateEnv + "=1 " + schema.SharedConsentCommand,
			"to read anyway",
		} {
			if strings.Contains(got, refused) {
				t.Errorf("proxied remote-backed hint still names %q, which refuses there:\n%s", refused, got)
			}
		}
	})
}
