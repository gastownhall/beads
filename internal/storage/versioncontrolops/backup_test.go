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

func TestIsBackupURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "http", raw: "http://backup.example/beads", want: true},
		{name: "https", raw: "https://backup.example/beads", want: true},
		{name: "file", raw: "file:///var/backups/beads", want: true},
		{name: "aws", raw: "aws://backup-bucket/beads", want: true},
		{name: "bracketed aws", raw: "aws://[dolt_table:my_bucket]/db", want: true},
		{name: "gs", raw: "gs://backup-bucket/beads", want: true},
		{name: "s3", raw: "s3://backup-bucket/beads", want: true},
		{name: "uppercase scheme", raw: "S3://bucket/db", want: false},
		{name: "git ssh", raw: "git+ssh://git@example.com/org/repo.git", want: false},
		{name: "git https", raw: "git+https://example.com/org/repo.git", want: false},
		{name: "relative path", raw: "backups/beads", want: false},
		{name: "absolute path", raw: "/var/backups/beads", want: false},
		{name: "empty", raw: "", want: false},
		{name: "scp style", raw: "git@example.com:org/repo.git", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBackupURL(tt.raw); got != tt.want {
				t.Errorf("IsBackupURL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestResolveBackupSource(t *testing.T) {
	dir := t.TempDir()
	dirURL, err := DirToFileURL(dir)
	if err != nil {
		t.Fatalf("DirToFileURL(%q): %v", dir, err)
	}
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write %q: %v", file, err)
	}

	tests := []struct {
		name    string
		source  string
		want    string
		wantErr string
	}{
		// A URL whose path does not exist locally: a stat would fail, so a
		// verbatim return proves the URL was never stat'ed.
		{name: "s3 URL passes through without stat", source: "s3://backup-bucket/beads", want: "s3://backup-bucket/beads"},
		{name: "bracketed aws URL passes through", source: "aws://[dolt_table:my_bucket]/db", want: "aws://[dolt_table:my_bucket]/db"},
		{name: "existing directory converts to file URL", source: dir, want: dirURL},
		{name: "missing directory", source: filepath.Join(dir, "missing"), wantErr: "backup source does not exist"},
		{name: "regular file", source: file, wantErr: "backup source is not a directory"},
		{name: "uppercase scheme is stat'ed as a path", source: "S3://bucket/db", wantErr: "backup source does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveBackupSource(tt.source)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveBackupSource(%q) = %q, want error containing %q", tt.source, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveBackupSource(%q) error %q does not contain %q", tt.source, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveBackupSource(%q): %v", tt.source, err)
			}
			if got != tt.want {
				t.Fatalf("ResolveBackupSource(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestRedactBackupURL(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "userinfo", source: "aws://key:secret@bucket/db", want: "aws://bucket/db"},
		{name: "signed query", source: "s3://bucket/db?X-Amz-Signature=abc&endpoint=https://minio.local", want: "s3://bucket/db"},
		{name: "fragment", source: "s3://bucket/db#frag", want: "s3://bucket/db"},
		{name: "user and query", source: "s3://user@bucket/db?x=1", want: "s3://bucket/db"},
		{name: "bracketed aws", source: "aws://[dynamo-table:bucket]/db", want: "aws://[dynamo-table:bucket]/db"},
		{name: "file URL", source: "file:///var/backups/x", want: "file:///var/backups/x"},
		{name: "plain path", source: "/var/backups/x", want: "/var/backups/x"},
		{name: "clean URL", source: "s3://bucket/db", want: "s3://bucket/db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactBackupURL(tt.source); got != tt.want {
				t.Errorf("redactBackupURL(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

// failingConn fails every statement so BackupRestore's error wrapper can be
// inspected.
type failingConn struct {
	err error
}

func (c *failingConn) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, c.err
}

func (c *failingConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *failingConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// A failed restore must not echo credentials a user put in the URL: userinfo
// and signed query parameters both reach BackupRestore verbatim because
// ResolveBackupSource passes URLs through untouched.
func TestBackupRestoreRedactsURLInError(t *testing.T) {
	tests := []struct {
		name  string
		force bool
	}{
		{name: "restore", force: false},
		{name: "force restore", force: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &failingConn{err: errors.New("dolt: restore failed")}
			err := BackupRestore(context.Background(), conn, "aws://key:secret@bucket/db", "beads", tt.force)
			if err == nil {
				t.Fatal("BackupRestore returned nil for a failing statement")
			}
			if !strings.Contains(err.Error(), "restore from backup aws://bucket/db") {
				t.Fatalf("BackupRestore error %q does not carry the redacted URL", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("BackupRestore error %q leaks the credential", err)
			}
		})
	}
}
