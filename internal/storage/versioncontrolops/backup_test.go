package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// restoreConn is a DBConn stub for BackupRestore: ExecContext records each
// statement's bind args and returns execErr.
type restoreConn struct {
	execErr error
	args    [][]any
}

func (c *restoreConn) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	c.args = append(c.args, args)
	return nil, c.execErr
}

func (c *restoreConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *restoreConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// errDoltRestore stands in for the engine's error. It deliberately does not
// quote the source URL: Dolt echoing the URL inside %w is a separate leak, and
// this test pins only the text BackupRestore itself interpolates.
var errDoltRestore = errors.New("dolt exploded")

// TestBackupRestoreSanitizesURLOnError pins that a failed restore never names
// the credentials or signed query of the backup URL in its own error text
// (#6341), on both the force and non-force branches, and that the driver error
// stays unwrappable.
func TestBackupRestoreSanitizesURLOnError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		forbidden []string
		want      string
	}{
		{name: "AC-1 userinfo", url: "aws://key:secret@bucket/path",
			forbidden: []string{"secret", "key:", "@"}, want: "aws://bucket/path"},
		{name: "AC-1 at sign in password", url: "aws://KEYAT:p@ssAT@bucket/db",
			forbidden: []string{"KEYAT", "ssAT", "@"}, want: "aws://bucket/db"},
		{name: "AC-2 signed query", url: "aws://bucket/path?X-Amz-Signature=deadbeefcafe&X-Amz-Credential=AKIAEXAMPLE",
			forbidden: []string{"deadbeefcafe", "AKIAEXAMPLE", "?"}, want: "aws://bucket/path"},
		{name: "AC-2 s3 signed query", url: "s3://bucket/db?X-Amz-Signature=s3sigvalue#frag",
			forbidden: []string{"s3sigvalue", "frag"}, want: "s3://bucket/db"},
		{name: "AC-4 slash in secret", url: "aws://AKIAUNP:wJal/K7MDUNP@bucket/db",
			forbidden: []string{"AKIAUNP", "wJal", "K7MDUNP"}, want: "aws:[redacted]"},
		{name: "AC-4 hash in password", url: "aws://KEYHASH:SEKHASH#tailH@b/p",
			forbidden: []string{"KEYHASH", "SEKHASH", "tailH"}, want: "aws:[redacted]"},
		{name: "AC-4 question mark in password", url: "aws://KEYQ:SEKQ?tailQ@b/p",
			forbidden: []string{"KEYQ", "SEKQ", "tailQ"}, want: "aws:[redacted]"},
		{name: "credential-free file URL", url: "file:///var/backups/x",
			want: "file:///var/backups/x"},
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", tc.name, force), func(t *testing.T) {
				conn := &restoreConn{execErr: errDoltRestore}
				err := BackupRestore(context.Background(), conn, tc.url, "beads", force)
				if err == nil {
					t.Fatal("BackupRestore returned nil, want an error")
				}
				msg := err.Error()
				if !strings.Contains(msg, "restore from backup "+tc.want) {
					t.Errorf("error = %q, want it to name the source as %q", msg, tc.want)
				}
				for _, s := range tc.forbidden {
					if strings.Contains(msg, s) {
						t.Errorf("error leaks %q: %q", s, msg)
					}
				}
				if !errors.Is(err, errDoltRestore) {
					t.Errorf("errors.Is(%v, errDoltRestore) = false, want the driver error wrapped", err)
				}
			})
		}
	}
}

// TestBackupRestorePassesURLVerbatim pins that redaction is error-text only:
// Dolt still receives the caller's URL byte-for-byte, credentials included.
func TestBackupRestorePassesURLVerbatim(t *testing.T) {
	for _, url := range []string{
		"aws://key:secret@bucket/path?X-Amz-Signature=deadbeefcafe",
		"aws://AKIAUNP:wJal/K7MDUNP@bucket/db",
	} {
		for _, force := range []bool{false, true} {
			conn := &restoreConn{}
			if err := BackupRestore(context.Background(), conn, url, "beads", force); err != nil {
				t.Fatalf("BackupRestore(%q, force=%v): %v", url, force, err)
			}
			if len(conn.args) != 1 {
				t.Fatalf("force=%v: ExecContext called %d times, want 1", force, len(conn.args))
			}
			if got := conn.args[0]; len(got) != 2 || got[0] != url || got[1] != "beads" {
				t.Errorf("force=%v: restore args = %q, want [%q %q]", force, got, url, "beads")
			}
		}
	}
}
