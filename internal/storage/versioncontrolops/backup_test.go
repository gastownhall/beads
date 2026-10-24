package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

		// file:// is the one backup URL that names a local directory, so it is
		// the one that is stat'ed. It still passes through verbatim; what the
		// stat buys is the refusal, because DOLT_BACKUP creates a missing
		// file:// source rather than failing on it.
		{name: "file URL to an existing directory passes through", source: "file://" + dir, want: "file://" + dir},
		{name: "file URL to a missing directory", source: "file://" + filepath.Join(dir, "missing"), wantErr: "backup source does not exist"},
		{name: "file URL to a regular file", source: "file://" + file, wantErr: "backup source is not a directory"},
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

// TestResolveBackupSourceRefusesFileURLsDoltReadsElsewhere pins the file://
// paths a stat and DOLT_BACKUP read differently. Every directory below exists
// where the stat looks, so the stat alone passes each row, while Dolt opens a
// different directory: it creates it and, under --force, drops the live
// database before the restore fails. The refusal is all that stops that.
func TestResolveBackupSourceRefusesFileURLsDoltReadsElsewhere(t *testing.T) {
	work := t.TempDir()
	// Dolt resolves a relative path against its data directory, never this
	// one, so relbak exists here and not where Dolt would look.
	t.Chdir(work)
	if err := os.Mkdir("relbak", 0o750); err != nil {
		t.Fatalf("mkdir relbak: %v", err)
	}

	const relative, notPlain = "not an absolute file:// path", "not a plain file:// path"
	type row struct{ source, wantErr string }
	rows := []row{
		{"file://relbak", relative},
		{"file://./relbak", relative},
	}
	// Dolt decodes %41 to A, ends the path at # or ?, and reads \ as /, so it
	// opens bkA, bk or bk/1.
	escaped := []string{"bk%41", "bk#1"}
	if runtime.GOOS != "windows" { // ? is not legal in a Windows file name, and \ is the separator
		escaped = append(escaped, "bk?1", `bk\1`)
	}
	for _, name := range escaped {
		dir := filepath.Join(work, name)
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
		rows = append(rows, row{"file://" + dir, notPlain})
	}

	for _, r := range rows {
		got, err := ResolveBackupSource(r.source)
		if err == nil || !strings.Contains(err.Error(), r.wantErr) {
			t.Errorf("ResolveBackupSource(%q) = %q, %v; want a refusal containing %q", r.source, got, err, r.wantErr)
		}
	}

	// The remedy the refusal names works: passed as a directory, the same
	// relative path is resolved here and sent as an absolute file:// URL.
	want, err := DirToFileURL("relbak")
	if err != nil {
		t.Fatalf("DirToFileURL(relbak): %v", err)
	}
	if got, err := ResolveBackupSource("relbak"); err != nil || got != want {
		t.Errorf("ResolveBackupSource(relbak) = %q, %v; want %q", got, err, want)
	}
}

// TestResolveBackupSourceStatsTheCleanedFileURLPath pins that the stat looks
// where Dolt will. Dolt cleans a file:// path lexically, so link/../bk is the
// bk beside link, not the one beside link's target. The raw path reaches an
// existing directory through the symlink; the cleaned one is missing, and Dolt
// would create it.
func TestResolveBackupSourceStatsTheCleanedFileURLPath(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "far", "near")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("mkdir %q: %v", target, err)
	}
	if err := os.Mkdir(filepath.Join(work, "far", "bk"), 0o750); err != nil {
		t.Fatalf("mkdir far/bk: %v", err)
	}
	link := filepath.Join(work, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}

	sep := string(filepath.Separator)
	source := "file://" + link + sep + ".." + sep + "bk"
	if got, err := ResolveBackupSource(source); err == nil || !strings.Contains(err.Error(), "backup source does not exist") {
		t.Errorf("ResolveBackupSource(%q) = %q, %v; want the missing-source refusal for %s",
			source, got, err, filepath.Join(work, "bk"))
	}
}

