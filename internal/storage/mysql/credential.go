package mysql

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
	"github.com/steveyegge/beads/internal/storage/mysqldialect"
)

// resolveDSNCredential returns baseDSN with a credential resolved and placed, ready to
// connect. The precedence, high to low:
//
//   - BEADS_MYSQL_CREDENTIAL_COMMAND set is a hard error: an identity credential is
//     presented as a username to a gateway, and this backend connects directly;
//   - the DSN already carries a password (a full BEADS_MYSQL_URL, or an explicit
//     --mysql-url) — it wins outright, no ladder is applied;
//   - BEADS_MYSQL_PASSWORD_COMMAND — a credential command (e.g. Vault / RDS-IAM / GCP-IAM);
//     run at open time, cached until near expiry;
//   - BEADS_MYSQL_PASSWORD — a static password;
//   - the credentials file [host:port] ($BEADS_CREDENTIALS_FILE or
//     ~/.config/beads/credentials);
//   - nothing configured — baseDSN is returned untouched and the connection is
//     attempted with an empty password (valid only for an unsecured dev server;
//     go-sql-driver reads no env vars or option files, so there is no ~/.my.cnf
//     fallback).
//
// It fails closed: if a configured credential command errors, the open aborts — it
// never silently downgrades to a lower rung.
func resolveDSNCredential(ctx context.Context, cfg *configfile.Config, baseDSN string) (string, error) {
	if os.Getenv("BEADS_MYSQL_CREDENTIAL_COMMAND") != "" {
		return "", fmt.Errorf("mysql: BEADS_MYSQL_CREDENTIAL_COMMAND is reserved: an identity credential is presented as a username to a gateway, and the mysql backend connects directly to the database with no gateway to present it to; use BEADS_MYSQL_PASSWORD_COMMAND for a helper that produces a password")
	}
	// host:port keys the credentials-file lookup; ok=false (unix socket / unparseable)
	// leaves the file rung not-configured (zero Host/Port), never failing.
	host, port, _ := mysqldialect.HostPort(baseDSN)
	return resolveDSNWithSources(ctx, baseDSN,
		creds.CommandSource{Command: cfg.GetMySQLPasswordCommand(), Kind: creds.KindSecret, Label: "BEADS_MYSQL_PASSWORD_COMMAND"},
		creds.EnvSource{Var: "BEADS_MYSQL_PASSWORD"},
		creds.FileSource{Host: host, Port: port, Lookup: configfile.LookupCredentialsPassword, Label: "credentials-file"},
	)
}

// resolveDSNWithSources is the testable core of resolveDSNCredential: it applies the
// given credential ladder to baseDSN.
func resolveDSNWithSources(ctx context.Context, baseDSN string, sources ...creds.Source) (string, error) {
	if mysqldialect.HasPassword(baseDSN) {
		return baseDSN, nil
	}
	cred, ok, err := creds.ResolveLadder(ctx, sources...)
	if err != nil {
		return "", err
	}
	if !ok {
		return baseDSN, nil
	}
	// The SQL backends take a secret in the password slot; an identity credential
	// (presented as a username to a gateway) has no home on a direct connection, so
	// refuse it loudly rather than land a token where a password belongs. Defense in
	// depth behind the BEADS_MYSQL_CREDENTIAL_COMMAND reject above.
	if cred.Kind != creds.KindSecret {
		return "", fmt.Errorf("mysql: credential from %s is an identity, not a password; the mysql backend connects directly and has no gateway to present it to", cred.Source)
	}
	return mysqldialect.WithCredential(baseDSN, cred.Username, cred.Value)
}
