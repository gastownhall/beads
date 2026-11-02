package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
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

// ErrNoBackup is wrapped by the refusal RestoreSourceURL returns for a local
// source directory that holds no Dolt backup, so a caller can attach its own
// remedy without matching message text.
var ErrNoBackup = errors.New("no Dolt backup found: restore refused before changing the database")

// RestoreSourceURL validates dir as a local directory holding a Dolt backup
// and returns its file:// URL, ready for BackupRestore.
//
// DOLT_BACKUP('restore', '--force') opens the source, drops the target
// database and recreates it before it syncs the source's roots, and Dolt's
// file store treats an absent manifest as an empty store rather than an open
// error — so a directory that merely exists drops the live database and only
// then fails (GH#7098). The checks here are the ones the stores already made
// (the directory exists and is a directory) plus the one piece of Dolt's
// backup layout that rules the drop out: a regular manifest file, identified
// the same way dolt_remote_reset_data.go recognizes a file backup store. A
// regular manifest is a layout prerequisite, not a promise that the backup is
// complete, readable or restorable; interpreting its contents is Dolt's, on
// the other side of beads' storage boundary.
//
// The check is an interim guard for that engine ordering, and it lives in
// this storage package so the layout knowledge stays behind the storage
// boundary: both stores run it in RestoreDatabase, the proxied route runs it
// as its preflight before it quiesces the topology, and the commands above
// act on the typed ErrNoBackup, never on the layout. The engine-side fix is
// proposed in dolthub/dolt#11951, which restores into a staging database and
// swaps it in only once the transfer succeeds. Once the pinned Dolt carries
// that, a failed restore leaves the live database as it was, and this check
// can go.
//
// It applies with and without --force: a directory without a manifest is
// never a usable local restore source. Symlinks are followed the way os.Stat
// follows them. A URL names no local directory, so it is refused as a
// missing source, and DirToFileURL refuses its scheme should a directory by
// that name exist.
func RestoreSourceURL(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("backup source does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("backup source is not a directory: %s", dir)
	}
	manifest, err := os.Stat(filepath.Join(dir, "manifest"))
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("%q: %w (no manifest file)", dir, ErrNoBackup)
	case err != nil:
		return "", fmt.Errorf("refusing restore from %q: %w", dir, remedy.PrintedError(err))
	case !manifest.Mode().IsRegular():
		return "", fmt.Errorf("%q: %w (manifest is not a regular file)", dir, ErrNoBackup)
	}
	return DirToFileURL(dir)
}

// BackupRestore restores a database from a backup at the given URL into
// the named database. When force is true, an existing database with the
// same name is overwritten. Mirrors the CLI: dolt backup restore [--force] <url> <db_name>
//
// A caller starting from a local directory must validate it with
// RestoreSourceURL first: the engine replaces the named database before it
// discovers a source that holds no backup, so the URL-level operation cannot
// protect it.
func BackupRestore(ctx context.Context, db DBConn, url, dbName string, force bool) error {
	if force {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', '--force', ?, ?)", url, dbName); err != nil {
			return backupRestoreError(url, err)
		}
	} else {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', ?, ?)", url, dbName); err != nil {
			return backupRestoreError(url, err)
		}
	}
	return nil
}

// backupRestoreError wraps Dolt's error for a failed restore from raw without
// printing raw's credentials (#6341). Naming the source safely is not enough:
// Dolt quotes the URL in its own errors (url.Parse's error repeats it, and the
// remotesapi factory's "could not access dolt url" prints it whole). So when
// raw carries anything sanitizeURL strips, or cannot be named safely at all,
// the error leaves Dolt's text out. It still unwraps to Dolt's error.
func backupRestoreError(raw string, err error) error {
	source, clean := backupRestoreSafeURL(raw)
	if !clean {
		return &restoreErrorWithheld{source: source, err: err}
	}
	return fmt.Errorf("restore from backup %s: %w", source, err)
}

// restoreErrorWithheld is a failed restore whose Dolt error may quote the
// backup URL's credentials, so its text is left out of Error.
type restoreErrorWithheld struct {
	source string // backupRestoreSafeURL's name for the URL
	err    error
}

func (e *restoreErrorWithheld) Error() string {
	return "restore from backup " + e.source + ": Dolt's error is withheld because it may quote credentials from the URL"
}

func (e *restoreErrorWithheld) Unwrap() error { return e.err }

// backupRestoreSafeURL names a restore source in error text without its
// credentials, and reports whether raw carries none, so that an error quoting
// it is safe to print as well. Any userinfo counts, since a token can be the
// username alone, and so does a query or fragment, which sanitizeURL drops.
//
// A file URL without userinfo, the form DirToFileURL builds, names a local
// directory and comes back as is, even where net/url rejects it (Windows'
// file://C:\..., a '%' in a directory name). Any other URL is named without
// its userinfo, query and fragment, unless net/url cannot parse it (a base64
// secret holding '/', a password holding '#' or '?'), leaves an '@' outside
// the userinfo (the same secrets where the part before the '/', '?' or '#'
// parses as a host or port), or reads it as opaque with credentials
// (scheme:KEY:SECRET@host). Then it is reduced to its scheme: sanitizeURL
// would print it raw, or print part of the secret as the host, port or path.
func backupRestoreSafeURL(raw string) (source string, clean bool) {
	if isLocalFileURL(raw) {
		return raw, true
	}
	u, err := parseRestoreURL(raw)
	if err != nil || strings.ContainsAny(u.Opaque, ":@") || strings.Contains(u.Host+u.Path+u.RawQuery+u.Fragment, "@") {
		return redactedURL(raw), false
	}
	clean = u.User == nil && u.RawQuery == "" && u.Fragment == ""
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String(), clean
}

// parseRestoreURL parses raw with net/url the way Dolt does. Dolt's
// aws://[bucket:table]/path form has a bracketed host that net/url rejects
// since Go 1.25.2 (golang/go#75678), so, like Dolt, it parses the rest with a
// stand-in host and then puts the bracketed one back.
func parseRestoreURL(raw string) (*neturl.URL, error) {
	rest, aws := strings.CutPrefix(raw, "aws://[")
	host, tail, closed := strings.Cut(rest, "]")
	if !aws || !closed {
		return neturl.Parse(raw)
	}
	u, err := neturl.Parse("aws://hostname" + tail)
	if err == nil {
		u.Host = "[" + host + "]"
	}
	return u, err
}

// isLocalFileURL reports whether raw is a file URL without userinfo:
// "file://" and an absolute path, POSIX or Windows.
func isLocalFileURL(raw string) bool {
	scheme, rest, ok := strings.Cut(raw, "://")
	authority, _, _ := strings.Cut(strings.ReplaceAll(rest, `\`, "/"), "/")
	return ok && strings.EqualFold(scheme, "file") && !strings.Contains(authority, "@")
}

// redactedURL is raw reduced to its scheme, or to nothing when what precedes
// "://" is not a plain scheme.
func redactedURL(raw string) string {
	scheme, _, ok := strings.Cut(raw, "://")
	if ok && scheme != "" && !strings.ContainsAny(scheme, ":/?#@") {
		return scheme + ":[redacted]"
	}
	return "[redacted]"
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
