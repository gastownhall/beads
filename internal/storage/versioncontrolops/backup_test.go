package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

// dirEntryNames lists dir's entries by name, for the no-side-effect checks
// below: Dolt writes a LOCK file and an oldgen/ directory into a source it
// opens, so a refused source must come back with the entries it had.
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

// writeManifest gives dir a non-empty regular `manifest` file, which is all
// the guard looks at: a real backup sync writes chunks beside it, and Dolt
// judges them when it opens the source.
func writeManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte("5:__DOLT__:lock:root:0:gcgen"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// engineQuestion is the statement engineManifestSize asks the engine with.
const engineQuestion = "SELECT @@secure_file_priv, LENGTH(LOAD_FILE(?))"

// expectEngineAnswer expects the engine to be asked about dir's manifest and
// answers with secureFilePriv and size (nil for NULL).
func expectEngineAnswer(mock sqlmock.Sqlmock, dir string, secureFilePriv, size any) {
	mock.ExpectQuery(regexp.QuoteMeta(engineQuestion)).
		WithArgs(filepath.Join(dir, "manifest")).
		WillReturnRows(sqlmock.NewRows([]string{"@@secure_file_priv", "size"}).AddRow(secureFilePriv, size))
}

// hostQuestion is the statement engineRunsHere asks the engine with, only
// when the engine cannot answer for the source.
const hostQuestion = "SELECT @@hostname"

// expectEngineHost expects the engine to be asked where it runs and answers
// with host.
func expectEngineHost(mock sqlmock.Sqlmock, host string) {
	mock.ExpectQuery(regexp.QuoteMeta(hostQuestion)).
		WillReturnRows(sqlmock.NewRows([]string{"@@hostname"}).AddRow(host))
}

// thisHost is this host's name, the @@hostname an engine running here reports.
func thisHost(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Fatalf("os.Hostname() = %q, %v: an engine on this host reports its name", host, err)
	}
	return host
}

// expectRestoreCall expects the DOLT_BACKUP restore of url into "beads".
func expectRestoreCall(mock sqlmock.Sqlmock, url string, force bool) {
	query := "CALL DOLT_BACKUP('restore', ?, ?)"
	if force {
		query = "CALL DOLT_BACKUP('restore', '--force', ?, ?)"
	}
	mock.ExpectExec(regexp.QuoteMeta(query)).
		WithArgs(url, "beads").
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestBackupRestoreRefusesSourceWithoutManifest pins #5972 at the choke point
// every restore path (direct store, embedded store, proxied route, bootstrap
// restore) shares: DOLT_BACKUP('restore', '--force', …) drops the target
// database before reading the source, so an ordinary directory — empty,
// holding exports, bare except a `manifest` subdirectory, or holding an empty
// manifest — must be refused before the CALL. The engine here cannot answer
// for the source (secure_file_priv confines LOAD_FILE, as on the local test
// server) and runs on this host, so this process's filesystem decides;
// sqlmock fails any statement it was not told to expect, so a regression that
// reached the CALL returns that failure instead of ErrNotDoltBackup.
func TestBackupRestoreRefusesSourceWithoutManifest(t *testing.T) {
	here := thisHost(t)
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
	emptyManifestDir := filepath.Join(base, "empty-manifest")
	if err := os.MkdirAll(emptyManifestDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyManifestDir, "manifest"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		dir  string
	}{
		{name: "empty_dir", dir: emptyDir},
		{name: "jsonl_only", dir: jsonlDir},
		{name: "manifest_is_a_directory", dir: manifestDir},
		{name: "empty_manifest", dir: emptyManifestDir},
	} {
		for _, force := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/force=%v", tc.name, force), func(t *testing.T) {
				db, mock := newMockDB(t)
				expectEngineAnswer(mock, tc.dir, "/var/lib/dolt-files", nil)
				expectEngineHost(mock, here)
				before := dirEntryNames(t, tc.dir)
				err := BackupRestore(context.Background(), db, "file://"+tc.dir, "beads", force)
				if !errors.Is(err, ErrNotDoltBackup) {
					t.Fatalf("BackupRestore(%q, force=%v) error = %v, want ErrNotDoltBackup", tc.dir, force, err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
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

// TestBackupRestoreRefusesPathDoltReadsElsewhere pins the path half of the
// guard: DOLT_BACKUP reads its source path out of an unescaped file:// URL, so
// a '#', '?', '%' or '\' in it names a different directory to Dolt, which
// Dolt creates and opens as an empty store while the forced restore drops the
// target. Each source here holds a manifest, so only the path can refuse it,
// and it must be refused before any SQL at all: the nil connection panics on
// any statement, the question to the engine included.
func TestBackupRestoreRefusesPathDoltReadsElsewhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`Windows reads '#' as written and has no '?' or '\' in a name; TestCheckBackupPath covers its '%'`)
	}
	base := t.TempDir()
	for _, leaf := range []string{"bk#1", "bk?1", "b%41k", `b\k`} {
		dir := filepath.Join(base, leaf)
		writeManifest(t, dir)
		for _, force := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/force=%v", leaf, force), func(t *testing.T) {
				err := BackupRestore(context.Background(), nil, "file://"+dir, "beads", force)
				if !errors.Is(err, ErrBackupPathRewritten) || errors.Is(err, ErrNotDoltBackup) {
					t.Fatalf("BackupRestore(%q, force=%v) error = %v, want ErrBackupPathRewritten", dir, force, err)
				}
				if got := dirEntryNames(t, dir); strings.Join(got, ",") != "manifest" {
					t.Errorf("refused restore changed the backup source %s: entries %v, want [manifest]", dir, got)
				}
			})
		}
	}
}