// TestResolveBackupSourceRedactsTheSourceItQuotes covers the arm a credentialed
// URL actually lands on. IsBackupURL is case-sensitive by design, so S3:// —
// and az://, which backupSchemes deliberately omits — is stat'ed as a path,
// and the *fs.PathError carries its own copy of the source. Wrapping it with
// %w put the whole thing, credentials included, into the error a failed
// restore prints: the same leak the redaction in BackupRestore exists to close,
// one arm over.
func TestResolveBackupSourceRedactsTheSourceItQuotes(t *testing.T) {
	const (
		key    = "AKIAEXAMPLE"
		secret = "wJalrXUtnFEMI/K7MDENG"
	)

	for _, source := range []string{
		"S3://" + key + ":" + secret + "@bucket/db",
		"az://" + key + ":" + secret + "@container/db",
	} {
		if IsBackupURL(source) {
			t.Fatalf("fixture %q is a recognized backup URL, so it passes through rather than reaching the stat arm", source)
		}

		got, err := ResolveBackupSource(source)
		if err == nil {
			t.Fatalf("ResolveBackupSource(%q) = %q, want the stat arm's refusal", source, got)
		}
		if !strings.Contains(err.Error(), "backup source does not exist") {
			t.Errorf("ResolveBackupSource(%q) error %q dropped the load-bearing prefix", source, err)
		}
		for _, leaked := range []string{key, secret} {
			if strings.Contains(err.Error(), leaked) {
				t.Errorf("ResolveBackupSource(%q) error %q echoes %q", source, err, leaked)
			}
		}
		// The reason stays wrapped, so a caller can still classify the failure
		// without parsing the message.
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ResolveBackupSource(%q) error %q no longer answers errors.Is(fs.ErrNotExist)", source, err)
		}
	}
}

func TestResolveBackupSourceRedactsNotDirectorySource(t *testing.T) {
	t.Chdir(t.TempDir())
	const source = "S3://AKIAEXAMPLE:hunter2pass@bucket/db"
	if err := os.MkdirAll(filepath.Dir(source), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveBackupSource(source)
	if err == nil || !strings.Contains(err.Error(), "backup source is not a directory: S3://bucket/db") {
		t.Fatalf("ResolveBackupSource(%q) = %v, want a redacted not-directory refusal", source, err)
	}
	for _, secret := range []string{"AKIAEXAMPLE", "hunter2pass"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("ResolveBackupSource(%q) error %q echoes %q", source, err, secret)
		}
	}
}

