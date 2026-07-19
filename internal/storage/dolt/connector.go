package dolt

import (
	"context"
	"database/sql"
	"fmt"

	mysql "github.com/go-sql-driver/mysql"
)

// openSQLDB opens a *sql.DB for a dolt sql-server DSN.
//
// When credCmd is empty (the static-user and local/embedded paths) it is exactly
// sql.Open("mysql", dsn) — byte-for-byte today's behavior.
//
// When credCmd is set (the hosted credential-command path), it returns a
// connector-backed *sql.DB whose BeforeConnect hook re-resolves a fresh cached
// credential token at EACH new physical connection dial. The gateway reads that
// token as the MySQL username, so:
//   - every new pooled connection authenticates with a live token, and
//   - EXISTING pooled connections are never re-authenticated by the server, so
//     they survive token rotation (the warm-connection property).
//
// go-sql-driver/mysql v1.9.3 clones the Config before invoking BeforeConnect
// (connector.go:70-77), so the per-dial User mutation is isolated to that dial.
// sql.Open("mysql", dsn) and sql.OpenDB(mysql.NewConnector(ParseDSN(dsn))) are
// equivalent — NewConnector normalizes the config "so calls have the same
// behavior as MySQLDriver.OpenConnector" (driver.go), and ReadTimeout/
// WriteTimeout/TLSConfig ride mysql.Config fields through both paths.
func openSQLDB(dsn, credCmd string) (*sql.DB, error) {
	if credCmd == "" {
		return sql.Open("mysql", dsn)
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DSN for credential connector: %w", err)
	}
	if err := cfg.Apply(mysql.BeforeConnect(credentialBeforeConnect(credCmd))); err != nil {
		return nil, fmt.Errorf("applying credential connector hook: %w", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating credential connector: %w", err)
	}
	return sql.OpenDB(connector), nil
}

// credentialBeforeConnect returns a mysql BeforeConnect hook that stamps a fresh
// cached credential token onto each new physical connection's username.
//
// resolveCredentialToken (credcmd.go) is process-cached with a 10s pre-expiry
// skew, so the common case is a mutex-guarded map hit (~µs); only near the token
// expiry boundary does it shell out to the helper. On any resolution error the
// hook fails closed — the dial is aborted rather than falling back to a stale
// token. It is a named function so tests can invoke it without a live server.
func credentialBeforeConnect(credCmd string) func(context.Context, *mysql.Config) error {
	return func(ctx context.Context, c *mysql.Config) error {
		tok, err := resolveCredentialToken(ctx, credCmd)
		if err != nil {
			return fmt.Errorf("resolving dolt credential command: %w", err)
		}
		c.User = tok
		return nil
	}
}