// TestCheckBackupPath pins which paths Dolt reads as written, on each platform
// DirToFileURL builds a file:// URL for, and that a refusal of a path cut at
// '#' or '?' names the directory Dolt would read instead: a workspace under
// such a path syncs its backups there, so that is where they are.
func TestCheckBackupPath(t *testing.T) {
	for _, tc := range []struct {
		goos, dir string
		refused   bool
		doltReads string
	}{
		{goos: "linux", dir: "/srv/backups/beads"},
		{goos: "linux", dir: "/srv/back ups/bëads (1)+[2]"},
		{goos: "linux", dir: "/srv/bk#1", refused: true, doltReads: "/srv/bk"},
		{goos: "linux", dir: "/home/u/src/C#/proj/.beads/backup", refused: true, doltReads: "/home/u/src/C"},
		{goos: "linux", dir: "/srv/bk/?x", refused: true, doltReads: "/srv/bk"},
		{goos: "linux", dir: "/srv/b%41k", refused: true},
		{goos: "darwin", dir: `/srv/b\k`, refused: true},
		{goos: "windows", dir: `C:\srv\bk#1`},
		{goos: "windows", dir: `C:\srv\back ups\beads`},
		{goos: "windows", dir: `C:\srv\b%41k`, refused: true},
	} {
		t.Run(tc.goos+":"+tc.dir, func(t *testing.T) {
			err := checkBackupPath(tc.goos, tc.dir)
			if !tc.refused {
				if err != nil {
					t.Fatalf("checkBackupPath(%q, %q) = %v, want nil: Dolt reads this path as written", tc.goos, tc.dir, err)
				}
				return
			}
			if !errors.Is(err, ErrBackupPathRewritten) {
				t.Fatalf("checkBackupPath(%q, %q) = %v, want ErrBackupPathRewritten", tc.goos, tc.dir, err)
			}
			if !strings.Contains(err.Error(), "the database was not changed") {
				t.Errorf("refusal %q does not say the database was not changed", err)
			}
			if tc.doltReads != "" && !strings.Contains(err.Error(), "the restore would read "+tc.doltReads+";") {
				t.Errorf("refusal %q does not name %s, the directory Dolt would read", err, tc.doltReads)
			}
		})
	}
}

