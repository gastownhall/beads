package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
)

// ExportBackupName is the Dolt backup remote the auto-backup path registers
// for its local backup directory. `bd backup init` registers a different
// name, so the two can coexist until they point at the same address.
const ExportBackupName = "backup_export"

// BackupAdd registers a Dolt backup destination.
func BackupAdd(ctx context.Context, db DBConn, name, url string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('add', ?, ?)", name, url); err != nil {
		return fmt.Errorf("add backup %s: %w", name, err)
	}
	return nil
}

// BackupSync pushes the database to the named backup destination.
func BackupSync(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('sync', ?)", name); err != nil {
		return fmt.Errorf("sync backup %s: %w", name, err)
	}
	return nil
}

// BackupRemove removes a configured Dolt backup destination.
func BackupRemove(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('rm', ?)", name); err != nil {
		return fmt.Errorf("remove backup %s: %w", name, err)
	}
	return nil
}

// ErrNotDoltBackup reports a file:// restore source that does not hold a
// Dolt backup. It is what every refusal of a not-a-backup directory
// unwraps to, so callers can word their own remedy without matching text.
var ErrNotDoltBackup = errors.New("not a Dolt backup")

// ErrBackupPathRewritten reports a file:// restore source whose path Dolt
// would read as a different directory (see CheckBackupPath).
var ErrBackupPathRewritten = errors.New("restore source path names a different directory to Dolt")

// ErrBackupSourceUnchecked reports a file:// restore source nothing can check
// where Dolt reads it: the engine that runs the restore cannot read the
// source's manifest, and it does not run on this host, so this process's
// filesystem is not the one it reads (see checkRestoreSource).
var ErrBackupSourceUnchecked = errors.New("restore source cannot be checked where Dolt reads it")

// refusal is a refused restore source. Its message is the whole sentence, and
// it unwraps to its sentinel without repeating the sentinel's text, so it
// reads as one sentence wherever it is printed as it is (bootstrap's restore
// error, for one).
type refusal struct {
	sentinel error
	msg      string
}

func (e *refusal) Error() string { return e.msg }
func (e *refusal) Unwrap() error { return e.sentinel }

// notDoltBackup refuses dir as a restore source that holds no Dolt backup.
func notDoltBackup(dir, why string) error {
	return &refusal{ErrNotDoltBackup, fmt.Sprintf("%s is not a Dolt backup (%s)", remedy.PrintedValue(dir), why)}
}

// CheckBackupPath refuses a restore source path Dolt would not read as written.
//
// DirToFileURL writes "file://"+abs with nothing escaped, and Dolt reads the
// path back out of that URL as a URL parser does (earl.Parse, which hands it
// to url.Parse), then unescapes it once more when it opens the store
// (url.PathUnescape): '#' or '?' ends the path, '%' starts an escape, and a
// '\' becomes '/'. A path holding one of these names a different directory to
// Dolt, which creates it when missing, so a forced restore drops the target
// and then fails, however good the backup at the path bd checked (#5972). On
// Windows earl.Parse takes a path that starts with a volume as it is, and '\'
// is the separator there anyway, so only '%' changes it.
//
// A relative dir is checked as the absolute path DirToFileURL makes of it.
func CheckBackupPath(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	return checkBackupPath(runtime.GOOS, abs)
}

// checkBackupPath is CheckBackupPath for an absolute dir on goos.
func checkBackupPath(goos, dir string) error {
	rewritten := `#?%\`
	if goos == "windows" {
		rewritten = "%"
	}
	i := strings.IndexAny(dir, rewritten)
	if i < 0 {
		return nil
	}
	var reads string
	switch c := dir[i]; c {
	case '#', '?':
		// The path before the first such character holds none of them, so
		// Dolt reads exactly that part of it.
		reads = fmt.Sprintf("Dolt reads a file:// path only up to its first '#' or '?', so the restore would read %s",
			remedy.PrintedValue(filepath.Clean(dir[:i])))
	case '%':
		reads = "Dolt decodes '%' escapes in a file:// path, so the restore would read a different directory"
	default:
		reads = `Dolt reads '\' in a file:// path as '/', so the restore would read a different directory`
	}
	avoid := `'#', '?', '%' or '\'`
	if goos == "windows" {
		avoid = "'%'"
	}
	return &refusal{ErrBackupPathRewritten, fmt.Sprintf(
		"cannot restore from %s: %s; the database was not changed\nTo restore this backup, copy it to a directory whose path has no %s and restore from there",
		remedy.PrintedValue(dir), reads, avoid)}
}

