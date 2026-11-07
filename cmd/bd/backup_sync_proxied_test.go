package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// fakeBackupSyncProvider hands runProxiedNonTx a connection from a sqlmock
// database, standing in for the proxied server the sync would run on.
type fakeBackupSyncProvider struct {
	uow.UnitOfWorkProvider
	db *sql.DB
}

func (f *fakeBackupSyncProvider) RunNonTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(ctx, conn)
}

func (f *fakeBackupSyncProvider) Close(context.Context) error { return nil }

func newBackupSyncTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "bd"}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output JSON")
	backup := &cobra.Command{Use: "backup"}
	backup.AddCommand(backupSyncCmd)
	root.AddCommand(backup)
	return root
}

// executeBackupSyncCommand runs `bd backup sync` under the stdio capture;
// extra args (e.g. --json) select the output mode, because binding the flag
// resets the package variable to its default.
func executeBackupSyncCommand(t *testing.T, root *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	root.SetArgs(append([]string{"backup", "sync"}, args...))
	return executeBackupStatusCommand(t, root)
}

// newBackupSyncFailureDB stands in for the proxied server: a clean working
// set (nothing to commit before the backup), a CALL DOLT_BACKUP('sync', ?)
// that fails the way a destination without credentials does, and the
// destination the server's dolt_backups row records — an s3 URL whose
// credentials bd holds in its own environment here.
func newBackupSyncFailureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("FROM dolt_status").WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(0))
	mock.ExpectExec("CALL DOLT_BACKUP").WillReturnError(errors.New("auth handler failed"))
	mock.ExpectQuery("SELECT url FROM dolt_backups").WillReturnRows(
		sqlmock.NewRows([]string{"url"}).AddRow("s3://bucket/beads"))
	return db
}

// TestBackupSyncProxiedFailureNamesServerEnvironment pins the
// server-environment note on a failed proxied backup sync (GH#5953): the
// sync runs as CALL DOLT_BACKUP in the proxied server's process, which takes
// the destination's credentials from its own environment, so the failure
// says so — in text and JSON — naming the variable family of the destination
// the server's dolt_backups row records, never a path or a credential value.
// A topology the route refuses, and a sync that succeeds, stay clean.
//
// Cannot be parallel: mutates the proxiedServerMode and uowProvider globals
// and captures stdio under stdioMutex.
func TestBackupSyncProxiedFailureNamesServerEnvironment(t *testing.T) {
	prepareProxiedSync := func(t *testing.T, sidecar string, db *sql.DB) {
		t.Helper()
		beadsDir := prepareBackupStatusTest(t)
		proxiedServerMode = true
		if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(beadsDir), []byte(sidecar), 0o600); err != nil {
			t.Fatalf("write proxied sidecar: %v", err)
		}
		oldProvider, oldCtx := uowProvider, rootCtx
		uowProvider = &fakeBackupSyncProvider{db: db}
		// The bare test root runs no PersistentPreRun, so the RunE's
		// `ctx := rootCtx` would hand a nil context to the sync path
		// (backup_cap_test.go does the same).
		rootCtx = context.Background()
		t.Cleanup(func() {
			uowProvider = oldProvider
			rootCtx = oldCtx
		})
	}

	t.Run("text output names the server environment", func(t *testing.T) {
		db := newBackupSyncFailureDB(t)
		prepareProxiedSync(t, `{}`, db)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fresh-secret")

		stdout, stderr, err := executeBackupSyncCommand(t, newBackupSyncTestRoot())

		if err == nil {
			t.Fatal("expected the sync to fail")
		}
		if !containsAll(stderr,
			"backup sync failed: sync backup default: auth handler failed",
			"; CALL DOLT_BACKUP runs in the Dolt server's process, which takes AWS_* from its own environment, not from bd's",
			"restart it") {
			t.Fatalf("stderr %q should carry the sync's own error and the server-environment note", stderr)
		}
		if containsAll(stdout, "server's process") {
			t.Fatalf("text errors belong on stderr; stdout %q", stdout)
		}
	})

	t.Run("JSON output keeps the note", func(t *testing.T) {
		db := newBackupSyncFailureDB(t)
		prepareProxiedSync(t, `{}`, db)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")

		stdout, _, err := executeBackupSyncCommand(t, newBackupSyncTestRoot(), "--json")

		if err == nil {
			t.Fatal("expected the sync to fail")
		}
		if !containsAll(stdout,
			"backup sync failed: sync backup default: auth handler failed",
			"CALL DOLT_BACKUP runs in the Dolt server's process",
			"AWS_*") {
			t.Fatalf("stdout %q should carry the error JSON with the server-environment note", stdout)
		}
	})

	t.Run("refused topology carries no note", func(t *testing.T) {
		db := newBackupSyncFailureDB(t)
		prepareProxiedSync(t, `{"external":{"host":"db.example.com","port":3306}}`, db)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")

		stdout, stderr, err := executeBackupSyncCommand(t, newBackupSyncTestRoot())

		if err == nil {
			t.Fatal("expected the route to refuse the external topology")
		}
		if containsAll(stdout+stderr, "server's process") {
			t.Fatalf("a refusal is not a credential explanation; stdout %q stderr %q", stdout, stderr)
		}
	})

	t.Run("successful sync stays clean", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.MatchExpectationsInOrder(false)
		mock.ExpectQuery("FROM dolt_status").WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(0))
		mock.ExpectExec("CALL DOLT_BACKUP").WillReturnResult(sqlmock.NewResult(0, 0))
		prepareProxiedSync(t, `{}`, db)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFRESH")

		stdout, stderr, err := executeBackupSyncCommand(t, newBackupSyncTestRoot())

		if err != nil {
			t.Fatalf("sync should succeed: %v (stderr %q)", err, stderr)
		}
		if !containsAll(stdout, "Backup synced in") {
			t.Fatalf("stdout %q should report the successful sync", stdout)
		}
		if containsAll(stdout+stderr, "server's process") {
			t.Fatalf("a successful sync explains nothing; stdout %q stderr %q", stdout, stderr)
		}
	})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
