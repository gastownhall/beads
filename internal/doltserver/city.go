package doltserver

import (
	"fmt"

	"github.com/steveyegge/beads/internal/config"
)

// The city is the only Dolt lifecycle surface for a store it owns. Every
// message bd prints about starting, restarting or checking the server flows
// through these helpers so the city variant and the upstream variant live in
// one place and never disagree. beadsDir is the resolved store whose
// config.yaml decides ownership (see config.ReadGasCityEndpointOrigin); an
// unreadable or invalid stamp is returned as an error.

// lifecycleHint returns the city command configured under cityKey when Gas
// City owns the store at beadsDir, and upstream otherwise.
func lifecycleHint(beadsDir, cityKey, upstream string) (string, error) {
	owned, err := config.CityOwnsDolt(beadsDir)
	if err != nil {
		return "", err
	}
	if !owned {
		return upstream, nil
	}
	return config.GasCityCommand(cityKey)
}

// StartHint returns the command a user runs to bring the Dolt server up for
// the store at beadsDir.
func StartHint(beadsDir string) (string, error) {
	return lifecycleHint(beadsDir, config.GasCityStartCommandKey, "bd dolt start")
}

// RestartHint returns the command that restarts the Dolt server so a changed
// server setting takes effect.
func RestartHint(beadsDir string) (string, error) {
	return lifecycleHint(beadsDir, config.GasCityRestartCommandKey, "bd dolt stop && bd dolt start")
}

// StatusHint returns the command that reports the Dolt server state for the
// store at beadsDir.
func StatusHint(beadsDir string) (string, error) {
	return lifecycleHint(beadsDir, config.GasCityStatusCommandKey, "bd dolt status")
}

// CityRefusal returns the error for `bd dolt <verb>` when Gas City owns the
// Dolt server of the store at beadsDir, nil when bd owns the lifecycle, or the
// ownership resolution error.
func CityRefusal(beadsDir, verb string) error {
	origin, err := config.ReadGasCityEndpointOrigin(beadsDir)
	if err != nil {
		return err
	}
	if !origin.CityOwned() {
		return nil
	}
	start, err := config.GasCityCommand(config.GasCityStartCommandKey)
	if err != nil {
		return err
	}
	restart, err := config.GasCityCommand(config.GasCityRestartCommandKey)
	if err != nil {
		return err
	}
	status, err := config.GasCityCommand(config.GasCityStatusCommandKey)
	if err != nil {
		return err
	}
	return fmt.Errorf("Gas City owns this store's Dolt server (%s=%s); 'bd dolt %s' is refused here.\n"+
		"  To start: %s\n"+
		"  To restart: %s\n"+
		"  To check status: %s",
		config.GasCityEndpointOriginKey, origin, verb, start, restart, status)
}
