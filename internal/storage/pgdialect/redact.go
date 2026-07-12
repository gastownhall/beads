package pgdialect

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// dsnWSClass is the regexp character class spelling out every byte pgx treats as
// whitespace between libpq keyword/value tokens. Go's regexp \s is [\t\n\f\r ] and
// does NOT include \v (vertical tab); pgx.ParseConfig DOES accept \v as a token
// separator (confirmed: "host=h\vpassword=x" parses host="h", password="x"). Relying
// on \s here would silently fail to match a \v-separated password token at all,
// leaking it in full — so the class is spelled out explicitly instead.
const dsnWSClass = `\t\n\f\r \v`

// kvPasswordRe matches a libpq keyword/value password token (password= or
// sslpassword=), whose value is either single-quoted or an unquoted run of
// non-whitespace characters that may itself contain backslash-escaped whitespace
// (pgx accepts password=SUPER\ SECRET as the single value `SUPER\ SECRET`, backslash
// retained literally — confirmed against pgx.ParseConfig).
var kvPasswordRe = regexp.MustCompile(`(?i)(^|[` + dsnWSClass + `])(?:password|sslpassword)[` + dsnWSClass + `]*=[` + dsnWSClass + `]*(?:'(?:[^'\\]|\\.)*'|(?:[^` + dsnWSClass + `\\]|\\.)*)`)

// pwValueRe captures the VALUE of a libpq keyword/value password token: group 1 is
// the single-quoted body, group 2 is an unquoted run of non-whitespace characters,
// including any backslash-escaped whitespace sequences (see kvPasswordRe).
var pwValueRe = regexp.MustCompile(`(?i)(?:^|[` + dsnWSClass + `])(?:password|sslpassword)[` + dsnWSClass + `]*=[` + dsnWSClass + `]*(?:'((?:[^'\\]|\\.)*)'|((?:[^` + dsnWSClass + `\\]|\\.)+))`)

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

// ScrubDSNString returns s with every cleartext password embedded in dsn replaced
// by "xxxxx". It is the string-level primitive behind ScrubDSNError, exposed so a
// caller that echoes a DSN somewhere other than an error — a telemetry span, a log
// line — can redact it through the SAME parser-backed extraction (URL userinfo, URL
// query password/sslpassword, and libpq keyword/value tokens) instead of a separate
// hand-rolled scan that misses a form. dsn and s are usually the same string (scrub a
// DSN in place); they differ only when the password leaked into surrounding text, as
// ScrubDSNError passes an error message as s. An empty dsn, or one with no password,
// returns s unchanged.
func ScrubDSNString(dsn, s string) string {
	secrets := dsnPasswordValues(dsn)
	// Replace the longest secrets first. dsnPasswordValues can return one secret that
	// is a byte-for-byte prefix of another (e.g. a userinfo password "foo" and a query
	// password "fooACTUAL"); replacing the short one first would turn "fooACTUAL" into
	// "xxxxxACTUAL" before the long-secret pass ever runs, leaking the "ACTUAL" tail
	// since ReplaceAll can no longer find the (now-mangled) longer substring.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, "xxxxx")
	}
	return s
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
	return errors.New(ScrubDSNString(dsn, err.Error()))
}

// isPasswordKey reports whether k names one of the URL query password params
// (case-insensitively).
func isPasswordKey(k string) bool {
	switch strings.ToLower(k) {
	case "password", "sslpassword":
		return true
	}
	return false
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
				// url.Parse percent-decodes userinfo, so u.User.Password() is only
				// the decoded value. pgx and telemetry sinks echo the DSN verbatim,
				// so also scrub the raw as-written form — mirroring the query-param
				// branch below, which adds both raw and decoded values.
				add(pw)
				add(rawURLUserinfoPassword(dsn))
			}
		}
		// pgx echoes the raw query string back verbatim, so match the raw value;
		// also add the decoded value in case a caller logs the parsed form.
		for _, pair := range strings.Split(u.RawQuery, "&") {
			key, val, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			// url.Query() (which pgx.ParseConfig uses) percent-decodes query KEYS as
			// well as values, so a query key of pass%77ord or %70assword is a live
			// "password" param even though the raw key string doesn't say so. Compare
			// both the raw (as-written) key and its unescaped form — ignoring an
			// unescape error and falling back to the raw compare, which already ran.
			matched := isPasswordKey(key)
			if !matched {
				if dk, err := url.QueryUnescape(key); err == nil {
					matched = isPasswordKey(dk)
				}
			}
			if matched {
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

// rawURLUserinfoPassword returns the userinfo password of a URL-form DSN exactly
// as written — still percent-encoded — or "" when there is none. url.Parse decodes
// userinfo, so an encoded secret like SUPER%2ASECRET survives verbatim when a sink
// echoes the raw DSN; extracting it structurally here (rather than re-encoding the
// decoded value) captures the original bytes regardless of encoding form. Mirrors
// the authority split url.Parse performs: password is after the first ':' of the
// userinfo, which is everything before the last '@' of the authority.
func rawURLUserinfoPassword(dsn string) string {
	i := strings.Index(dsn, "://")
	if i < 0 {
		return ""
	}
	authority := dsn[i+len("://"):]
	if j := strings.IndexAny(authority, "/?#"); j >= 0 {
		authority = authority[:j]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return ""
	}
	_, pw, ok := strings.Cut(authority[:at], ":")
	if !ok {
		return ""
	}
	return pw
}
