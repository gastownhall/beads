package mysqldialect

import (
	"fmt"
	"net"
	"strconv"

	"github.com/go-sql-driver/mysql"
)

// HasPassword reports whether the DSN already carries a password. A DSN that already
// has one (a full BEADS_MYSQL_URL, or an explicit --mysql-url) wins outright: the
// credential ladder is not applied on top of it.
func HasPassword(dsn string) bool {
	cfg, err := mysql.ParseDSN(dsn)
	return err == nil && cfg.Passwd != ""
}

// WithCredential returns dsn with the resolved credential placed: the password
// always, and the username only when non-empty (a Vault-style dynamic user/password
// pair) — an empty username leaves the DSN's user untouched. It uses go-sql-driver's
// own parser (never string surgery) and VERIFIES the round-trip.
//
// The grammar has one sharp edge pgdialect does not: it cannot carry a password
// without a username (FormatDSN emits the user:pass@ block only when a user is
// present), so a userless result is refused. But a provided username satisfies that
// requirement, making a bare tcp(host)/ base DSN plus a dynamic user/password pair a
// valid configuration. This is the counterpart to mysql.RedactPassword.
func WithCredential(dsn, username, password string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("cannot place credential into the connection string (unparseable DSN): %w", err)
	}
	if username != "" {
		cfg.User = username
	}
	if cfg.User == "" {
		return "", fmt.Errorf("cannot place a password: the DSN has no username, and the MySQL DSN grammar cannot carry a password without one; add a user to the DSN")
	}
	cfg.Passwd = password
	out := cfg.FormatDSN()

	again, err := mysql.ParseDSN(out)
	if err != nil {
		return "", fmt.Errorf("cannot place credential into the connection string (unparseable result): %w", err)
	}
	if again.Passwd != password {
		return "", fmt.Errorf("password did not round-trip into the connection string; refusing to connect with a wrong or empty password")
	}
	if username != "" && again.User != username {
		return "", fmt.Errorf("username did not round-trip into the connection string; refusing to connect as the wrong user")
	}
	return out, nil
}

// WithPassword places only the password, preserving the DSN's user. It is
// WithCredential with no username override.
func WithPassword(dsn, password string) (string, error) {
	return WithCredential(dsn, "", password)
}

// HostPort returns the TCP endpoint go-sql-driver would dial for dsn, for keying the
// [host:port] credentials-file lookup. ok=false for non-TCP networks (a unix socket
// has no port) or an unparseable DSN; the caller degrades the file rung to
// not-configured rather than failing.
func HostPort(dsn string) (host string, port int, ok bool) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", 0, false
	}
	if cfg.Net != "tcp" {
		return "", 0, false
	}
	h, p, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return "", 0, false
	}
	port, err = strconv.Atoi(p)
	if err != nil {
		return "", 0, false
	}
	return h, port, true
}
