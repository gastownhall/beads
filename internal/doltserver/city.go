package doltserver

import (
	"fmt"

	"github.com/steveyegge/beads/internal/config"
)

// The city is the only Dolt lifecycle surface for a store it owns. Every
// message bd prints about starting, restarting or repairing the server flows
// through these helpers so the city variant and the upstream variant live in
// one place and never disagree. beadsDir selects the store whose ownership is
// read (see config.GasCityEndpointOrigin); "" consults the merged CLI config
// only.

// StartHint returns the command a user runs to bring the Dolt server up for
// the store at beadsDir: `gc start` when Gas City owns it, `bd dolt start`
// otherwise.
func StartHint(beadsDir string) string {
	if config.CityOwnsDolt(beadsDir) {
		return "gc start"
	}
	return "bd dolt start"
}

// RestartHint returns the command that restarts the Dolt server so a changed
// server setting takes effect.
func RestartHint(beadsDir string) string {
	if config.CityOwnsDolt(beadsDir) {
		return "gc stop && gc start"
	}
	return "bd dolt stop && bd dolt start"
}

// StatusHint returns the command that reports the Dolt server state for the
// store at beadsDir.
func StatusHint(beadsDir string) string {
	if config.CityOwnsDolt(beadsDir) {
		return "gc doctor"
	}
	return "bd dolt status"
}

// CityRefusal renders the fixed error printed when `bd dolt <verb>` is invoked
// against a store Gas City owns.
func CityRefusal(beadsDir, verb string) string {
	return fmt.Sprintf("Error: Gas City owns this store's Dolt server (%s=%s). "+
		"Use `gc start` / `gc stop` / `gc doctor`; `bd dolt %s` is refused here.",
		config.GasCityEndpointOriginKey, config.GasCityEndpointOrigin(beadsDir), verb)
}