// CheckBackupDir verifies dir can be a file:// backup restore source as this
// process's filesystem shows it: Dolt would read dir as written
// (CheckBackupPath), and dir exists, is a directory, and holds a non-empty
// regular Dolt backup `manifest` file.
//
// Presence is the judgement here, not contents. Dolt opens the source before
// it drops the target database and reads it only after: opening parses the
// manifest and opens every table file it names, so a corrupt manifest or a
// missing table file fails before the drop. A directory with no manifest is
// what gets through: Dolt opens it as an empty store, drops the target, and
// only then fails to read it (#5972). So this check must run first: a
// directory that merely exists is not evidence it was written by a backup,
// and an empty one must cost nothing. An empty manifest fails Dolt's open
// too, but only after Dolt has written a LOCK file into the source, so it is
// refused here as well. What still passes is a well-formed manifest whose
// store holds no root, if an interrupted first sync can leave one: Dolt
// opens that and drops the target before it fails to read it. Only restoring
// into a staging database first would close that.
func CheckBackupDir(dir string) error {
	if err := CheckBackupPath(dir); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup source does not exist: %w", err)
		}
		return fmt.Errorf("inspect backup source %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source is not a directory: %s", dir)
	}
	manifest, err := os.Stat(filepath.Join(dir, "manifest"))
	if err != nil {
		if os.IsNotExist(err) {
			return notDoltBackup(dir, "no manifest found")
		}
		return fmt.Errorf("inspect backup source %s: %w", dir, err)
	}
	if !manifest.Mode().IsRegular() {
		return notDoltBackup(dir, "no manifest found")
	}
	if manifest.Size() == 0 {
		return notDoltBackup(dir, "its manifest is empty")
	}
	return nil
}

// checkRestoreSource checks dir where the restore will read it, before any
// statement that can change the target database.
//
// The SQL engine behind db is asked first (engineManifestSize), because the
// directory DOLT_BACKUP reads is on that engine's filesystem: a direct
// server-mode connection can reach a Dolt server on another host or in a
// container, where this process's filesystem says nothing about it — a backup
// the server wrote there would be refused, and a manifest only this process
// sees would let the server drop the target. An engine that cannot answer
// leaves the check to this process's filesystem (CheckBackupDir) only when
// that is the engine's, because the engine runs on this host
// (engineRunsHere): embedded Dolt, the managed-local server proxied mode
// restores through, and a local Dolt server. An engine elsewhere that cannot
// answer is refused, since nothing here shows what it would read.
func checkRestoreSource(ctx context.Context, db DBConn, dir string) error {
	size, unanswered := engineManifestSize(ctx, db, dir)
	switch {
	case unanswered != nil && engineRunsHere(ctx, db):
		return CheckBackupDir(dir)
	case unanswered != nil:
		return &refusal{ErrBackupSourceUnchecked, fmt.Sprintf(
			"cannot restore from %s: the Dolt server cannot read the directory's manifest (%v), and nothing shows the server runs on this host, where bd could check the directory itself; the database was not changed\n"+
				"To restore it, let the server read the backup (set its secure_file_priv to %s or \"\", then restart it), or run bd backup restore where the server runs",
			remedy.PrintedValue(dir), unanswered, remedy.PrintedValue(dir))}
	case !size.Valid:
		return notDoltBackup(dir, "no manifest found")
	case size.Int64 == 0:
		return notDoltBackup(dir, "its manifest is empty")
	}
	return nil
}

