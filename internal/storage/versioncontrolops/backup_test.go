package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestDirToFileURLRejectsSchemes pins the guard that stops a URL being turned
// into a bogus local path. filepath.Abs treats "https://host/repo" as a
// relative path, so without the check this helper would hand DOLT_BACKUP
// "file:///cwd/https:/host/repo" and the failure would name a directory nobody
// asked for. No caller can reach it today — `bd backup restore` stats its
// argument first — but every caller is a restore path, and restore-from-a-remote
// is the open capability that would reach it.
func TestDirToFileURLRejectsSchemes(t *testing.T) {
	for _, dir := range []string{
		"https://doltremoteapi.dolthub.com/user/repo",
		"file:///already/a/url",
		"aws://bucket/key",
		"gs://bucket/key",
	} {
		if got, err := DirToFileURL(dir); err == nil {
			t.Errorf("DirToFileURL(%q) = %q, want an error naming the scheme", dir, got)
		}
	}

	got, err := DirToFileURL("backups/nightly")
	if err != nil {
		t.Fatalf("DirToFileURL on a plain relative dir: %v", err)
	}
	if !strings.HasPrefix(got, "file://") || strings.Contains(got, "://backups") {
		t.Fatalf("DirToFileURL(%q) = %q", "backups/nightly", got)
	}
}

func TestExtractAddressConflictName(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("connection refused"),
			want: "",
		},
		{
			name: "standard conflict",
			err:  fmt.Errorf("Error 1105: address conflict with a remote: 'default' -> file:///backup"),
			want: "default",
		},
		{
			name: "full dolt error format from doc comment",
			err:  fmt.Errorf("Error 1105: address conflict with a remote: 'backup_export' -> file:///some/path"),
			want: "backup_export",
		},
		{
			name: "missing closing quote",
			err:  fmt.Errorf("address conflict with a remote: 'oops"),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractAddressConflictName(tt.err); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBackupToDirRefusesMissingOrFileDestination pins the local-directory
// precondition every auto-backup caller (embedded, sql-server and proxied)
// now shares. It fails before any SQL is issued, so nil connections are safe:
// a regression that reached the server first would panic here.
func TestBackupToDirRefusesMissingOrFileDestination(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, want string
	}{
		{name: "missing", dir: filepath.Join(dir, "absent"), want: "does not exist"},
		{name: "file", dir: file, want: "is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := BackupToDir(context.Background(), nil, nil, tc.dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BackupToDir(%q) error = %v, want one containing %q", tc.dir, err, tc.want)
			}
		})
	}
}

// dirEntryNames lists dir's entries by name, for the no-side-effect check in
// TestBackupRestoreRefusesSourceWithoutManifest.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestBackupRestoreRefusesSourceWithoutManifest pins #5972 at the choke point
// every restore path (direct store, embedded store, proxied route, bootstrap
// restore) shares: DOLT_BACKUP('restore', '--force', …) drops the target
// database before reading the source, so an ordinary directory — empty, or
// holding exports, or bare except a `manifest` subdirectory — must be refused
// here, before any SQL is issued. A nil connection is the idiom
// TestBackupToDirRefusesMissingOrFileDestination uses: a regression that
// reached SQL would panic, so the assertions prove the guard ran first.
func TestBackupRestoreRefusesSourceWithoutManifest(t *testing.T) {
	base := t.TempDir()
	jsonlDir := filepath.Join(base, "jsonl")
	if err := os.MkdirAll(jsonlDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jsonlDir, "issues.jsonl"), []byte("{\"id\":\"bd-1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestDir := filepath.Join(base, "manifest-dir")
	if err := os.MkdirAll(filepath.Join(manifestDir, "manifest"), 0o750); err != nil {
		t.Fatal(err)
	}
	emptyDir := filepath.Join(base, "empty")
	if err := os.MkdirAll(emptyDir, 0o750); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		dir  string
	}{
		{name: "empty_dir", dir: emptyDir},
		{name: "jsonl_only", dir: jsonlDir},
		{name: "manifest_is_a_directory", dir: manifestDir},
	} {
		for _, force := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/force=%v", tc.name, force), func(t *testing.T) {
				before := dirEntryNames(t, tc.dir)
				err := BackupRestore(context.Background(), nil, "file://"+tc.dir, "beads", force)
				if !errors.Is(err, ErrNotDoltBackup) {
					t.Fatalf("BackupRestore(%q, force=%v) error = %v, want ErrNotDoltBackup", tc.dir, force, err)
				}
				after := dirEntryNames(t, tc.dir)
				if strings.Join(after, ",") != strings.Join(before, ",") {
					t.Errorf("refused restore wrote into the backup source %s: before %v, after %v (Dolt opens the source with Mkdir(oldgen) before failing)", tc.dir, before, after)
				}
			})
		}
	}

	// The missing-path and not-a-directory branches come from the same
	// pre-flight, with the two stores' wording, not ErrNotDoltBackup.
	if err := CheckBackupDir(filepath.Join(base, "absent")); err == nil || !strings.Contains(err.Error(), "does not exist") || errors.Is(err, ErrNotDoltBackup) {
		t.Errorf("CheckBackupDir on a missing path = %v, want the does-not-exist wording", err)
	}
	file := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckBackupDir(file); err == nil || !strings.Contains(err.Error(), "is not a directory") || errors.Is(err, ErrNotDoltBackup) {
		t.Errorf("CheckBackupDir on a regular file = %v, want the not-a-directory wording", err)
	}
}

// TestBackupRestoreIssuesRestoreForBackupWithManifest is the positive control
// for the same guard: a directory that holds a regular `manifest` file looks
// like a Dolt backup, so the CALL still reaches the connection — for both the
// --force and the plain form — and a non-file:// URL is not newly blocked.
// The manifest's contents are Dolt's to judge, not the guard's; presence is
// what the guard checks.
func TestBackupRestoreIssuesRestoreForBackupWithManifest(t *testing.T) {
	dir := t.TempDir()
	// A real backup sync has written chunks here; the guard only stats the
	// manifest, so an empty one stands in for the fixture.
	if err := os.WriteFile(filepath.Join(dir, "manifest"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	url := "file://" + dir

	for _, tc := range []struct {
		name  string
		force bool
	}{
		{name: "force", force: true},
		{name: "no_force", force: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			if tc.force {
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('restore', '--force', ?, ?)")).
					WithArgs(url, "beads").
					WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('restore', ?, ?)")).
					WithArgs(url, "beads").
					WillReturnResult(sqlmock.NewResult(0, 1))
			}

			if err := BackupRestore(context.Background(), db, url, "beads", tc.force); err != nil {
				t.Fatalf("BackupRestore: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}

	// Remote-scheme restore is not a beads capability today, but the guard must
	// not be what blocks it when it becomes one: a non-file:// URL passes
	// through to the connection.
	t.Run("non_file_url", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('restore', '--force', ?, ?)")).
			WithArgs("https://doltremoteapi.dolthub.com/user/repo", "beads").
			WillReturnResult(sqlmock.NewResult(0, 1))

		if err := BackupRestore(context.Background(), db, "https://doltremoteapi.dolthub.com/user/repo", "beads", true); err != nil {
			t.Fatalf("BackupRestore: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
