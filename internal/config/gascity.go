package config

import (
	"errors"
	"fmt"
	"strings"
)

// Gas City writes its endpoint ownership into the store's .beads/config.yaml
// as the flat key `gc.endpoint_origin` (see the gascity contract). The city
// runs the only Dolt server for every store it owns and publishes its port at
// runtime; bd must never start, stop, re-point or kill that server itself.

// GasCityEndpointOriginKey is the config.yaml key the city stamps.
const GasCityEndpointOriginKey = "gc.endpoint_origin"

// GasCityEndpointOrigin is the validated value of gc.endpoint_origin.
type GasCityEndpointOrigin string

const (
	// GasCityOriginManagedCity marks the city root store: the city allocates
	// and runs the Dolt server.
	GasCityOriginManagedCity GasCityEndpointOrigin = "managed_city"
	// GasCityOriginCityCanonical marks a rig store that resolves to the city's
	// canonical endpoint.
	GasCityOriginCityCanonical GasCityEndpointOrigin = "city_canonical"
	// GasCityOriginInheritedCity marks a rig store that inherits the city's
	// endpoint through the mirror the city maintains.
	GasCityOriginInheritedCity GasCityEndpointOrigin = "inherited_city"
	// GasCityOriginExplicit marks a store the city registered with an
	// explicit endpoint it does not run; bd keeps its upstream lifecycle.
	GasCityOriginExplicit GasCityEndpointOrigin = "explicit"
)

// gasCityEndpointOrigins is the closed set of values the city stamps.
var gasCityEndpointOrigins = []GasCityEndpointOrigin{
	GasCityOriginManagedCity,
	GasCityOriginCityCanonical,
	GasCityOriginInheritedCity,
	GasCityOriginExplicit,
}

// CityOwned reports whether the origin means the city runs the Dolt server.
func (o GasCityEndpointOrigin) CityOwned() bool {
	switch o {
	case GasCityOriginManagedCity, GasCityOriginCityCanonical, GasCityOriginInheritedCity:
		return true
	default:
		return false
	}
}

// ReadGasCityEndpointOrigin returns the gc.endpoint_origin the city stamped in
// <beadsDir>/config.yaml, or "" when the store carries no stamp (no city
// manages it). The store's own file is the only source: the merged viper
// config is not consulted, so the CLI and library consumers decide ownership
// from the same bytes.
//
// An empty beadsDir, an unreadable or malformed config.yaml, and a value
// outside the closed set are errors, never "not city-owned".
func ReadGasCityEndpointOrigin(beadsDir string) (GasCityEndpointOrigin, error) {
	if beadsDir == "" {
		return "", errors.New("resolving " + GasCityEndpointOriginKey + ": no .beads directory given")
	}
	raw, found, err := lookupStringFromDir(beadsDir, GasCityEndpointOriginKey)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", GasCityEndpointOriginKey, err)
	}
	if !found {
		return "", nil
	}
	origin := GasCityEndpointOrigin(raw)
	for _, known := range gasCityEndpointOrigins {
		if origin == known {
			return origin, nil
		}
	}
	valid := make([]string, len(gasCityEndpointOrigins))
	for i, known := range gasCityEndpointOrigins {
		valid[i] = string(known)
	}
	return "", fmt.Errorf("invalid %s %q in %s/config.yaml (valid: %s)",
		GasCityEndpointOriginKey, raw, beadsDir, strings.Join(valid, ", "))
}

// CityOwnsDolt reports whether Gas City owns the Dolt server behind the store
// at beadsDir (see ReadGasCityEndpointOrigin). When true, every bd Dolt
// lifecycle operation (start, stop, set, killall, auto-start) is refused and
// the city's own commands (GasCityCommand) are the only lifecycle surface.
func CityOwnsDolt(beadsDir string) (bool, error) {
	origin, err := ReadGasCityEndpointOrigin(beadsDir)
	if err != nil {
		return false, err
	}
	return origin.CityOwned(), nil
}

// Config keys naming the city's lifecycle commands, printed in hints when the
// city owns a store's Dolt server.
const (
	GasCityStartCommandKey   = "gc.start-command"
	GasCityRestartCommandKey = "gc.restart-command"
	GasCityStatusCommandKey  = "gc.status-command"
)

// gasCityCommandDefaults declares the default of every city lifecycle command
// key once; Initialize registers them with viper and GasCityCommand serves
// them to library consumers that never call Initialize.
var gasCityCommandDefaults = map[string]string{
	GasCityStartCommandKey:   "gc start",
	GasCityRestartCommandKey: "gc stop && gc start",
	GasCityStatusCommandKey:  "gc doctor",
}

// GasCityCommand returns the configured city lifecycle command for key, one of
// the GasCity*CommandKey constants. An absent key yields the declared default;
// an unknown key or a value explicitly configured empty is an error.
func GasCityCommand(key string) (string, error) {
	def, ok := gasCityCommandDefaults[key]
	if !ok {
		return "", fmt.Errorf("unknown Gas City command key %q", key)
	}
	if v == nil || !v.IsSet(key) {
		return def, nil
	}
	command := v.GetString(key)
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("config key %s is empty; it must name the Gas City command", key)
	}
	return command, nil
}
