package config

import "strings"

// Gas City writes its endpoint ownership into the rig's .beads/config.yaml as
// the flat key `gc.endpoint_origin` (see gascity contract/files.go). The city
// runs the only Dolt server for every store it owns and publishes its port at
// runtime; bd must never start, stop, re-point or kill that server itself.
const (
	// GasCityEndpointOriginKey is the config.yaml key the city stamps.
	GasCityEndpointOriginKey = "gc.endpoint_origin"

	// GasCityOriginManagedCity marks the city root store: the city allocates
	// and runs the Dolt server.
	GasCityOriginManagedCity = "managed_city"
	// GasCityOriginCityCanonical marks a rig store that resolves to the city's
	// canonical endpoint.
	GasCityOriginCityCanonical = "city_canonical"
	// GasCityOriginInheritedCity marks a rig store that inherits the city's
	// endpoint through the mirror the city maintains.
	GasCityOriginInheritedCity = "inherited_city"
)

// GasCityEndpointOrigin returns the trimmed `gc.endpoint_origin` value the
// city stamped for the active store, or "" when no city manages it.
//
// The merged viper config is consulted first (populated by Initialize() on
// every CLI path). When it carries no value and beadsDir is non-empty, the
// store's own <beadsDir>/config.yaml is read directly, so library consumers
// that never call Initialize() see the same ownership the CLI sees. An empty
// beadsDir means "no file to consult".
func GasCityEndpointOrigin(beadsDir string) string {
	origin := strings.TrimSpace(GetString(GasCityEndpointOriginKey))
	if origin == "" && beadsDir != "" {
		origin = strings.TrimSpace(GetStringFromDir(beadsDir, GasCityEndpointOriginKey))
	}
	return origin
}

// CityOwnsDolt reports whether Gas City owns the Dolt server behind the
// store at beadsDir (see GasCityEndpointOrigin for the lookup order). When
// true, every bd Dolt lifecycle operation (start, stop, set host/port,
// killall, auto-start) is refused: `gc start`, `gc stop` and `gc doctor` are
// the only lifecycle surface. An explicit or absent origin keeps upstream
// behavior.
func CityOwnsDolt(beadsDir string) bool {
	switch GasCityEndpointOrigin(beadsDir) {
	case GasCityOriginManagedCity, GasCityOriginCityCanonical, GasCityOriginInheritedCity:
		return true
	default:
		return false
	}
}
