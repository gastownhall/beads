package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// sqlmockMaintenanceProvider is a proxied provider whose non-transactional
// seam hands the callback a connection from a sqlmock database.
type sqlmockMaintenanceProvider struct {
	uow.UnitOfWorkProvider
	db   *sql.DB
	runs int
}

func (p *sqlmockMaintenanceProvider) RunNonTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	p.runs++
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(ctx, conn)
}

func (p *sqlmockMaintenanceProvider) Close(context.Context) error { return nil }

// TestBackupSyncProxiedFailureNamesServerEnvironment pins bd-f68dud (GH#5953)
// for `bd backup sync` on a managed-local proxied server: the sync runs in the
// server's process, so a failed sync to the cloud destination the server has
// registered, while bd holds that scheme's variables, reports the original
// failure plus one DOLT_BACKUP note, in text and in JSON. The destination is
// the server's registration, not the local dolt-backup.json. A sync that
// succeeds stays a success and looks nothing up, and an external proxied
// server keeps its capability refusal.
//
// Cannot be parallel: mutates the jsonOutput, proxiedServerMode and
// uowProvider globals.
func TestBackupSyncProxiedFailureNamesServerEnvironment(t *testing.T) {
	const serverURL = "s3://bucket/backups"
	for _, tc := range []struct {
		name     string
		json     bool
		sidecar  string
		syncErr  error
		wantText string
	}{
		{name: "text failure", sidecar: `{}`, syncErr: errors.New("AccessDenied: the bucket refused the request")},
		{name: "json failure", json: true, sidecar: `{}`, syncErr: errors.New("AccessDenied: the bucket refused the request")},
		{name: "success", sidecar: `{}`, wantText: "Backup synced"},
		{name: "external server", sidecar: `{"external":{"host":"db.example.com","port":3306}}`, wantText: "not supported in proxied-server mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := prepareBackupStatusTest(t)
			clearBackupNoteCloudEnv(t)
			t.Setenv("AWS_ACCESS_KEY_ID", "sentinel-key")
			proxiedServerMode = true
			jsonOutput = tc.json
			if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(beadsDir), []byte(tc.sidecar), 0o600); err != nil {
				t.Fatalf("write proxied sidecar: %v", err)
			}
			// The local record names another destination; the server's
			// registration is what the sync used.
			if err := saveDoltBackupConfig("file:///somewhere/else"); err != nil {
				t.Fatalf("save local backup config: %v", err)
			}

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			sync := mock.ExpectExec("CALL DOLT_BACKUP\\('sync'").WithArgs(proxiedBackupTargetName)
			if tc.syncErr != nil {
				sync.WillReturnError(tc.syncErr)
			} else {
				sync.WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectQuery("SELECT url FROM dolt_backups WHERE name").WithArgs(proxiedBackupTargetName).
				WillReturnRows(sqlmock.NewRows([]string{"url"}).AddRow(serverURL))

			oldProvider := uowProvider
			t.Cleanup(func() { uowProvider = oldProvider })
			provider := &sqlmockMaintenanceProvider{db: db}
			uowProvider = provider

			var runErr error
			stdout, stderr := captureStdio(t, func() { runErr = runBackupSyncProxied(context.Background()) })
			out := stdout + stderr

			if tc.syncErr == nil {
				if !strings.Contains(out, tc.wantText) {
					t.Fatalf("output = %q, want %q", out, tc.wantText)
				}
				if strings.Contains(out, "runs in the Dolt server's process") {
					t.Fatalf("output = %q carries a server-environment note", out)
				}
				if tc.sidecar != `{}` {
					if runErr == nil || provider.runs != 0 {
						t.Fatalf("external server: err = %v, provider runs = %d; want the refusal before any server call", runErr, provider.runs)
					}
					return
				}
				if runErr != nil {
					t.Fatalf("runBackupSyncProxied = %v, want success", runErr)
				}
				if err := mock.ExpectationsWereMet(); err == nil || !strings.Contains(err.Error(), "dolt_backups") {
					t.Fatalf("a successful sync looked its destination up (unmet expectations: %v)", err)
				}
				return
			}
			if runErr == nil {
				t.Fatal("runBackupSyncProxied succeeded, want the sync failure")
			}
			want := "backup sync failed: sync backup " + proxiedBackupTargetName + ": AccessDenied: the bucket refused the request; " +
				"CALL DOLT_BACKUP runs in the Dolt server's process, which takes AWS_* from its own environment, not from bd's; " +
				"if the server lacks current credentials, set them in the environment the server starts from and restart it"
			if !strings.Contains(out, want) {
				t.Fatalf("output = %q, want %q", out, want)
			}
			if tc.json && !strings.Contains(stdout, `"error"`) {
				t.Fatalf("stdout = %q, want the JSON error object", stdout)
			}
			for _, leak := range []string{serverURL, "sentinel", "no backup destination configured"} {
				if strings.Contains(out, leak) {
					t.Fatalf("output = %q contains %q", out, leak)
				}
			}
		})
	}
}

// clearBackupNoteCloudEnv unsets the cloud credential variables a runner may
// carry, so only the ones a test sets decide the note. Originals are restored
// on cleanup.
func clearBackupNoteCloudEnv(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		for _, prefix := range []string{"AZURE_STORAGE_", "AWS_", "GOOGLE_", "GCS_", "OCI_", "DOLT_REMOTE_"} {
			if strings.HasPrefix(key, prefix) {
				t.Setenv(key, "")
				_ = os.Unsetenv(key)
				break
			}
		}
	}
}
