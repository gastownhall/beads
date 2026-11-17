package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	neturl "net/url"
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

// errDoltRestore is the engine error every doltRestoreError wraps.
var errDoltRestore = errors.New("dolt exploded")

// doltRestoreError stands in for the engine's error when a restore from url
// fails, quoting url the way Dolt's own errors do: url.Parse's error for a URL
// it cannot parse, which also repeats part of it outside the quotes (`invalid
// port ":wJal"`), and the remotesapi factory's "could not access dolt url"
// for one it can.
func doltRestoreError(url string) error {
	if _, err := neturl.Parse(url); err != nil {
		return fmt.Errorf("%w: %w", errDoltRestore, err)
	}
	return fmt.Errorf("%w: could not access dolt url '%s'", errDoltRestore, url)
}

// TestBackupRestoreSanitizesURLOnError pins that a failed restore never prints
// the credentials or signed query of the backup URL (#6341), on both the force
// and non-force branches: the source is named without them, and the engine's
// error, which quotes the URL, is left out of the text whenever the URL
// carries them. A credential-free URL keeps the engine's error, and the driver
// error stays unwrappable either way.
func TestBackupRestoreSanitizesURLOnError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		forbidden []string
		want      string // the source as the error names it
		withheld  bool   // the engine's error text is left out
	}{
		{name: "AC-1 userinfo", url: "aws://key:secret@bucket/path",
			forbidden: []string{"secret", "key:", "@"}, want: "aws://bucket/path", withheld: true},
		{name: "AC-1 at sign in password", url: "aws://KEYAT:p@ssAT@bucket/db",
			forbidden: []string{"KEYAT", "ssAT", "@"}, want: "aws://bucket/db", withheld: true},
		{name: "AC-2 signed query", url: "aws://bucket/path?X-Amz-Signature=deadbeefcafe&X-Amz-Credential=AKIAEXAMPLE",
			forbidden: []string{"deadbeefcafe", "AKIAEXAMPLE", "?"}, want: "aws://bucket/path", withheld: true},
		{name: "AC-2 s3 signed query", url: "s3://bucket/db?X-Amz-Signature=s3sigvalue#frag",
			forbidden: []string{"s3sigvalue", "frag"}, want: "s3://bucket/db", withheld: true},
		{name: "AC-4 slash in secret", url: "aws://AKIAUNP:wJal/K7MDUNP@bucket/db",
			forbidden: []string{"AKIAUNP", "wJal", "K7MDUNP"}, want: "aws:[redacted]", withheld: true},
		{name: "AC-4 hash in password", url: "aws://KEYHASH:SEKHASH#tailH@b/p",
			forbidden: []string{"KEYHASH", "SEKHASH", "tailH"}, want: "aws:[redacted]", withheld: true},
		{name: "AC-4 question mark in password", url: "aws://KEYQ:SEKQ?tailQ@b/p",
			forbidden: []string{"KEYQ", "SEKQ", "tailQ"}, want: "aws:[redacted]", withheld: true},
		{name: "userinfo the engine quotes whole", url: "https://user:hunter2@doltremoteapi.dolthub.com/org/db",
			forbidden: []string{"hunter2", "user:"}, want: "https://doltremoteapi.dolthub.com/org/db", withheld: true},
		{name: "at sign in password before a hash", url: "aws://AKIA:p@s3cret#tail@bucket/db",
			forbidden: []string{"AKIA", "s3cret", "tail"}, want: "aws:[redacted]", withheld: true},
		{name: "at sign in password before a question mark", url: "aws://AKIA:p@s3cret?tail@bucket/db",
			forbidden: []string{"AKIA", "s3cret", "tail"}, want: "aws:[redacted]", withheld: true},
		{name: "at sign in password before a slash", url: "aws://AKIA:abc@wJal/K7MD@bucket/db",
			forbidden: []string{"AKIA", "wJal", "K7MD"}, want: "aws:[redacted]", withheld: true},
		{name: "slash secret with an empty head", url: "aws://AKIAIOSFODNN7EXAMPLE:/wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY@bucket/db",
			forbidden: []string{"AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"}, want: "aws:[redacted]", withheld: true},
		{name: "slash secret with a numeric head", url: "aws://AKIAIOSFODNN7EXAMPLE:42/wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKE@bucket/db",
			forbidden: []string{"AKIAIOSFODNN7EXAMPLE", ":42", "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKE"}, want: "aws:[redacted]", withheld: true},
		{name: "question mark secret with a numeric head", url: "aws://KEYPORT:9137?SEKPORT@b/p",
			forbidden: []string{"KEYPORT", "9137", "SEKPORT"}, want: "aws:[redacted]", withheld: true},
		{name: "opaque userinfo", url: "aws:KEYOP:SECRETOP@bucket/db",
			forbidden: []string{"KEYOP", "SECRETOP"}, want: "[redacted]", withheld: true},
		{name: "single-slash userinfo", url: "aws:/KEYSL:SECRETSL@bucket/db",
			forbidden: []string{"KEYSL", "SECRETSL"}, want: "[redacted]", withheld: true},
		{name: "userinfo before the scheme separator", url: "aws:KEYSEP:SECRETSEP://bucket/db",
			forbidden: []string{"KEYSEP", "SECRETSEP"}, want: "[redacted]", withheld: true},
		{name: "file URL with userinfo", url: "file://user:secret@host/path",
			forbidden: []string{"secret", "user:"}, want: "file://host/path", withheld: true},
		{name: "aws bucket and table with a signed query", url: "aws://[bucket:table]/db?X-Amz-Signature=sigAWS",
			forbidden: []string{"sigAWS", "?"}, want: "aws://[bucket:table]/db", withheld: true},
		{name: "userinfo before the aws bucket and table", url: "aws://KEYBT:SECRETBT@[bucket:table]/db",
			forbidden: []string{"KEYBT", "SECRETBT"}, want: "aws:[redacted]", withheld: true},
		{name: "userinfo inside the aws bucket and table", url: "aws://[KEYIN:SECRETIN@bucket:table]/db",
			forbidden: []string{"KEYIN", "SECRETIN"}, want: "aws:[redacted]", withheld: true},
		{name: "credential-free aws URL", url: "aws://[bucket:table]/db",
			want: "aws://[bucket:table]/db"},
		{name: "credential-free file URL", url: "file:///var/backups/x",
			want: "file:///var/backups/x"},
		{name: "windows file URL", url: `file://C:\Users\me\backup`,
			want: `file://C:\Users\me\backup`},
		{name: "percent in file path", url: "file:///var/backups/100%done",
			want: "file:///var/backups/100%done"},
		{name: "hash in file path", url: "file:///var/backups/my#dir",
			want: "file:///var/backups/my#dir"},
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", tc.name, force), func(t *testing.T) {
				conn := &restoreConn{execErr: doltRestoreError(tc.url)}
				err := BackupRestore(context.Background(), conn, tc.url, "beads", force)
				if err == nil {
					t.Fatal("BackupRestore returned nil, want an error")
				}
				msg := err.Error()
				prefix := "restore from backup " + tc.want + ": "
				switch {
				case !strings.HasPrefix(msg, prefix):
					t.Errorf("error = %q, want it to name the source as %q", msg, tc.want)
				case tc.withheld && strings.Contains(msg, errDoltRestore.Error()):
					t.Errorf("error = %q, want the engine's error text left out", msg)
				case !tc.withheld && msg != prefix+conn.execErr.Error():
					t.Errorf("error = %q, want %q", msg, prefix+conn.execErr.Error())
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

// TestRestoreSourceURL pins the restore precondition (bd-jwaljq, GH#7098):
// only a directory whose top level holds Dolt's manifest as a regular file
// counts as a backup. An existing directory without one opens as an empty
// store inside DOLT_BACKUP, so a forced restore from it would drop the live
// database for nothing; exports, backup state and failed-restore debris are
// not backups. A source that passes comes back as the file:// URL a restore
// hands Dolt.
func TestRestoreSourceURL(t *testing.T) {
	root := t.TempDir()
	mkdir := func(name string, files ...string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			path := filepath.Join(dir, f)
			if strings.HasSuffix(f, "/") {
				if err := os.MkdirAll(path, 0o750); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	brokenLink := mkdir("broken-manifest-link")
	if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(brokenLink, "manifest")); err != nil {
		t.Fatal(err)
	}
	goodLink := mkdir("manifest-link")
	if err := os.Symlink(filepath.Join(mkdir("real", "manifest"), "manifest"), filepath.Join(goodLink, "manifest")); err != nil {
		t.Fatal(err)
	}

	const noBackup = "holds no Dolt backup"
	for _, tc := range []struct {
		name, dir, want string
	}{
		{name: "missing", dir: filepath.Join(root, "absent"), want: "backup source does not exist"},
		{name: "regular_file", dir: file, want: "backup source is not a directory"},
		{name: "empty_dir", dir: mkdir("empty"), want: noBackup},
		{name: "jsonl_and_state_only", dir: mkdir("exports", "issues.jsonl", "backup_state.json"), want: noBackup},
		{name: "lock_and_oldgen_debris", dir: mkdir("debris", "LOCK", "oldgen/"), want: noBackup},
		{name: "manifest_in_subdir_only", dir: mkdir("nested", "inner/", "inner/manifest"), want: noBackup},
		{name: "manifest_is_a_directory", dir: mkdir("manifest-dir", "manifest/"), want: noBackup},
		{name: "broken_manifest_symlink", dir: brokenLink, want: noBackup},
		{name: "manifest_file", dir: mkdir("backup", "manifest", "LOCK")},
		{name: "manifest_symlink_to_file", dir: goodLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, err := RestoreSourceURL(tc.dir)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("RestoreSourceURL(%q) = %v, want nil", tc.dir, err)
				}
				if want := "file://" + tc.dir; url != want {
					t.Errorf("RestoreSourceURL(%q) = %q, want %q", tc.dir, url, want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RestoreSourceURL(%q) error = %v, want one containing %q", tc.dir, err, tc.want)
			}
			if tc.want == noBackup && !strings.Contains(err.Error(), tc.dir) {
				t.Errorf("RestoreSourceURL(%q) error = %v, want it to name the source", tc.dir, err)
			}
		})
	}

	// A backup at a path Dolt does not read literally is refused even though
	// it holds a manifest: Dolt parses the source as a file:// URL, so
	// "proj#2" would restore from "proj", an empty directory Dolt creates,
	// after dropping the live database. Windows paths skip the URL parse,
	// but '%' is unescaped there too.
	type misreadCase struct{ name, dir string }
	misread := []misreadCase{
		{"escape", "proj%232"},
		{"bad_escape", "proj%zz"},
	}
	if runtime.GOOS != "windows" {
		misread = append(misread,
			misreadCase{"fragment", "proj#2"},
			misreadCase{"query", "proj?x"},
			misreadCase{"backslash", `proj\2`},
		)
	}
	for _, tc := range misread {
		t.Run("backup_at_a_misread_path_"+tc.name, func(t *testing.T) {
			dir := mkdir(tc.dir, "manifest")
			url, err := RestoreSourceURL(dir)
			if err == nil {
				t.Fatalf("RestoreSourceURL(%q) = %q, want a refusal", dir, url)
			}
			for _, want := range []string{dir, "cannot be restored where it is", "move or rename the backup"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("RestoreSourceURL(%q) error = %v, want it to contain %q", dir, err, want)
				}
			}
		})
	}

	// The check follows the path Dolt opens, not the kernel's reading of the
	// argument: after a symlink, "link/../bk" names link's target's sibling
	// to the kernel, but filepath.Abs, and so the URL Dolt gets, cleans it to
	// link's own sibling. Here only the kernel's directory holds a backup.
	t.Run("dotdot_after_symlink_checks_the_directory_dolt_opens", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows resolves .. lexically, as filepath.Abs does")
		}
		mkdir(filepath.Join("elsewhere", "bk"), "manifest")
		mkdir("bk")
		link := filepath.Join(root, "link")
		if err := os.Symlink(mkdir(filepath.Join("elsewhere", "inner")), link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		// Built by hand: filepath.Join would clean the ".." away.
		sep := string(filepath.Separator)
		dir := link + sep + ".." + sep + "bk"
		if _, err := os.Stat(dir + sep + "manifest"); err != nil {
			t.Fatalf("fixture: the kernel does not reach a manifest through %s: %v", dir, err)
		}
		url, err := RestoreSourceURL(dir)
		if err == nil || !strings.Contains(err.Error(), noBackup) || !strings.Contains(err.Error(), filepath.Join(root, "bk")) {
			t.Fatalf("RestoreSourceURL(%q) = %q, %v; want a refusal naming %s, the directory Dolt would open", dir, url, err, filepath.Join(root, "bk"))
		}
	})

	// A source bd cannot inspect is refused, not waved through. Only asserted
	// where the platform and user really make the stat fail (root reads past
	// a 0000 directory).
	t.Run("unreadable_parent", func(t *testing.T) {
		parent := mkdir("locked", "inner/", "inner/manifest")
		if err := os.Chmod(parent, 0); err != nil {
			t.Skipf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o750) })
		dir := filepath.Join(parent, "inner")
		if _, statErr := os.Stat(filepath.Join(dir, "manifest")); statErr == nil || os.IsNotExist(statErr) {
			t.Skipf("stat through a 0000 directory did not fail with a permission error here: %v", statErr)
		}
		if url, err := RestoreSourceURL(dir); err == nil {
			t.Fatalf("RestoreSourceURL(%q) = %q for a source it cannot stat", dir, url)
		}
	})
}
