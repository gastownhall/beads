package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExportBackupName is the Dolt backup remote the auto-backup path registers
// for its local backup directory. `bd backup init` registers a different
// name, so the two can coexist until they point at the same address.
const ExportBackupName = "backup_export"

// BackupAdd registers a Dolt backup destination.
func BackupAdd(ctx context.Context, db DBConn, name, url string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('add', ?, ?)", name, url); err != nil {
		return fmt.Errorf("add backup %s: %w", name, redactBackupError(err, url))
	}
	return nil
}

// BackupSync pushes the database to the named backup destination.
func BackupSync(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('sync', ?)", name); err != nil {
		return fmt.Errorf("sync backup %s: %w", name, redactBackupError(err, ""))
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
	if _, err := neturl.Parse(url); err != nil && RedactBackupURL(url) != url {
		return fmt.Errorf("restore from backup %s: invalid backup URL: %w",
			RedactBackupURL(url), &hiddenBackupError{err: err, source: url})
	}
	if force {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', '--force', ?, ?)", url, dbName); err != nil {
			return fmt.Errorf("restore from backup %s: %w", RedactBackupURL(url), redactBackupError(err, url))
		}
	} else {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BACKUP('restore', ?, ?)", url, dbName); err != nil {
			return fmt.Errorf("restore from backup %s: %w", RedactBackupURL(url), redactBackupError(err, url))
		}
	}
	return nil
}

// backupErrorRedactor removes a backup URL from an underlying Dolt error while
// keeping that error in the chain. Dolt parse and registration errors can echo
// their URL argument byte-for-byte; wrapping them directly with %w would put
// credentials back into an otherwise-redacted diagnostic. Unwrap preserves
// errors.Is/errors.As without trusting the cause's Error string.
type backupErrorRedactor struct {
	err    error
	source string
}

