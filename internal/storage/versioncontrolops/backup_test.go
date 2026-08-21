package versioncontrolops

import (
	"context"
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