// engineManifestSize asks the SQL engine behind db for the size of dir's
// `manifest` file as that engine sees it, read with LOAD_FILE. A length is
// the engine's answer whatever secure_file_priv says: the engine opened the
// file. A NULL is one only when secure_file_priv is "", which lets LOAD_FILE
// read any path, so that NULL means there is no such file. unanswered says
// why the engine cannot answer otherwise: secure_file_priv confines LOAD_FILE
// to one directory (where any file outside it reads NULL) or turns it off, or
// the query failed, as it does for a `manifest` that is a directory.
func engineManifestSize(ctx context.Context, db DBConn, dir string) (size sql.NullInt64, unanswered error) {
	var secureFilePriv sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@secure_file_priv, LENGTH(LOAD_FILE(?))",
		filepath.Join(dir, "manifest")).Scan(&secureFilePriv, &size); err != nil {
		return sql.NullInt64{}, fmt.Errorf("LOAD_FILE failed: %w", remedy.PrintedError(err))
	}
	switch {
	case size.Valid:
		return size, nil
	case !secureFilePriv.Valid:
		return size, errors.New("its secure_file_priv is NULL, which turns LOAD_FILE off")
	case secureFilePriv.String != "":
		return size, fmt.Errorf("its secure_file_priv lets LOAD_FILE read only %s", remedy.PrintedValue(secureFilePriv.String))
	}
	return size, nil
}

// engineRunsHere reports whether the SQL engine behind db runs on this host,
// so that this process's filesystem is the one it reads: the engine's
// @@hostname, which Dolt's engine takes from os.Hostname where it runs, is
// this host's name. A Dolt server on another host or in a container reports
// that host's or container's own name. A failed query or a host with no name
// shows nothing, so it is taken as elsewhere. The name is a strong sign, not
// proof: a container that takes the host's name (--network=host, --uts=host)
// but not its filesystem passes as here.
func engineRunsHere(ctx context.Context, db DBConn) bool {
	var engineHost sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@hostname").Scan(&engineHost); err != nil {
		return false
	}
	here, err := os.Hostname()
	return err == nil && here != "" && engineHost.String == here
}

// BackupRestore restores a database from a backup at the given URL into
// the named database. When force is true, an existing database with the
// same name is overwritten. Mirrors the CLI: dolt backup restore [--force] <url> <db_name>
//
// A file:// URL must name a directory holding a Dolt backup, as the engine
// that runs the restore sees it (checkRestoreSource); anything else is refused
// before any statement that can change the target, so a bad source cannot drop
// or recreate the target database. Restore with --force drops the target
// before reading the source, which is why the source is validated first.
// Where the engine cannot read the source's manifest, that guarantee rests on
// its host name: only an engine that reports this host's is checked on this
// process's filesystem, and a container that takes the host's name but not
// its filesystem would be checked on the wrong one (engineRunsHere).
// DOLT_BACKUP also writes into the source while opening it (a LOCK file and an
// oldgen/ directory), so a refused source is left untouched.
func BackupRestore(ctx context.Context, db DBConn, url, dbName string, force bool) error {
	// DirToFileURL (the only producer of file:// URLs here) returns
	// "file://"+abs, so cutting the scheme gives back the directory bd named.
	// That is not always the directory Dolt reads, which parses the URL
	// instead; CheckBackupPath refuses a path where the two differ.
	// Non-file:// URLs cannot name a directory to check and pass through
	// untouched.
	if dir, ok := strings.CutPrefix(url, "file://"); ok {
		if err := CheckBackupPath(dir); err != nil {
			return err
		}
		if err := checkRestoreSource(ctx, db, dir); err != nil {
			return err
		}
	}
	if force {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', '--force', ?, ?)", url, dbName); err != nil {
			return fmt.Errorf("restore from backup %s: %w", url, err)
		}
	} else {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', ?, ?)", url, dbName); err != nil {
			return fmt.Errorf("restore from backup %s: %w", url, err)
		}
	}
	return nil
}