func (e *backupErrorRedactor) Error() string {
	message := redactBackupURLsInText(e.err.Error())
	if e.source == "" {
		return message
	}

	replacements := map[string]string{
		e.source: RedactBackupURL(e.source),
	}
	if parsed, err := neturl.Parse(e.source); err == nil && parsed.User != nil {
		replacements[parsed.String()] = RedactBackupURL(parsed.String())
		for _, credential := range backupCredentialSpellings(e.source, parsed) {
			// Very short strings are too likely to be ordinary words or pieces of
			// an unrelated diagnostic. Full URLs and userinfo are scrubbed above;
			// individual credential spellings are useful only when non-trivial.
			if len(credential) >= 4 {
				replacements[credential] = "[redacted]"
			}
		}
	}

	keys := make([]string, 0, len(replacements))
	for old := range replacements {
		if old != "" && old != replacements[old] {
			keys = append(keys, old)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, old := range keys {
		message = strings.ReplaceAll(message, old, replacements[old])
	}
	return message
}

func (e *backupErrorRedactor) Unwrap() error {
	return e.err
}

func (e *backupErrorRedactor) As(target any) bool {
	return assignRedactedURLError(target, e.err, e.source)
}

func redactBackupError(err error, source string) error {
	if err == nil {
		return nil
	}
	// A parser error can repeat a credential fragment outside the quoted raw
	// URL (for example, as an "invalid port"). Exact URL replacement cannot
	// sanitize that shape, so hide the rendered cause when the source itself is
	// parse-invalid. BackupRestore rejects it before Dolt; BackupAdd retains
	// Dolt's bracketed aws compatibility but still cannot leak a failing cause.
	if _, parseErr := neturl.Parse(source); parseErr != nil && RedactBackupURL(source) != source {
		return &hiddenBackupError{err: err, source: source}
	}
	return &backupErrorRedactor{err: err, source: source}
}

// backupCredentialSpellings returns the raw and decoded forms that a URL
// parser or a downstream factory can emit separately from the full URL. It is
// called only for a successfully parsed URL carrying userinfo.
func backupCredentialSpellings(source string, parsed *neturl.URL) []string {
	userinfo := parsed.User.String()
	spellings := []string{userinfo}

	if sep := strings.Index(source, "://"); sep >= 0 {
		rest := source[sep+len("://"):]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rawUserinfo := rest[:at]
			spellings = append(spellings, rawUserinfo)
			if colon := strings.IndexByte(rawUserinfo, ':'); colon >= 0 {
				spellings = append(spellings, rawUserinfo[colon+1:])
			}
		}
	}

	username := parsed.User.Username()
	if password, ok := parsed.User.Password(); ok {
		spellings = append(spellings, username+":"+password, password)
		// UserPassword.String is the canonical userinfo escaping used by
		// url.URL.String (notably, it turns an @ in a password into %40).
		if escaped := neturl.UserPassword("", password).String(); len(escaped) > 1 {
			spellings = append(spellings, escaped[1:])
		}
	}
	return spellings
}

// redactBackupURLsInText scrubs recognizable backup URLs from a Dolt error
// even when the caller has only a remote name, as BackupSync does. Dolt quotes
// URLs in these diagnostics; whitespace and quote characters therefore form
// the conservative token boundary. Strings without :// are deliberately left
// to RedactBackupURL's source-aware handling rather than scanned from prose.
func redactBackupURLsInText(message string) string {
	var out strings.Builder
	for cursor := 0; cursor < len(message); {
		start := nextBackupURL(message, cursor)
		if start < 0 {
			out.WriteString(message[cursor:])
			break
		}
		out.WriteString(message[cursor:start])
		end := start
		for end < len(message) && !strings.ContainsRune(" \t\r\n'\"<>", rune(message[end])) {
			end++
		}
		out.WriteString(RedactBackupURL(message[start:end]))
		cursor = end
	}
	return out.String()
}

func nextBackupURL(message string, cursor int) int {
	next := -1
	for scheme := range backupSchemes {
		if offset := strings.Index(message[cursor:], scheme+"://"); offset >= 0 {
			candidate := cursor + offset
			if next < 0 || candidate < next {
				next = candidate
			}
		}
	}
	return next
}

// assignRedactedURLError intercepts errors.As for *url.Error so callers see a
// sanitized URL field instead of the raw credential-bearing value. The
// original cause remains unwrap-compatible for errors.Is and other error
// types; the nested parse detail is hidden only when rendering the clone.
func assignRedactedURLError(target any, err error, source string) bool {
	targetURL, ok := target.(**neturl.Error)
	if !ok {
		return false
	}
	var urlErr *neturl.Error
	if !errors.As(err, &urlErr) {
		return false
	}
	safeURL := RedactBackupURL(urlErr.URL)
	inner := urlErr.Err
	if safeURL != urlErr.URL || (source != "" && RedactBackupURL(source) != source) {
		inner = &hiddenBackupError{err: inner}
	}
	*targetURL = &neturl.Error{Op: urlErr.Op, URL: safeURL, Err: inner}
	return true
}

// hiddenBackupError keeps a rejected parser or Dolt error classifiable without
// rendering its message. net/url can repeat only a credential fragment outside
// the quoted raw URL (for example, as an "invalid port"), so replacing the URL
// alone is not enough on this path.
type hiddenBackupError struct {
	err    error
	source string
}

func (e *hiddenBackupError) Error() string {
	return "backup error details redacted"
}

func (e *hiddenBackupError) Unwrap() error {
	return e.err
}

func (e *hiddenBackupError) As(target any) bool {
	return assignRedactedURLError(target, e.err, e.source)
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

// RedactBackupURL strips the parts of a backup URL that can carry credentials
// before it is quoted in an error or echoed back to the operator: userinfo
// (aws://key:secret@...) and the query and fragment (s3 URLs carry signed
// parameters there). Scheme, host and path stay. Plain string operations are
// used because this function must also fail closed for strings url.Parse
// rejects. Wrapped Dolt errors are scrubbed separately by redactBackupError.
//
// Exported because the source is echoed outside this package too — the CLI's
// restore gate and its proxied failure messages quote it — and a second copy
// of this logic is exactly how one arm of a redaction ends up lagging another.
//
// The userinfo boundary is found FIRST, on the raw remainder, because a secret
// is arbitrary bytes: AWS secret access keys are base64, so "/" and "+" are
// ordinary characters in one, and "?" or "#" reach it through a hand-written
// password. Cutting on those separators before looking for "@" let any of them
// inside the secret defeat the redaction: "/" returned the URL untouched, and
// "?"/"#" were worse than a no-op — they emitted the access key plus a prefix
// of the secret while dropping the host and path the operator needs. Taking
// the LAST "@" on the uncut remainder over-strips when a query parameter
// carries one (an endpoint= value, say), which costs debug detail and nothing
// else. A redactor has to fail in that direction. file:// is the exception:
// an "@" in its absolute path is a filename byte, not userinfo, so only an
// authority-level "@" is treated as credentials there.
func RedactBackupURL(source string) string {
	sep := strings.Index(source, "://")
	if sep < 0 {
		// A malformed scheme can still carry userinfo. Returning it unchanged is
		// the unsafe failure mode: aws:/key:secret@bucket/db reaches both the
		// resolver and CLI gate. Preserve only the scheme for diagnostics.
		if colon := strings.IndexByte(source, ':'); colon > 0 &&
			strings.LastIndex(source[colon+1:], "@") >= 0 &&
			!strings.ContainsAny(source[:colon], `/\\`) {
			return source[:colon+1] + "[redacted]"
		}
		return source
	}
	prefix := source[:sep+len("://")]
	rest := source[sep+len("://"):]
	if source[:sep] == "file" {
		// file:///absolute/path@2024 has no authority, while
		// file://user:secret@host/path does. Search only the authority so the
		// former stays byte-identical and the latter still fails closed.
		authorityEnd := len(rest)
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			authorityEnd = slash
		}
		if at := strings.LastIndex(rest[:authorityEnd], "@"); at >= 0 {
			rest = rest[at+1:]
		}
	} else if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if cut := strings.IndexAny(rest, "?#"); cut >= 0 {
		rest = rest[:cut]
	}
	return prefix + rest
}

// DirToFileURL resolves dir to an absolute path and returns a file:// URL.
//
// An input that already carries a scheme is rejected rather than prefixed.
// filepath.Abs would happily turn "https://doltremoteapi.dolthub.com/u/r" into
// "file:///cwd/https:/doltremoteapi.dolthub.com/u/r" — a syntactically valid
// URL naming a local directory that does not exist, which DOLT_BACKUP would
// then fail on, or worse create. Restore no longer reaches here with a URL:
// ResolveBackupSource is the single entry point for a restore source on both
// the direct and the proxied route, and it passes a recognized backup URL
// through untouched, calling this only for the directory arm. What still calls
// it directly is the backup (write) direction — BackupToDir, which
// BackupDatabase in both stores and the proxied auto-backup go through — which
// is directory-only by design and stats its argument first. So does the
// directory arm above it here. The guard therefore stays as a backstop rather
// than a live path: no caller can hand it a scheme today, and if one ever
// does, being told which scheme is a better failure than a mangled
// file:///cwd/S3:/... path.
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
// DOLT_BACKUP('restore', ...). Recognized backup URLs pass through unchanged;
// anything else must be an existing local directory and is converted with
// DirToFileURL. The error strings are load-bearing: the resolver and store
// tests assert them.
//
// A remote URL is never stat'ed, since there is nothing local to stat. A
// file:// URL is the exception: it names a local directory, so that directory
// is checked like any other before the URL is passed through. DOLT_BACKUP does
// not fail on a missing file:// source. It creates the directory, opens it as
// an empty backup and, under --force, drops the live database before the
// restore fails. This check rejects a missing path, but it does not establish
// that an existing directory contains a backup: an empty directory (or an
// accessible empty remote prefix) can still reach the destructive drop under
// --force. It only provides even that limited protection if it checks the
// directory Dolt will open, so a file:// path Dolt reads differently is
// refused rather than stat'ed; see fileURLDir.
func ResolveBackupSource(source string) (string, error) {
	if IsBackupURL(source) {
		if rest, isFile := strings.CutPrefix(source, "file://"); isFile {
			dir, err := fileURLDir(source, rest)
			if err != nil {
				return "", err
			}
			if err := statBackupDir(source, dir); err != nil {
				return "", err
			}
		}
		return source, nil
	}
	if err := statBackupDir(source, source); err != nil {
		return "", err
	}
	return DirToFileURL(source)
}

// fileURLDir returns the directory DOLT_BACKUP will open for a file:// source,
// rest being everything after "file://", or refuses a path Dolt reads
// differently from a stat. Dolt parses the URL, cleans the path and makes it
// absolute against its own data directory, so:
//
//   - a relative path names a directory under the Dolt data directory, not
//     under the working directory a stat here resolves it against;
//   - %XX escapes are decoded, on more than one layer, and the path ends at
//     the first ? or #, so the directory Dolt opens has a different name;
//   - a \ is read as /, on every OS. On Windows both are the separator, but
//     elsewhere \ is an ordinary file-name byte, so the stat finds a\b where
//     Dolt opens a/b.
//
// In each case the stat passes on one directory while Dolt creates another,
// which is exactly the loss the stat is there to prevent.
//
// One reading is left to the stat rather than refused: Dolt takes a leading
// /C:/ or /C$/ for a Windows drive on every OS. Outside Windows the stat then
// looks for a C: or C$ directory at the filesystem root, which takes root to
// create, so in practice the source is refused as missing.
func fileURLDir(source, rest string) (string, error) {
	if !filepath.IsAbs(rest) {
		return "", fmt.Errorf("backup source is not an absolute file:// path: %s: "+
			"Dolt resolves a relative file:// path against its data directory, not the current directory; "+
			"pass the directory itself to restore from a relative path", RedactBackupURL(source))
	}
	if strings.ContainsAny(rest, "%?#") {
		return "", fmt.Errorf("backup source is not a plain file:// path: %s: "+
			"Dolt decodes %%-escapes and ends the path at ? or #, so it would restore from a different directory than the one named",
			RedactBackupURL(source))
	}
	if filepath.Separator != '\\' && strings.Contains(rest, `\`) {
		return "", fmt.Errorf("backup source is not a plain file:// path: %s: "+
			`Dolt reads \ as /, so it would restore from a different directory than the one named`,
			RedactBackupURL(source))
	}
	// Dolt cleans the path lexically before opening it; a stat of the raw
	// a/link/../b would follow link where Dolt does not.
	return filepath.Clean(rest), nil
}

// statBackupDir checks that dir, the local directory a restore source names,
// exists and is a directory. dir is the source itself or the path of a file://
// URL; the errors quote the source as the operator gave it, redacted.
func statBackupDir(source, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		// %w on the *fs.PathError would print its own copy of the source, and
		// this is the arm a credentialed URL falls through to: IsBackupURL is
		// case-sensitive on purpose, so S3://key:secret@bucket/db — or the
		// az:// of #6227 — is stat'ed as a path and quoted right here. Wrap
		// the stat reason instead of the PathError so errors.Is(err,
		// fs.ErrNotExist) still answers for callers while the path that
		// reaches the operator is redacted.
		return fmt.Errorf("backup source does not exist: %s: %w", RedactBackupURL(source), statReason(err))
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source is not a directory: %s", RedactBackupURL(source))
	}
	return nil
}

// statReason returns the reason inside an *fs.PathError, dropping the
// operation and the path it quotes. The path here is the restore source, which
// can carry credentials; the reason ("no such file or directory") cannot, and
// keeping it wrapped preserves errors.Is against fs.ErrNotExist.
func statReason(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
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
