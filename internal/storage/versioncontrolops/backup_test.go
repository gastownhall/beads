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

// expectFailedRestore arms a sqlmock DBConn whose CALL DOLT_BACKUP('restore', …)
// exec fails with a sentinel error, runs BackupRestore, and returns the
// sentinel alongside the returned error so callers can assert errors.Is. The
// sentinel deliberately does not contain the URL: the wrapped Dolt text echoing
// it is a separate leak (see #6341 follow-ups), and an echoing sentinel would
// mask whether the %s operand itself was redacted.
//
// The WithArgs expectation also pins that the verbatim URL still reaches Dolt
// on the error path: if a future change scrubbed the bind argument,
// ExpectationsWereMet would fail the test.
func expectFailedRestore(t *testing.T, url string, force bool) (sentinel error, err error) {
	t.Helper()
	db, mock, mockErr := sqlmock.New()
	if mockErr != nil {
		t.Fatalf("sqlmock.New: %v", mockErr)
	}
	defer db.Close()
	sentinel = errors.New("dolt exploded")
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('restore'")).
		WithArgs(url, "beads").
		WillReturnError(sentinel)
	err = BackupRestore(context.Background(), db, url, "beads", force)
	if metErr := mock.ExpectationsWereMet(); metErr != nil {
		t.Errorf("force=%v: %v", force, metErr)
	}
	return sentinel, err
}

// checkRedactedRestoreError runs the assertions shared by #6341's
// redaction ACs: the error still says what failed and still unwraps to the
// driver sentinel, names the backup source when wantSource is set, and never
// contains any of the forbidden substrings (credentials, signatures).
func checkRedactedRestoreError(t *testing.T, url string, force bool, wantSource string, forbidden []string) {
	t.Helper()
	sentinel, err := expectFailedRestore(t, url, force)
	if err == nil {
		t.Fatalf("force=%v: BackupRestore returned nil, want an error", force)
	}
	if !strings.Contains(err.Error(), "restore from backup") {
		t.Errorf("force=%v: error %q lost the restore context", force, err)
	}
	if wantSource != "" && !strings.Contains(err.Error(), wantSource) {
		t.Errorf("force=%v: error %q no longer names the backup source %q", force, err, wantSource)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("force=%v: errors.Is lost the driver error: %v", force, err)
	}
	for _, f := range forbidden {
		if strings.Contains(err.Error(), f) {
			t.Errorf("force=%v: error leaks %q: %v", force, f, err)
		}
	}
}

// TestBackupRestoreSanitizesUserinfoInError is AC-1 of #6341: a failed
// restore must not echo the userinfo of the backup URL into its error, for
// either force branch.
func TestBackupRestoreSanitizesUserinfoInError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		url        string
		wantSource string
		forbidden  []string
	}{
		{
			name:      "aws credentials",
			url:       "aws://key:secret@bucket/path",
			forbidden: []string{"secret", "key:secret@"},
		},
		{
			// url.Parse takes everything up to the last '@' as userinfo, so
			// an '@' inside the password is not a parse failure: sanitizeURL
			// still strips it.
			name:      "at sign in password",
			url:       "aws://KEYAT:p@ssAT@bucket/db",
			forbidden: []string{"KEYAT", "p@ssAT"},
		},
		{
			// Credential-free local sources keep naming the destination.
			name:       "file url names the source",
			url:        "file:///var/backups/x",
			wantSource: "file:///var/backups/x",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, force := range []bool{false, true} {
				checkRedactedRestoreError(t, tc.url, force, tc.wantSource, tc.forbidden)
			}
		})
	}
}

// TestBackupRestoreSanitizesSignedQueryInError is AC-2 of #6341: the signed
// query parameters a cloud backup URL carries (X-Amz-Signature,
// X-Amz-Credential) must not reach the error text, for either force branch.
func TestBackupRestoreSanitizesSignedQueryInError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		forbidden []string
	}{
		{
			name:      "aws signed query",
			url:       "aws://bucket/path?X-Amz-Signature=deadbeefcafe&X-Amz-Credential=AKIAEXAMPLE",
			forbidden: []string{"deadbeefcafe", "AKIAEXAMPLE"},
		},
		{
			name:      "s3 signed query",
			url:       "s3://bucket/key?X-Amz-Signature=cafebabe99&X-Amz-Credential=AKIAEXAMPQ",
			forbidden: []string{"cafebabe99", "AKIAEXAMPQ"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, force := range []bool{false, true} {
				checkRedactedRestoreError(t, tc.url, force, "", tc.forbidden)
			}
		})
	}
}

// TestBackupRestorePassesVerbatimURLToDolt is AC-3 of #6341: redaction is
// error-text only. The URL bound for DOLT_BACKUP('restore', …) must be the
// caller's argument byte-for-byte: WithArgs fails the exec and
// ExpectationsWereMet fails the test if any pre-validation or scrubbing ever
// reaches the bind argument.
func TestBackupRestorePassesVerbatimURLToDolt(t *testing.T) {
	const url = "aws://key:secret@bucket/path?X-Amz-Signature=sigvalue123"
	for _, force := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('restore'")).
			WithArgs(url, "beads").
			WillReturnResult(sqlmock.NewResult(0, 0))
		if err := BackupRestore(context.Background(), db, url, "beads", force); err != nil {
			t.Errorf("force=%v: BackupRestore: %v", force, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("force=%v: restore did not receive the verbatim URL: %v", force, err)
		}
		db.Close()
	}
}

// TestBackupRestoreRedactsUnparseableCredentialedURLs is AC-4 of #6341:
// url.Parse rejects the real AWS secret shapes ('/', '#' or '?' inside the
// password), and sanitizeURL fails open on those, returning the raw string
// with the secret in it. The restore error must degrade to a redacted form
// there instead, for either force branch.
func TestBackupRestoreRedactsUnparseableCredentialedURLs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		forbidden []string
	}{
		{
			name:      "slash in secret",
			url:       "aws://AKIAUNP:wJal/K7MDUNP@bucket/db",
			forbidden: []string{"AKIAUNP", "wJal", "K7MDUNP"},
		},
		{
			name:      "hash in password",
			url:       "aws://KEYHASH:SEKHASH#tailH@b/p",
			forbidden: []string{"KEYHASH", "SEKHASH", "tailH"},
		},
		{
			name:      "question mark in password",
			url:       "aws://KEYQ:SEKQ?tailQ@b/p",
			forbidden: []string{"KEYQ", "SEKQ", "tailQ"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, force := range []bool{false, true} {
				checkRedactedRestoreError(t, tc.url, force, "", tc.forbidden)
			}
		})
	}
}

// TestBackupRestoreErrorRedactsURLCredentials pins that a failed restore never
// echoes the userinfo secret or a signed query from the backup URL (#6341).
func TestBackupRestoreErrorRedactsURLCredentials(t *testing.T) {
	const url = "aws://k:supersecret@b/p?X-Amz-Signature=sigvalue123"
	for _, force := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('restore'")).
			WillReturnError(fmt.Errorf("access denied"))

		err = BackupRestore(context.Background(), db, url, "beads", force)
		db.Close()
		if err == nil {
			t.Fatalf("force=%v: BackupRestore returned nil, want an error", force)
		}
		for _, secret := range []string{"supersecret", "sigvalue123"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("force=%v: error leaks %q: %v", force, secret, err)
			}
		}
	}
}
