package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

// TestRestoreSourceURL pins the local-source prerequisite every restore entry
// point (embedded, sql-server and proxied) shares: a directory that is not a
// Dolt backup store is refused before any restore work, with or without
// --force, because DOLT_BACKUP('restore', '--force') drops the target
// database before it discovers the source is empty (GH#7098). A regular
// manifest file is the layout prerequisite, not a promise about the backup's
// contents: an empty one passes, and Dolt owns interpreting it. A backup
// whose path Dolt would read as another directory is refused too.
func TestRestoreSourceURL(t *testing.T) {
	base := t.TempDir()
	regularFile := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(regularFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	jsonlDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(jsonlDir, "issues.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "backup_state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirManifest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirManifest, "manifest"), 0o750); err != nil {
		t.Fatal(err)
	}
	// symlink returns link, or "" when the platform cannot make symlinks, so
	// only the cases that need one skip.
	symlink := func(target, link string) string {
		if err := os.Symlink(target, link); err != nil {
			t.Logf("symlinks unavailable: %v", err)
			return ""
		}
		return link
	}
	dangling := t.TempDir()
	if symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(dangling, "manifest")) == "" {
		dangling = ""
	}

	valid := t.TempDir()
	if err := os.WriteFile(filepath.Join(valid, "manifest"), []byte("manifest contents are Dolt's to interpret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular zero-length manifest passes this layout check.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "manifest"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink to a regular manifest is a regular manifest, as os.Stat
	// resolves it.
	linkTarget := filepath.Join(t.TempDir(), "real-manifest")
	if err := os.WriteFile(linkTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if symlink(linkTarget, filepath.Join(linked, "manifest")) == "" {
		linked = ""
	}
	// A symlink to a backup directory is that backup directory.
	linkedDir := symlink(valid, filepath.Join(base, "linked-backup"))

	for _, tc := range []struct {
		name         string
		dir          string
		wantURL      bool   // want the source's file:// URL back
		wantText     string // error text it must carry
		wantNoBackup bool   // error must wrap ErrNoBackup
	}{
		{name: "absent path", dir: filepath.Join(base, "absent"), wantText: "backup source does not exist"},
		{name: "regular file", dir: regularFile, wantText: "backup source is not a directory"},
		{name: "empty dir", dir: t.TempDir(), wantText: "no Dolt backup found", wantNoBackup: true},
		{name: "url input", dir: "https://doltremoteapi.dolthub.com/user/repo", wantText: ""},
		{name: "jsonl export only", dir: jsonlDir, wantText: "no Dolt backup found", wantNoBackup: true},
		{name: "backup state only", dir: stateDir, wantText: "no Dolt backup found", wantNoBackup: true},
		{name: "directory manifest", dir: dirManifest, wantText: "no Dolt backup found", wantNoBackup: true},
		{name: "dangling manifest symlink", dir: dangling, wantText: "no Dolt backup found", wantNoBackup: true},
		{name: "regular manifest", dir: valid, wantURL: true},
		{name: "empty regular manifest", dir: empty, wantURL: true},
		{name: "manifest symlink to a regular file", dir: linked, wantURL: true},
		{name: "symlinked backup dir", dir: linkedDir, wantURL: true},
		// The four filesystem scenarios from PR #7192's
		// TestValidateBackupRestoreDir, held by the shared helper instead of
		// a CLI-only guard so the stores' SDK callers keep them too.
		{name: "pr7192 missing directory", dir: filepath.Join(t.TempDir(), "does-not-exist"), wantText: "backup source does not exist"},
		{name: "pr7192 existing but empty directory", dir: t.TempDir(), wantText: "no Dolt backup found", wantNoBackup: true},
		{name: "pr7192 path is a file", dir: regularFile, wantText: "backup source is not a directory"},
		{name: "pr7192 directory with a manifest", dir: valid, wantURL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dir == "" {
				t.Skip("fixture needs symlinks, which this platform cannot make")
			}
			got, err := RestoreSourceURL(tc.dir)
			if tc.wantURL {
				if err != nil {
					t.Fatalf("RestoreSourceURL(%q) = %v, want the source's URL", tc.dir, err)
				}
				if want, werr := DirToFileURL(tc.dir); werr != nil || got != want {
					t.Errorf("RestoreSourceURL(%q) = %q, want %q (DirToFileURL of it)", tc.dir, got, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("RestoreSourceURL(%q) = %q, want a refusal", tc.dir, got)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantText)
			}
			// The ErrNoBackup refusals quote the source with %q, which doubles
			// a Windows path's backslashes; the older errors print it raw.
			source := tc.dir
			if tc.wantNoBackup {
				source = strconv.Quote(tc.dir)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), source) {
				t.Errorf("error = %v, want it to name the source as %s", err, source)
			}
			if gotNoBackup := errors.Is(err, ErrNoBackup); gotNoBackup != tc.wantNoBackup {
				t.Errorf("errors.Is(err, ErrNoBackup) = %v, want %v (err = %v)", gotNoBackup, tc.wantNoBackup, err)
			}
		})
	}

	// A directory whose manifest cannot be stat'd is refused with the
	// underlying error preserved, not reported as a missing backup. Root and
	// non-POSIX builds cannot make this happen (Windows reports euid -1, and
	// its chmod only sets the read-only attribute); elsewhere the mode change
	// is the whole fixture.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		denied := t.TempDir()
		if err := os.WriteFile(filepath.Join(denied, "manifest"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(denied, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(denied, 0o700) })
		_, err := RestoreSourceURL(denied)
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("RestoreSourceURL(denied) = %v, want the underlying permission error preserved", err)
		}
		if errors.Is(err, ErrNoBackup) {
			t.Errorf("a permission failure is not a missing backup: %v", err)
		}
	}

	// A backup whose path Dolt does not take literally in a file:// URL is
	// refused even though it holds a manifest: Dolt would restore from, and
	// create, another directory ('#' and '?' cut the path short, '%41'
	// decodes to 'A', '\' becomes '/'). Dolt takes a space literally, so that
	// path keeps the URL DirToFileURL builds. Each fixture is made under one
	// parent directory, so the special character is in its last element only.
	windows := runtime.GOOS == "windows"
	parent := t.TempDir()
	for _, tc := range []struct {
		name    string
		refused bool
	}{
		{name: "a dir"},
		{name: "100%done", refused: true},
		{name: "x%41y", refused: true},
		{name: "my#dir", refused: !windows}, // a drive-letter path skips net/url
		{name: "a?b", refused: true},
		{name: `back\slash`, refused: !windows}, // Windows' separator
	} {
		t.Run("path "+tc.name, func(t *testing.T) {
			if windows && strings.Contains(tc.name, "?") {
				t.Skip("Windows file names cannot hold '?'")
			}
			special := filepath.Join(parent, tc.name)
			if err := os.MkdirAll(special, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(special, "manifest"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := RestoreSourceURL(special)
			if !tc.refused {
				if err != nil {
					t.Fatalf("RestoreSourceURL(%q): %v", special, err)
				}
				if want, werr := DirToFileURL(special); werr != nil || got != want {
					t.Errorf("RestoreSourceURL(%q) = %q, want %q", special, got, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("RestoreSourceURL(%q) = %q, want a refusal: Dolt would restore from another directory", special, got)
			}
			if errors.Is(err, ErrNoBackup) {
				t.Errorf("the source holds a manifest, so the refusal must not say it holds no backup: %v", err)
			}
			for _, want := range []string{strconv.Quote(special), "The database was not restored", "move or rename the backup"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to contain %s", err, want)
				}
			}
		})
	}

	// Such a path without a backup is refused as one, and when the directory
	// Dolt reads it as holds a backup the refusal names that directory: bd's
	// backup writers hand Dolt the same URL, so the backups bd synced to the
	// path went there. Dolt reads a path only up to a '#' or '?', decodes
	// '%41' to 'A' and reads '\' as '/'; it cannot parse "100%done" at all, so
	// bd's backups to it failed and nothing is named.
	misread := t.TempDir()
	for _, tc := range []struct {
		source string // the restore source, under misread
		readAs string // the directory Dolt reads it as, which holds a backup; "" for none
		posix  bool   // Windows takes the character literally
	}{
		{source: "ws#1/proj/backup", readAs: "ws", posix: true},
		{source: "x%41y/backup", readAs: "xAy/backup"},
		{source: "a?b/backup", readAs: "a", posix: true},
		{source: `back\slash/backup`, readAs: "back/slash/backup", posix: true},
		{source: "q#r/backup", posix: true}, // Dolt reads it as q, which holds no backup
		{source: "100%done/backup"},
	} {
		name, _, _ := strings.Cut(tc.source, "/")
		t.Run("misread "+name, func(t *testing.T) {
			if windows && tc.posix {
				t.Skip("Windows takes this character literally")
			}
			source := filepath.Join(misread, tc.source)
			if err := os.MkdirAll(source, 0o750); err != nil {
				t.Fatal(err)
			}
			readAs := ""
			if tc.readAs != "" {
				readAs = filepath.Join(misread, tc.readAs)
				if err := os.MkdirAll(readAs, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(readAs, "manifest"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := RestoreSourceURL(source)
			if err == nil {
				t.Fatalf("RestoreSourceURL(%q) = %q, want a refusal: the source holds no backup", source, got)
			}
			if !errors.Is(err, ErrNoBackup) {
				t.Errorf("errors.Is(err, ErrNoBackup) = false, want true (err = %v)", err)
			}
			if !strings.Contains(err.Error(), strconv.Quote(source)) {
				t.Errorf("error = %v, want it to name the source as %s", err, strconv.Quote(source))
			}
			if readAs == "" {
				if strings.Contains(err.Error(), "reads this path as") {
					t.Errorf("error = %v, want no directory named: Dolt's reading of the path holds no backup", err)
				}
				return
			}
			for _, want := range []string{"reads this path as " + strconv.Quote(readAs) + ", which holds a Dolt backup", "bd's backups to this path went there"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to contain %s", err, want)
				}
			}
		})
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
