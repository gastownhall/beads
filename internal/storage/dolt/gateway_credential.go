package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
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
	command := fileCfg.GetDoltCredentialCommand()
	if command == "" {
		return false, nil
	}
	if err := CheckServerEndpointTrust(cfg.BeadsDir, fileCfg, cfg.ServerHost, cfg.ServerPort, cfg.ServerSocket); err != nil {
		return false, err
	}
	// The token resolution is doltserver.GatewayIdentity's, which a dial
	// through the endpoint presents too (doltserver.Endpoint.WithIdentity).
	token, ok, err := doltserver.GatewayIdentity(ctx, command)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	cfg.ServerUser = token
	cfg.Gateway = true
	cfg.DisableAutoStart = true
	return true, nil
}

// CheckServerEndpointTrust validates a Dolt server endpoint before gateway credentials are used.
func CheckServerEndpointTrust(beadsDir string, fileCfg *configfile.Config, host string, port int, socket string) error {
	return doltserver.CheckEndpointTrust(beadsDir, fileCfg, host, port, socket)
}
