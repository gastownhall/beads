package versioncontrolops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
