package versioncontrolops

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// BackupRestore restores a database from a backup at the given URL into
// the named database. When force is true, an existing database with the
// same name is overwritten. Mirrors the CLI: dolt backup restore [--force] <url> <db_name>
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

// backupManifestName is the file a Dolt backup keeps at the top of its
// directory: Dolt's file manifest (store/nbs/file_manifest.go in the pinned
// Dolt names it "manifest"). `bd backup sync` writes it on every sync.
const backupManifestName = "manifest"

// RestoreSourceURL returns the file:// URL a restore from the local directory
// dir hands DOLT_BACKUP, after checking that the directory it names holds a
// Dolt backup, so a restore can refuse a source before replacing anything. A
// forced DOLT_BACKUP('restore') opens the source, drops the live database, and
// only then syncs the backup into its replacement; an existing directory
// without a manifest opens as an empty store, so without this check an empty
// or wrong directory costs the database.
//
// The checks run on the path in the returned URL, the directory Dolt opens,
// not on dir as typed: filepath.Abs resolves a ".." lexically where the
// kernel would follow a symlink first. A path Dolt does not read literally
// (fileURLSyntax) is refused before anything is checked.
//
// It checks only that the backup's manifest is present as a regular file, not
// that the backup is complete or readable. That check is an interim guard for
// the engine's drop-before-sync order, kept in this storage package so the
// commands never handle the backup layout; it can go once the pinned Dolt
// restores into a staging database and swaps it in only after the transfer
// succeeds (dolthub/dolt#11951). The path refusal stays: staging does not
// change which directory Dolt reads.
func RestoreSourceURL(dir string) (string, error) {
	url, err := DirToFileURL(dir)
	if err != nil {
		return "", err
	}
	path := strings.TrimPrefix(url, "file://")
	if i := strings.IndexAny(path, fileURLSyntax()); i >= 0 {
		return "", fmt.Errorf("backup source %s cannot be restored where it is: Dolt does not take '%c' in a file:// URL literally and would restore from a different directory; move or rename the backup to a path without '%c'", path, path[i], path[i])
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("backup source does not exist: %w", err)
		}
		return "", fmt.Errorf("check backup source %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("backup source is not a directory: %s", path)
	}
	info, err = os.Stat(filepath.Join(path, backupManifestName))
	switch {
	case err == nil && info.Mode().IsRegular():
		return url, nil
	case err == nil, os.IsNotExist(err):
		return "", fmt.Errorf("backup source %s holds no Dolt backup: it has no regular %q file at its top level", path, backupManifestName)
	default:
		return "", fmt.Errorf("check backup source %s: %w", path, err)
	}
}

// fileURLSyntax returns the characters a restore source's absolute path must
// not hold. Dolt parses the file:// URL DirToFileURL builds with net/url,
// which ends the path at '#' (a fragment) or '?' (a query) and decodes a
// '%XX' escape (any other '%' fails the parse), then turns each '\' into '/',
// and opens the directory that is left, creating it when it is missing. A
// restore from a backup at such a path therefore reads another directory:
// under --force Dolt drops the live database before it finds that one empty,
// or restores whatever backup it holds. Escaping the path does not help:
// Dolt rebuilds the URL from the decoded path and parses it again. A
// Windows drive-letter path skips net/url and takes '\' as its separator, but
// Dolt's file store unescapes the path a second time, so '%' is refused on
// every platform.
func fileURLSyntax() string {
	if runtime.GOOS == "windows" {
		return "%"
	}
	return `#?%\`
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