func TestResolveBackupSourceRedactsUnsafeFileURLRefusals(t *testing.T) {
	tests := []struct {
		name, source, want string
	}{
		{
			name:   "relative path",
			source: "file://AKIAEXAMPLE:hunter2pass@relative-backup",
			want:   "file://relative-backup",
		},
		{
			name:   "encoded or delimited path",
			source: "file:///tmp/backup%41?token=hunter2pass",
			want:   "file:///tmp/backup%41",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveBackupSource(tt.source)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolveBackupSource(%q) = %v, want refusal containing %q", tt.source, err, tt.want)
			}
			for _, secret := range []string{"AKIAEXAMPLE", "hunter2pass"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("ResolveBackupSource(%q) error %q echoes %q", tt.source, err, secret)
				}
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
		{name: "at sign in file path", source: "file:///srv/backups@2024/db", want: "file:///srv/backups@2024/db"},
		{name: "userinfo in file authority", source: "file://key:secret@host/backups/x", want: "file://host/backups/x"},
		{name: "plain path", source: "/var/backups/x", want: "/var/backups/x"},
		{name: "malformed URL with userinfo", source: "aws:/k:s@b/db", want: "aws:[redacted]"},
		{name: "clean URL", source: "s3://bucket/db", want: "s3://bucket/db"},

		// One row per separator class, with the separator INSIDE the secret.
		// A secret is arbitrary bytes, so each of these used to defeat the
		// redaction: the authority was bounded at the first "/" and the
		// "?#" cut ran before the "@" search. The "/" row is the common
		// case, not a corner one — AWS secret access keys are base64.
		{name: "slash in secret", source: "aws://AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG@bucket/db", want: "aws://bucket/db"},
		{name: "fragment in secret", source: "aws://AKIAEXAMPLE:sec#ret@bucket/db", want: "aws://bucket/db"},
		{name: "question mark in secret", source: "aws://AKIAEXAMPLE:sec?ret@bucket/db", want: "aws://bucket/db"},
		{name: "at sign in secret", source: "aws://AKIAEXAMPLE:sec@ret@bucket/db", want: "aws://bucket/db"},

		// Over-stripping is the direction this is allowed to fail in: an "@"
		// in a query parameter costs the host and path in a debug message,
		// where taking the first "@" instead would leak every secret that
		// contains one.
		{name: "at sign in query over-strips", source: "s3://bucket/db?endpoint=user@minio.local", want: "s3://minio.local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactBackupURL(tt.source); got != tt.want {
				t.Errorf("RedactBackupURL(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

// TestRedactBackupURLLeaksNoSecret is the property the table rows are examples
// of: whatever shape the userinfo takes, neither the access key nor the secret
// survives into the redacted string. A table row pins one spelling; this pins
// the claim the function's doc comment makes.
func TestRedactBackupURLLeaksNoSecret(t *testing.T) {
	const (
		key    = "AKIAEXAMPLE"
		secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCY?EXAMPLE#KEY@x"
	)

	for _, source := range []string{
		"aws://" + key + ":" + secret + "@bucket/db",
		"s3://" + key + ":" + secret + "@bucket/db?X-Amz-Signature=abc",
		"aws://" + secret + "@bucket/db",
	} {
		got := RedactBackupURL(source)
		if strings.Contains(got, secret) {
			t.Errorf("RedactBackupURL(%q) = %q, still carries the secret", source, got)
		}
		if strings.Contains(got, key) {
			t.Errorf("RedactBackupURL(%q) = %q, still carries the access key", source, got)
		}
	}
}

// failingConn fails every statement so BackupRestore's error wrapper can be
// inspected.
type failingConn struct {
	err   error
	calls int
}

func (c *failingConn) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	c.calls++
	return nil, c.err
}

func (c *failingConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *failingConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

type echoingConn struct {
	calls int
	cause error
}

func (c *echoingConn) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	c.calls++
	for _, arg := range args {
		if raw, ok := arg.(string); ok && strings.Contains(raw, "://") {
			return nil, fmt.Errorf("dolt rejected backup URL %q: %w", raw, c.cause)
		}
	}
	return nil, c.cause
}

func (c *echoingConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *echoingConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// reserializingEchoConn mirrors the http/https Dolt factory: it parses the
// supplied URL and includes url.URL.String() rather than the caller's original
// spelling in its error.
type reserializingEchoConn struct {
	calls int
	cause error
}

func (c *reserializingEchoConn) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	c.calls++
	for _, arg := range args {
		raw, ok := arg.(string)
		if !ok || !strings.Contains(raw, "://") {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("could not access dolt url %q: %w", parsed.String(), c.cause)
	}
	return nil, c.cause
}

func (c *reserializingEchoConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *reserializingEchoConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
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

func TestBackupRestoreRejectsInvalidCredentialedURLsBeforeDolt(t *testing.T) {
	tests := []string{
		"aws://AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG@bucket/db",
		"https://user:hunter2pass@[bad/db",
		"aws://user:pass%zz@bucket/db",
	}
	for _, source := range tests {
		t.Run(source, func(t *testing.T) {
			conn := &failingConn{err: errors.New("Dolt must not be called")}
			err := BackupRestore(context.Background(), conn, source, "beads", true)
			if err == nil {
				t.Fatal("BackupRestore returned nil for an invalid URL")
			}
			if conn.calls != 0 {
				t.Fatalf("BackupRestore called Dolt %d time(s) for invalid URL %q", conn.calls, source)
			}
			for _, secret := range []string{"AKIAEXAMPLE", "wJalrXUtnFEMI", "K7MDENG", "hunter2pass", "pass%zz"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("BackupRestore(%q) error %q echoes %q", source, err, secret)
				}
			}
			var parseErr *url.Error
			if !errors.As(err, &parseErr) {
				t.Errorf("BackupRestore(%q) error no longer preserves *url.Error: %v", source, err)
				return
			}
			if want := RedactBackupURL(source); parseErr.URL != want {
				t.Errorf("errors.As(*url.Error).URL = %q, want %q", parseErr.URL, want)
			}
			for _, secret := range []string{"AKIAEXAMPLE", "wJalrXUtnFEMI", "K7MDENG", "hunter2pass", "pass%zz"} {
				if strings.Contains(parseErr.Error(), secret) {
					t.Errorf("errors.As(*url.Error) for %q renders %q", source, parseErr)
				}
			}
		})
	}
}

func TestBackupRestoreAllowsBracketedAWSToReachDolt(t *testing.T) {
	const source = "aws://[dynamo_table:bucket]/db"
	sentinel := errors.New("Dolt reached")
	conn := &failingConn{err: sentinel}

	err := BackupRestore(context.Background(), conn, source, "beads", false)
	if err == nil {
		t.Fatal("BackupRestore returned nil for a failing statement")
	}
	if conn.calls != 1 {
		t.Fatalf("BackupRestore called Dolt %d time(s), want 1", conn.calls)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("BackupRestore error %q no longer preserves errors.Is", err)
	}
}

func TestBackupRestoreSanitizesWrappedURLError(t *testing.T) {
	const (
		source     = "https://user:p@ss@doltremoteapi.dolthub.com/org/db"
		serialized = "https://user:p%40ss@doltremoteapi.dolthub.com/org/db"
	)
	conn := &failingConn{err: &url.Error{Op: "Get", URL: serialized, Err: errors.New("dial failed")}}

	err := BackupRestore(context.Background(), conn, source, "beads", false)
	if err == nil {
		t.Fatal("BackupRestore returned nil for a failing statement")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("BackupRestore error no longer preserves *url.Error: %v", err)
	}
	if want := "https://doltremoteapi.dolthub.com/org/db"; urlErr.URL != want {
		t.Errorf("errors.As(*url.Error).URL = %q, want %q", urlErr.URL, want)
	}
	for _, secret := range []string{"user:", "p@ss", "p%40ss"} {
		if strings.Contains(urlErr.Error(), secret) {
			t.Errorf("errors.As(*url.Error) renders %q", urlErr)
		}
	}
}

func TestBackupRestoreScrubsEchoedURLAndPreservesCause(t *testing.T) {
	const source = "aws://AKIAEXAMPLE:hunter2pass@bucket/db"
	sentinel := errors.New("typed Dolt failure")
	for _, force := range []bool{false, true} {
		conn := &echoingConn{cause: sentinel}
		err := BackupRestore(context.Background(), conn, source, "beads", force)
		if err == nil {
			t.Fatal("BackupRestore returned nil for a failing statement")
		}
		if conn.calls != 1 {
			t.Fatalf("BackupRestore called Dolt %d times, want 1", conn.calls)
		}
		if !strings.Contains(err.Error(), "restore from backup aws://bucket/db") {
			t.Errorf("BackupRestore error %q dropped the redacted URL", err)
		}
		for _, secret := range []string{"AKIAEXAMPLE", "hunter2pass"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("BackupRestore error %q echoes %q", err, secret)
			}
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("BackupRestore error %q no longer preserves errors.Is", err)
		}
	}
}

func TestBackupRestoreScrubsReserializedURL(t *testing.T) {
	tests := []struct {
		name                string
		source              string
		decodedPasswordEcho string
		secrets             []string
	}{
		{
			name:    "at sign is escaped",
			source:  "https://user:p@ss@doltremoteapi.dolthub.com/org/db",
			secrets: []string{"user:", "p@ss", "p%40ss"},
		},
		{
			name:    "percent escape is decoded",
			source:  "https://user:pa%73s@doltremoteapi.dolthub.com/org/db",
			secrets: []string{"user:", "pa%73s", "pass"},
		},
		{
			name:                "decoded password differs from canonical escape",
			source:              "https://user:p%40ss@doltremoteapi.dolthub.com/org/db",
			decodedPasswordEcho: "p@ss",
			secrets:             []string{"user:", "p@ss", "p%40ss"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentinel := errors.New("typed Dolt failure")
			cause := error(sentinel)
			if tt.decodedPasswordEcho != "" {
				cause = fmt.Errorf("decoded password=%s: %w", tt.decodedPasswordEcho, sentinel)
			}
			conn := &reserializingEchoConn{cause: cause}
			err := BackupRestore(context.Background(), conn, tt.source, "beads", false)
			if err == nil {
				t.Fatal("BackupRestore returned nil for a failing statement")
			}
			if conn.calls != 1 {
				t.Fatalf("BackupRestore called Dolt %d time(s), want 1", conn.calls)
			}
			if !strings.Contains(err.Error(), "https://doltremoteapi.dolthub.com/org/db") {
				t.Errorf("BackupRestore error %q dropped the safe URL", err)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("BackupRestore error %q echoes %q", err, secret)
				}
			}
			if !errors.Is(err, sentinel) {
				t.Errorf("BackupRestore error %q no longer preserves errors.Is", err)
			}
		})
	}
}

func TestBackupErrorRedactorScrubsCredentialSpellings(t *testing.T) {
	const source = "https://user:pa%73s@doltremoteapi.dolthub.com/org/db"
	sentinel := errors.New("typed Dolt failure")
	cause := fmt.Errorf("raw userinfo=user:pa%%73s raw password=pa%%73s decoded userinfo=user:pass decoded password=pass: %w", sentinel)
	err := redactBackupError(cause, source)

	for _, secret := range []string{"user:pa%73s", "pa%73s", "user:pass", "pass"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("redactBackupError error %q echoes %q", err, secret)
		}
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("redactBackupError error %q no longer preserves errors.Is", err)
	}
}

func TestBackupSyncScrubsURLFromDoltError(t *testing.T) {
	sentinel := errors.New("typed Dolt failure")
	conn := &failingConn{err: fmt.Errorf("could not access dolt url %q: %w",
		"https://user:p%40ss@doltremoteapi.dolthub.com/org/db", sentinel)}

	err := BackupSync(context.Background(), conn, "default")
	if err == nil {
		t.Fatal("BackupSync returned nil for a failing statement")
	}
	if conn.calls != 1 {
		t.Fatalf("BackupSync called Dolt %d time(s), want 1", conn.calls)
	}
	if !strings.Contains(err.Error(), "https://doltremoteapi.dolthub.com/org/db") {
		t.Errorf("BackupSync error %q dropped the safe URL", err)
	}
	for _, secret := range []string{"user:", "p@ss", "p%40ss"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("BackupSync error %q echoes %q", err, secret)
		}
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("BackupSync error %q no longer preserves errors.Is", err)
	}
}

func TestBackupAddScrubsEchoedURLAndPreservesCause(t *testing.T) {
	const source = "aws://AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG@bucket/db"
	sentinel := errors.New("typed Dolt failure")
	conn := &failingConn{err: fmt.Errorf("parse %q: invalid port %q after host: %w",
		source, ":wJalrXUtnFEMI", sentinel)}
	err := BackupAdd(context.Background(), conn, "default", source)
	if err == nil {
		t.Fatal("BackupAdd returned nil for a failing statement")
	}
	for _, secret := range []string{"AKIAEXAMPLE", "wJalrXUtnFEMI", "K7MDENG"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("BackupAdd error %q echoes %q", err, secret)
		}
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("BackupAdd error %q no longer preserves errors.Is", err)
	}
}
