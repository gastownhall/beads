package dolt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestBackupSyncFailureNamesServerEnvironment pins bd-f68dud (GH#5953) for
// backups: CALL DOLT_BACKUP('sync') runs in the Dolt server's process, so a
// failed server-mode sync to a registered cloud destination, while bd holds
// that scheme's variables, carries one DOLT_BACKUP note naming them. The
// destination is the one the server has registered (dolt_backups). A lookup
// that fails or finds nothing, a file destination, embedded mode, and a
// failure to set up the sync connection (a bad DSN, an unreachable server)
// leave the error unchanged and look nothing up. The note
// avoids the "no backup"/"not found" wording the backup commands match on.
func TestBackupSyncFailureNamesServerEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      string
		lookup   error
		env      map[string]string
		embedded bool
		connStr  string
		vars     string
	}{
		{name: "s3 destination", url: "s3://bucket/backups", env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}, vars: "AWS_*"},
		{name: "gs destination", url: "gs://bucket/backups", env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/sentinel/key.json"}, vars: "GOOGLE_*"},
		{name: "lookup fails", lookup: fmt.Errorf("dolt_backups unavailable"), env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "backup not registered", env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "file destination", url: "file:///tmp/backups", env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "embedded mode", url: "s3://bucket/backups", embedded: true, env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "connection setup fails", url: "s3://bucket/backups", connStr: "no-database-separator", env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
		{name: "server unreachable", url: "s3://bucket/backups", connStr: unroutableConnStr, env: map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearCloudAuthEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			q := mock.ExpectQuery("SELECT url FROM dolt_backups WHERE name").WithArgs("default")
			switch {
			case tc.lookup != nil:
				q.WillReturnError(tc.lookup)
			case tc.url == "":
				q.WillReturnRows(sqlmock.NewRows([]string{"url"}))
			default:
				q.WillReturnRows(sqlmock.NewRows([]string{"url"}).AddRow(tc.url))
			}
			// The sync connection reaches a server that refuses the CALL
			// itself, unless the case breaks the connection's setup.
			connStr := tc.connStr
			if connStr == "" {
				connStr = refusingSQLServer(t)
			}
			store := &DoltStore{serverMode: !tc.embedded, db: db, database: "beads", connStr: connStr}

			err = store.BackupSync(context.Background(), "default")

			if err == nil {
				t.Fatal("BackupSync succeeded, want the sync to fail")
			}
			if tc.connStr == "" && !strings.Contains(err.Error(), "sync backup default: Error 1105 (HY000): "+refusedCallMessage) {
				t.Fatalf("BackupSync = %v, want the refused sync in its existing context", err)
			}
			if tc.connStr == unroutableConnStr && !strings.Contains(err.Error(), "sync backup default") {
				t.Fatalf("BackupSync = %v, want the dial failure in the sync's context", err)
			}
			if tc.connStr != "" {
				if lookupErr := mock.ExpectationsWereMet(); lookupErr == nil {
					t.Fatal("a sync that never reached the server looked its destination up")
				}
			}
			checkServerEnvNote(t, err, "DOLT_BACKUP", tc.vars, tc.url)
			for _, phrase := range []string{"no backup", "not found"} {
				if strings.Contains(err.Error(), phrase) {
					t.Fatalf("BackupSync = %q contains %q, which the backup commands read as an unconfigured destination", err, phrase)
				}
			}
		})
	}
}
