package versioncontrolops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
			return fmt.Errorf("restore from backup %s: %w", redactBackupURL(url), err)
		}
	} else {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', ?, ?)", url, dbName); err != nil {
			return fmt.Errorf("restore from backup %s: %w", redactBackupURL(url), err)
		}
	}
	return nil
}

// redactBackupURL strips the parts of a backup URL that can carry credentials
// before BackupRestore quotes it in an error: userinfo (aws://key:secret@...)
// and the query and fragment (s3 URLs carry signed parameters there). Scheme,
// host and path stay. Plain string operations rather than url.Parse, which
// rejects Dolt's bracketed aws://[dynamo_table:bucket]/db form (see
// IsBackupURL). Only the wrapper's copy of the URL is redacted; the wrapped
// Dolt error text is not rewritten.
func redactBackupURL(source string) string {
	sep := strings.Index(source, "://")
	if sep < 0 {
		return source
	}
	rest := source[sep+len("://"):]
	if cut := strings.IndexAny(rest, "?#"); cut >= 0 {
		rest = rest[:cut]
	}
	authority := rest
	if slash := strings.Index(rest, "/"); slash >= 0 {
		authority = rest[:slash]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	return source[:sep+len("://")] + rest
}

// DirToFileURL resolves dir to an absolute path and returns a file:// URL.
func DirToFileURL(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return "file://" + abs, nil
}

// backupSchemes are the URL schemes DOLT_BACKUP accepts as a destination or
// restore source. Deliberately not doltremote.NativeSchemes: that list is the
// clone/push vocabulary, excludes the http(s) backups bd already supports and
// includes git+ schemes that are not backup targets.
// az:// is a valid Dolt scheme (dbfactory/az.go) but is missing here and in
// NativeSchemes alike; tracked in #6227.
var backupSchemes = map[string]bool{
	"http": true, "https": true, "file": true, "aws": true, "gs": true, "s3": true,
}

// IsBackupURL reports whether raw carries a scheme DOLT_BACKUP accepts. It
// classifies by scheme token only (everything before the first "://",
// case-sensitive) and does not validate the URL: Go 1.25.2+ url.Parse rejects
// Dolt's bracketed aws://[dynamo_table:bucket]/db form (Dolt itself carries a
// shim for it, earl.ParseRawWithAWSSupport), so parse-validity is the wrong
// signal. URL validity stays Dolt's job. Everything downstream matches
// case-sensitively too: Dolt's dbfactory registry, remotecache and
// doltremote all key on the lowercase scheme, so S3:// falls through to the
// directory path and its clearer "backup source does not exist" error
// instead of a Dolt internal error.
func IsBackupURL(raw string) bool {
	sep := strings.Index(raw, "://")
	if sep <= 0 {
		return false
	}
	return backupSchemes[raw[:sep]]
}

// ResolveBackupSource turns a restore source into the URL passed to
// DOLT_BACKUP('restore', ...). Recognized backup URLs pass through unchanged
// and are never stat'ed; anything else must be an existing local directory
// and is converted with DirToFileURL. The error strings are load-bearing:
// the resolver and store tests assert them.
func ResolveBackupSource(source string) (string, error) {
	if IsBackupURL(source) {
		return source, nil
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("backup source does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("backup source is not a directory: %s", source)
	}
	return DirToFileURL(source)
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
