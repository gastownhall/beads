package dolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
)

// ApplyPasswordCommand resolves the server secret command
// (BEADS_DOLT_PASSWORD_COMMAND) into cfg.ServerPassword. It mirrors
// ApplyGatewayCredential's mechanics exactly — same env-only command source, same
// fail-closed walk — but for a KindSecret credential placed in the connection
// password slot instead of a KindIdentity token placed in the username slot.
//
// It is a no-op ((false, nil)) when cfg.ServerPassword is already set (a
// caller/flag preset, or an earlier BEADS_DOLT_PASSWORD env var already applied by
// the caller, wins) or no command is configured. It fails closed: a
// configured-but-failing command aborts the open and never falls back to
// GetDoltServerPasswordForPort's static BEADS_DOLT_PASSWORD / credentials-file tier
// — a wrong or stale password must never silently apply.
func ApplyPasswordCommand(ctx context.Context, fileCfg *configfile.Config, cfg *Config) (bool, error) {
	if cfg.ServerPassword != "" {
		return false, nil
	}
	cred, ok, err := creds.ResolveLadder(ctx, creds.CommandSource{
		Command: fileCfg.GetDoltPasswordCommand(),
		Kind:    creds.KindSecret,
		Label:   "BEADS_DOLT_PASSWORD_COMMAND",
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	// Defense in depth: the value is placed in the password slot, so a
	// non-secret credential must never reach it.
	if cred.Kind != creds.KindSecret {
		return false, fmt.Errorf("dolt: credential from %s is not a secret; refusing to place it in the connection password", cred.Source)
	}
	cfg.ServerPassword = cred.Value
	return true, nil
}
