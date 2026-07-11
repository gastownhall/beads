package pgdialect

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// kvPasswordRe matches a libpq keyword/value password token (password= or
// sslpassword=), whose value is either single-quoted or a run of non-space chars.
var kvPasswordRe = regexp.MustCompile(`(?i)(^|\s)(?:password|sslpassword)\s*=\s*(?:'(?:[^'\\]|\\.)*'|\S*)`)

// pwValueRe captures the VALUE of a libpq keyword/value password token: group 1 is
// the single-quoted body, group 2 is an unquoted run of non-space characters.
var pwValueRe = regexp.MustCompile(`(?i)(?:^|\s)(?:password|sslpassword)\s*=\s*(?:'((?:[^'\\]|\\.)*)'|(\S+))`)

// RedactPassword returns dsn with the password removed, or an error if a password
// cannot be safely removed. It strips every known password location — URL userinfo,
// URL query params (password, sslpassword), and libpq keyword/value tokens — then
// VERIFIES that no EMBEDDED password survives, failing closed rather than persisting a
// secret in a DSN shape we did not anticipate. The check is embedded-only (HasPassword,
// not pgx.ParseConfig.Password) so an ambient PGPASSWORD/~/.pgpass — which pgx would
// fold in but which is never written to disk — does not falsely block init.
//
// Callers persist the returned string (e.g. to metadata.json) and re-supply the
// password at open time via the credential ladder (see the backend's resolveDSNCredential),
// so nothing operational is lost by never writing the password to disk. The hard
// invariant — a password must never be persisted — is enforced here, not assumed.
func RedactPassword(dsn string) (string, error) {
	stripped := stripPasswordBestEffort(dsn)
	// It must still parse, or we cannot reason about it — refuse a shape we mangled.
	if _, err := pgx.ParseConfig(stripped); err != nil {
		return "", fmt.Errorf("cannot verify the connection string is free of a password (unparseable after redaction): %w", err)
	}
	if HasPassword(stripped) {
		return "", fmt.Errorf("refusing to persist a connection string that still carries a password; supply it via BEADS_PG_PASSWORD instead of embedding it in --pg-url")
	}
	return stripped, nil
}

// stripPasswordBestEffort removes passwords from the two connection-string shapes
// pgx accepts. The authoritative guarantee is RedactPassword's post-parse check; this
// only has to handle the common forms well enough not to trip that check falsely.
func stripPasswordBestEffort(dsn string) string {
	// URL form (scheme://...): strip userinfo password and query password/sslpassword.
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		if u.User != nil {
			if name := u.User.Username(); name == "" {
				u.User = nil
			} else {
				u.User = url.User(name)
			}
		}
		q := u.Query()
		q.Del("password")
		q.Del("sslpassword")
		u.RawQuery = q.Encode()
		return u.String()
	}
	// libpq keyword/value form (host=... password=... ...): drop the password tokens
	// and collapse surrounding whitespace so the remainder still parses.
	out := kvPasswordRe.ReplaceAllString(dsn, "$1")
	return strings.TrimSpace(strings.Join(strings.Fields(out), " "))
}

// ScrubDSNError returns a new error whose message has every cleartext password
// embedded in dsn removed. pgx's ParseConfigError redacts only URL userinfo, so a
// `?password=`/`?sslpassword=` URL query param — the shape `bd init` connects with —
// otherwise survives verbatim in the error text and leaks the secret to logs on any
// parse or TLS-config failure. The returned error intentionally does NOT wrap err:
// the pgx error's own Error() still contains the secret, so exposing it through the
// unwrap chain would defeat the redaction. Non-nil in, non-nil out; nil passes through.
func ScrubDSNError(dsn string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, secret := range dsnPasswordValues(dsn) {
		msg = strings.ReplaceAll(msg, secret, "xxxxx")
	}
	return errors.New(msg)
}

// dsnPasswordValues returns every cleartext password embedded in dsn — URL userinfo,
// URL query params (password, sslpassword), and libpq keyword/value tokens — in both
// the raw (as-written) and percent-decoded forms, so a value matches however the
// connection string is echoed back. Empty values are skipped, and results are
// de-duplicated.
func dsnPasswordValues(dsn string) []string {
	seen := map[string]bool{}
	var vals []string
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		vals = append(vals, v)
	}
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		if u.User != nil {
			if pw, ok := u.User.Password(); ok {
				add(pw)
			}
		}
		// pgx echoes the raw query string back verbatim, so match the raw value;
		// also add the decoded value in case a caller logs the parsed form.
		for _, pair := range strings.Split(u.RawQuery, "&") {
			key, val, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(key) {
			case "password", "sslpassword":
				add(val)
				if dec, err := url.QueryUnescape(val); err == nil {
					add(dec)
				}
			}
		}
	}
	for _, m := range pwValueRe.FindAllStringSubmatch(dsn, -1) {
		add(m[1]) // single-quoted body
		add(m[2]) // unquoted token
	}
	return vals
}
