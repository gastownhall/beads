package mysql

import (
	"fmt"

	gomysql "github.com/go-sql-driver/mysql"
)

// RedactPassword returns dsn with the password removed, or an error if it cannot be
// stripped. It uses go-sql-driver's own parser (never hand-rolled string surgery) and
// verifies with a re-parse that no password survives, failing closed. bd init persists
// the returned string to metadata.json; the password is re-supplied at open time by
// the credential ladder (see resolveDSNCredential in credential.go).
func RedactPassword(dsn string) (string, error) {
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse DSN: %w", err)
	}
	cfg.Passwd = ""
	stripped := cfg.FormatDSN()
	if again, err := gomysql.ParseDSN(stripped); err != nil || again.Passwd != "" {
		return "", fmt.Errorf("refusing to persist a connection string that still carries a password; supply it via BEADS_MYSQL_PASSWORD instead of embedding it in --mysql-url")
	}
	return stripped, nil
}

// withDatabase returns dsn with its default database set to database (or cleared when
// database is ""). Used to derive a server connection (for CREATE DATABASE) and the
// per-workspace connection from one base DSN. It also forces parseTime and UTC so
// datetime columns round-trip identically to the Dolt reference.
func withDatabase(dsn, database string) (string, error) {
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	// The go-sql-driver grammar cannot carry a password without a username —
	// FormatDSN would silently drop it. Refuse loudly rather than reformat this DSN
	// into a passwordless one and connect (the same invariant mysqldialect.WithPassword
	// enforces; this covers the path where a userless password-bearing DSN reaches us
	// directly, e.g. an inline ":secret@tcp(host)/" URL that skips the credential ladder).
	if cfg.User == "" && cfg.Passwd != "" {
		return "", fmt.Errorf("connection string carries a password but no username; the MySQL DSN grammar cannot carry a password without a user (it would be silently dropped) — add a user to the DSN")
	}
	cfg.DBName = database
	cfg.ParseTime = true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	// Pin the session time zone to UTC so CURRENT_TIMESTAMP defaults and reads match
	// the shared layer's UTC assumption (and the Dolt reference).
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg.FormatDSN(), nil
}
