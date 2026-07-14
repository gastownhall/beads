package pgdialect

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// kvUserRe matches a libpq keyword/value user token, whose value is either
// single-quoted or a run of non-space chars. Like kvPasswordRe (redact.go) it is a
// token-level regex, not a full parser, so a ` user=` sequence inside another token's
// quoted value could be matched — an exotic case (a keyword-form DSN whose quoted
// value contains ` user=`, combined with a username-bearing credential). The
// round-trip verify in WithCredential catches the loud failures; the silent case is a
// documented sharp edge shared with kvPasswordRe.
var kvUserRe = regexp.MustCompile(`(?i)(^|\s)user\s*=\s*(?:'(?:[^'\\]|\\.)*'|\S*)`)

// kvHasPasswordRe detects a libpq keyword/value connection password token (password=,
// not sslpassword=) for the DSN-embedded check in HasPassword.
var kvHasPasswordRe = regexp.MustCompile(`(?i)(^|\s)password\s*=`)

// HasPassword reports whether the connection string itself EMBEDS a password — URL
// userinfo, a URL ?password= query param, or a libpq password= token. It deliberately
// does NOT report whether pgx would resolve a password from the ambient environment:
// pgx.ParseConfig folds PGPASSWORD and ~/.pgpass into cfg.Password, so parsing here
// would let a stale PGPASSWORD short-circuit the whole ladder and mask a configured
// command or credentials file. An embedded password is the only thing that wins
// outright; ambient PGPASSWORD/~/.pgpass remain the driver-native fallback, consulted
// at connect only when no ladder rung is configured.
func HasPassword(dsn string) bool {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		if u.User != nil {
			if _, ok := u.User.Password(); ok {
				return true
			}
		}
		return u.Query().Get("password") != ""
	}
	return kvHasPasswordRe.MatchString(dsn)
}

// WithCredential returns dsn with the resolved credential placed: the password
// always, and the username only when non-empty (a Vault-style dynamic user/password
// pair) — an empty username leaves the DSN's user untouched. It handles both grammars
// pgx accepts (URL userinfo for scheme:// DSNs, single-quoted keyword/value tokens
// otherwise) and VERIFIES with the same parser pgx uses that both fields round-tripped,
// failing loudly rather than emit a DSN that silently drops or mangles the secret.
func WithCredential(dsn, username, password string) (string, error) {
	var out string
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		// URL form: set the userinfo. url.String() percent-encodes special characters
		// (an RDS-IAM token's '&'/'='/'/'), which pgx.ParseConfig decodes back — the
		// verify below proves it.
		user := username
		if user == "" && u.User != nil {
			user = u.User.Username()
		}
		u.User = url.UserPassword(user, password)
		out = u.String()
	} else {
		// libpq keyword/value form: drop any existing password token (and, when we are
		// overriding the user, the user token too), then append single-quoted tokens.
		stripped := kvPasswordRe.ReplaceAllString(dsn, "$1")
		if username != "" {
			stripped = kvUserRe.ReplaceAllString(stripped, "$1")
		}
		stripped = strings.TrimSpace(strings.Join(strings.Fields(stripped), " "))
		if username != "" {
			stripped += " user='" + escapeKeywordValue(username) + "'"
		}
		out = stripped + " password='" + escapeKeywordValue(password) + "'"
	}

	cfg, err := pgx.ParseConfig(out)
	if err != nil {
		return "", fmt.Errorf("cannot place credential into the connection string (unparseable result): %w", err)
	}
	if cfg.Password != password {
		return "", fmt.Errorf("password did not round-trip into the connection string; refusing to connect with a wrong or empty password")
	}
	if username != "" && cfg.User != username {
		return "", fmt.Errorf("username did not round-trip into the connection string; refusing to connect as the wrong user")
	}
	return out, nil
}

// WithPassword places only the password, preserving the DSN's user. It is
// WithCredential with no username override. This is the counterpart to
// RedactPassword: RedactPassword strips the password for persistence, WithPassword
// re-injects the resolved password at open time.
func WithPassword(dsn, password string) (string, error) {
	return WithCredential(dsn, "", password)
}

// HostPort returns the TCP endpoint pgx would connect to for dsn, for keying the
// [host:port] credentials-file lookup. ok=false when the DSN does not parse or
// resolves to a unix-socket directory (an absolute-path host) — a peer-auth socket
// has no place in a password file, and the caller degrades the file rung to
// not-configured. For a multi-host DSN this is the primary endpoint only. IPv6 hosts
// are returned bare (e.g. "::1"); the credentials-file key format does not bracket
// them, so an IPv6 endpoint effectively degrades to not-found in the file.
func HostPort(dsn string) (host string, port int, ok bool) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", 0, false
	}
	if cfg.Host == "" || strings.HasPrefix(cfg.Host, "/") {
		return "", 0, false
	}
	return cfg.Host, int(cfg.Port), true
}

// escapeKeywordValue escapes a value for a single-quoted libpq keyword/value token.
func escapeKeywordValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}