// CurrentCommit returns the hash of the current HEAD commit.
func CurrentCommit(ctx context.Context, db DBConn) (string, error) {
	var hash string
	if err := db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&hash); err != nil {
		return "", fmt.Errorf("failed to get current commit: %w", err)
	}
	return hash, nil
}

// HeadHashes returns the hash of the HEAD commit and the hash of its root
// value, DOLT_HASHOF('HEAD') and DOLT_HASHOF_DB('HEAD'), read in one
// statement so both describe the same HEAD.
func HeadHashes(ctx context.Context, db DBConn) (commit, root string, err error) {
	if err = db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD'), DOLT_HASHOF_DB('HEAD')").Scan(&commit, &root); err != nil {
		return "", "", fmt.Errorf("failed to read HEAD hashes: %w", err)
	}
	return commit, root, nil
}

// BackupToDir registers dir as the ExportBackupName file:// backup remote and
// syncs the full database to it, preserving complete commit history. dir must
// already exist on this machine.
//
// register carries the remote add/remove; sync carries DOLT_BACKUP('sync'),
// which streams the database and can outlive a pooled connection's read
// deadline, so a caller that has one hands in a long-timeout connection there.
// A caller with a single connection passes it for both.
//
// Registration is idempotent (remove, then add). When another remote — e.g.
// the one `bd backup init` registered — already holds the same address, Dolt
// refuses the add; the sync then goes through that remote's name instead.
func BackupToDir(ctx context.Context, register, sync DBConn, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup destination does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup destination is not a directory: %s", dir)
	}
	backupURL, err := DirToFileURL(dir)
	if err != nil {
		return err
	}

	name := ExportBackupName
	_ = BackupRemove(ctx, register, name)
	if err := BackupAdd(ctx, register, name, backupURL); err != nil {
		conflict := ExtractAddressConflictName(err)
		if conflict == "" {
			return fmt.Errorf("register backup remote: %w", err)
		}
		name = conflict
	}
	if err := BackupSync(ctx, sync, name); err != nil {
		return fmt.Errorf("sync to backup: %w", err)
	}
	return nil
}

// DirToFileURL resolves dir to an absolute path and returns a file:// URL.
//
// An input that already carries a scheme is rejected rather than prefixed.
// filepath.Abs would happily turn "https://doltremoteapi.dolthub.com/u/r" into
// "file:///cwd/https:/doltremoteapi.dolthub.com/u/r" — a syntactically valid
// URL naming a local directory that does not exist, which DOLT_BACKUP would
// then fail on, or worse create. No caller can reach that today (`bd backup
// restore` os.Stats its argument first, so a URL never gets here), but every
// caller is a restore path and restore-from-a-remote is an open capability
// question — see backupRemoteSchemeTracking in cmd/bd/capability_registry.go.
// Whoever closes it should get an error here, not a mangled path.
func DirToFileURL(dir string) (string, error) {
	if scheme, _, found := strings.Cut(dir, "://"); found {
		return "", fmt.Errorf("%q is a %s URL, not a directory: this path takes a local directory to turn into a file:// URL", dir, scheme)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return "file://" + abs, nil
}

// ExtractAddressConflictName parses the conflicting remote name from a Dolt
// "address conflict with a remote" error.
//
// Dolt returns errors of the form:
//
//	Error 1105: address conflict with a remote: 'name' -> url
//
// When BackupAdd fails because another remote (e.g. "default", registered by
// `bd backup init`) already points to the same URL, the caller can use the
// conflicting name to sync directly rather than treating it as a hard error.
// Returns "" if the error is not an address conflict.
func ExtractAddressConflictName(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	const marker = "address conflict with a remote: '"
	idx := strings.Index(s, marker)
	if idx == -1 {
		return ""
	}
	s = s[idx+len(marker):]
	end := strings.Index(s, "'")
	if end == -1 {
		return ""
	}
	return s[:end]
}
