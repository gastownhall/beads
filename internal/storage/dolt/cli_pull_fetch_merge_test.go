package dolt

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestDoltCLIPullFetchesThenMergesTrackingRef pins the CLI pull route's
// statement pair (bd-e2q8zs, AC-2): `dolt fetch [--user <u>] <remote>
// <branch>` in the subprocess — never `dolt pull`, whose three-way merge
// message quotes the stored remote URL, credentials included — followed by
// CALL DOLT_MERGE('<remote>/<branch>') on a pinned pool connection, with
// @@dolt_force_transaction_commit set (the variable `dolt pull` sets so a
// conflicted merge sticks in the working set) and reset before the
// connection is released.
//
// The fake dolt records its arguments and the DOLT_REMOTE_PASSWORD it was
// handed (fakeDoltOnPath); sqlmock keeps the SQL half honest — an ExpectQuery
// is only satisfied by a driver Query call, so a merge reverted to ExecContext
// fails this test loudly.
func TestDoltCLIPullFetchesThenMergesTrackingRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	const remoteURL = "git+ssh://git@example.com/org/repo.git"

	for _, tc := range []struct {
		name     string
		creds    *remoteCredentials
		wantCall string
	}{
		{"default", nil, "|fetch origin main|DOLT_REMOTE_PASSWORD="},
		{"credentialed", &remoteCredentials{username: "user", password: "client-secret"},
			"|fetch --user user origin main|DOLT_REMOTE_PASSWORD=client-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callLog := fakeDoltOnPath(t, remoteURL)
			beadsDir := unmanagedWorkspace(t)
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			// isS3Remote reads the remotes table before the transfer is built.
			mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
				WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("origin", remoteURL, nil))
			// The merge half of the pair, on the pinned pool connection.
			mock.ExpectExec("SET @@dolt_force_transaction_commit = 1").
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("CALL DOLT_MERGE(?)").
				WithArgs("origin/main").
				WillReturnRows(sqlmock.NewRows([]string{"hash", "fast_forward", "conflicts", "message"}).
					AddRow("abc123", "0", "0", "merge successful"))
			mock.ExpectExec("SET @@dolt_force_transaction_commit = 0").
				WillReturnResult(sqlmock.NewResult(0, 0))

			store := &DoltStore{
				serverMode: true,
				beadsDir:   beadsDir,
				dbPath:     filepath.Join(beadsDir, "dolt"),
				database:   "beads",
				remote:     "origin",
				branch:     "main",
				db:         db,
				cfg:        &Config{ServerHost: "127.0.0.1", ServerPort: 3310, BeadsDir: beadsDir},
			}
			if err := os.MkdirAll(store.CLIDir(), 0o755); err != nil {
				t.Fatalf("mkdir CLI dir: %v", err)
			}

			if err := store.doltCLIPull(context.Background(), "origin", tc.creds); err != nil {
				t.Fatalf("doltCLIPull: %v", err)
			}

			raw, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatalf("read fake dolt call log: %v", err)
			}
			calls := string(raw)
			if !strings.Contains(calls, tc.wantCall) {
				t.Errorf("dolt calls %q lack %q", calls, tc.wantCall)
			}
			if strings.Contains(calls, "|pull") {
				t.Errorf("the CLI pull route still runs `dolt pull`:\n%s", calls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations (the merge statement pair did not run as pinned): %v", err)
			}
		})
	}
}
