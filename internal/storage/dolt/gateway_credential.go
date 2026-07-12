package dolt

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
)

// ApplyGatewayCredential resolves the server credential command
// (BEADS_DOLT_CREDENTIAL_COMMAND) into cfg: the command's short-lived token becomes the
// connection (MySQL) username, and the connection is marked as targeting a gateway
// server. It is the vendor-neutral credential-process idiom (kubectl ExecCredential /
// AWS credential_process / git credential helper) — bd runs a command the operator
// configures and knows nothing of the issuer.
//
// Presenting the token as the username is how bd connects to an authenticating gateway
// server: the server verifies the token, routes by the project database, and owns the
// schema (so bd stays a passive client — it does not create databases or run
// migrations). This is the one place a token is placed in the username slot; the direct
// SQL backends only ever place a secret in the password slot.
//
// It is a no-op ((false, nil)) when cfg.ServerUser is already set (a caller/flag preset
// wins) or no command is configured. It fails closed: a configured-but-failing command
// aborts the open and never falls back to the static/root user — a wrong identity must
// never connect. It also disables auto-start: a gateway server is externally managed, so
// spawning a local dolt server would shadow it.
func ApplyGatewayCredential(ctx context.Context, fileCfg *configfile.Config, cfg *Config) (bool, error) {
	if cfg.ServerUser != "" {
		return false, nil
	}
	cred, ok, err := creds.ResolveLadder(ctx, creds.CommandSource{
		Command: fileCfg.GetDoltCredentialCommand(),
		Kind:    creds.KindIdentity,
		Label:   "BEADS_DOLT_CREDENTIAL_COMMAND",
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	// Defense in depth: the token is presented AS the username, so a non-identity
	// credential must never reach this slot.
	if cred.Kind != creds.KindIdentity {
		return false, fmt.Errorf("dolt: credential from %s is not an identity; refusing to present it as the connection username", cred.Source)
	}
	// The token becomes the DSN username; the go-sql-driver grammar has no escaping for
	// the user field, so a ':' '@' or '/' would silently mis-split it into user/password.
	// Reject rather than connect with a mangled identity. (JWTs are base64url + '.', safe.)
	if strings.ContainsAny(cred.Value, ":@/") {
		return false, fmt.Errorf("dolt: credential from %s contains a character (:, @, or /) that cannot be placed in the connection username", cred.Source)
	}
	// cred.Username (a dynamic user/password pair) is meaningless here: the token IS the
	// username. Ignored deliberately.
	cfg.ServerUser = cred.Value
	cfg.Gateway = true
	cfg.DisableAutoStart = true
	return true, nil
}