// TestBackupRestoreChecksTheSourceWhereTheEngineReadsIt pins whose filesystem
// the manifest check reads. DOLT_BACKUP reads the source on the SQL engine's
// filesystem, which for a direct server-mode connection can be another host or
// a container, so the engine is asked first and its answer wins over this
// process's view either way: a backup only the server holds restores, and a
// manifest only this process sees does not let the server drop the target. A
// length the engine read is an answer however secure_file_priv confines
// LOAD_FILE. An engine that cannot answer leaves the check to this process's
// filesystem only when it runs on this host; elsewhere the source is refused
// as one nothing can check, whatever this process's filesystem shows.
func TestBackupRestoreChecksTheSourceWhereTheEngineReadsIt(t *testing.T) {
	withManifest := t.TempDir()
	writeManifest(t, withManifest)
	empty := t.TempDir()
	here := thisHost(t)
	const elsewhere = "dolt-server.internal"
	const confined = "/var/lib/dolt-files"
	isADirectory := errors.New("read manifest: is a directory")

	for _, tc := range []struct {
		name           string
		dir            string
		secureFilePriv any // nil is NULL
		size           any // nil is NULL
		engineErr      error
		host           string // the engine's @@hostname, asked only when it cannot answer
		hostErr        error  // asking for it fails
		want           error  // nil restores
		wantText       string // the refusal says why the engine cannot answer
	}{
		// The engine answers: this process's filesystem and host do not matter.
		{name: "engine_holds_a_backup_bd_cannot_see", dir: empty, secureFilePriv: "", size: 812},
		{name: "engine_has_no_manifest_bd_sees_one", dir: withManifest, secureFilePriv: "", size: nil, want: ErrNotDoltBackup},
		{name: "engine_manifest_is_empty", dir: withManifest, secureFilePriv: "", size: 0, want: ErrNotDoltBackup},
		{name: "confined_engine_holds_a_backup_bd_cannot_see", dir: empty, secureFilePriv: confined, size: 812},
		{name: "confined_engine_manifest_is_empty", dir: withManifest, secureFilePriv: confined, size: 0, want: ErrNotDoltBackup},

		// The engine cannot answer and runs here: this process's filesystem
		// is the engine's.
		{name: "confined_load_file_here_leaves_it_to_bd", dir: withManifest, secureFilePriv: confined, size: nil, host: here},
		{name: "disabled_load_file_here_leaves_it_to_bd", dir: withManifest, secureFilePriv: nil, size: nil, host: here},
		{name: "failed_question_here_leaves_it_to_bd", dir: withManifest, engineErr: isADirectory, host: here},
		{name: "failed_question_here_bd_has_no_manifest", dir: empty, engineErr: isADirectory, host: here, want: ErrNotDoltBackup},

		// The engine cannot answer and runs elsewhere: the manifest this
		// process sees says nothing about the directory the engine reads.
		{name: "confined_load_file_elsewhere", dir: withManifest, secureFilePriv: confined, size: nil, host: elsewhere,
			want: ErrBackupSourceUnchecked, wantText: "its secure_file_priv lets LOAD_FILE read only " + confined},
		{name: "disabled_load_file_elsewhere", dir: withManifest, secureFilePriv: nil, size: nil, host: elsewhere,
			want: ErrBackupSourceUnchecked, wantText: "its secure_file_priv is NULL"},
		{name: "failed_question_elsewhere", dir: withManifest, engineErr: isADirectory, host: elsewhere,
			want: ErrBackupSourceUnchecked, wantText: "LOAD_FILE failed: read manifest: is a directory"},
		{name: "engine_host_unknown", dir: withManifest, secureFilePriv: confined, size: nil, hostErr: errors.New("bad connection"),
			want: ErrBackupSourceUnchecked, wantText: "its secure_file_priv lets LOAD_FILE read only " + confined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			if tc.engineErr != nil {
				mock.ExpectQuery(regexp.QuoteMeta(engineQuestion)).
					WithArgs(filepath.Join(tc.dir, "manifest")).
					WillReturnError(tc.engineErr)
			} else {
				expectEngineAnswer(mock, tc.dir, tc.secureFilePriv, tc.size)
			}
			switch {
			case tc.hostErr != nil:
				mock.ExpectQuery(regexp.QuoteMeta(hostQuestion)).WillReturnError(tc.hostErr)
			case tc.host != "":
				expectEngineHost(mock, tc.host)
			}
			url := "file://" + tc.dir
			if tc.want == nil {
				expectRestoreCall(mock, url, true)
			}

			err := BackupRestore(context.Background(), db, url, "beads", true)
			if tc.want == nil && err != nil {
				t.Fatalf("BackupRestore: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("BackupRestore error = %v, want %v before the restore CALL", err, tc.want)
			}
			if tc.wantText != "" {
				assertUncheckedRefusal(t, err, tc.dir, tc.wantText)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// assertUncheckedRefusal checks that err refuses dir as a source nothing can
// check: it names dir, says why the engine cannot answer and that the database
// was not changed, and offers to let the server read the backup rather than to
// back up into dir.
func assertUncheckedRefusal(t *testing.T, err error, dir, why string) {
	t.Helper()
	for _, want := range []string{"cannot restore from " + dir + ":", why, "the database was not changed", "set its secure_file_priv to " + dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	// The CLI offers to back up into a directory refused with
	// ErrNotDoltBackup, and this one may well hold a backup.
	if errors.Is(err, ErrNotDoltBackup) {
		t.Errorf("refusal %q unwraps to ErrNotDoltBackup, but nothing showed the source is not a backup", err)
	}
}

// TestBackupRestoreIssuesRestoreForBackupWithManifest is the positive control
// for the same guard: a directory the engine finds a non-empty `manifest` in
// looks like a Dolt backup, so the CALL still reaches the connection — for
// both the --force and the plain form — and a non-file:// URL is neither
// checked nor newly blocked.
func TestBackupRestoreIssuesRestoreForBackupWithManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	url := "file://" + dir

	for _, tc := range []struct {
		name  string
		force bool
	}{
		{name: "force", force: true},
		{name: "no_force", force: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			expectEngineAnswer(mock, dir, "", 812)
			expectRestoreCall(mock, url, tc.force)

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
		db, mock := newMockDB(t)
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
