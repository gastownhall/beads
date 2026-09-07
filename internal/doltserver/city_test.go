package doltserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// storeWithOrigin provisions a .beads directory whose config.yaml carries the
// city's flat gc.endpoint_origin stamp (or none when origin is ""), with no
// merged CLI config and no auto-start env override, so only the store's own
// file decides ownership — the library-consumer contract.
func storeWithOrigin(t *testing.T, origin string) string {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	dir := t.TempDir()
	body := ""
	if origin != "" {
		body = config.GasCityEndpointOriginKey + ": " + origin + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return dir
}

func TestCityOwnedStoreDisablesAutoStartAndRoutesHintsToGc(t *testing.T) {
	for _, origin := range []string{
		config.GasCityOriginManagedCity,
		config.GasCityOriginCityCanonical,
		config.GasCityOriginInheritedCity,
	} {
		t.Run(origin, func(t *testing.T) {
			dir := storeWithOrigin(t, origin)

			if !IsAutoStartDisabled(dir) {
				t.Fatalf("IsAutoStartDisabled(%s) = false with %s=%q; the city owns the server",
					dir, config.GasCityEndpointOriginKey, origin)
			}
			if got := StartHint(dir); got != "gc start" {
				t.Fatalf("StartHint() = %q, want gc start", got)
			}
			if got := RestartHint(dir); got != "gc stop && gc start" {
				t.Fatalf("RestartHint() = %q, want gc stop && gc start", got)
			}
			if got := StatusHint(dir); got != "gc doctor" {
				t.Fatalf("StatusHint() = %q, want gc doctor", got)
			}
		})
	}
}

func TestUnmanagedStoreKeepsUpstreamAutoStartAndHints(t *testing.T) {
	for _, origin := range []string{"", "explicit"} {
		t.Run("origin="+origin, func(t *testing.T) {
			dir := storeWithOrigin(t, origin)

			if IsAutoStartDisabled(dir) {
				t.Fatalf("IsAutoStartDisabled(%s) = true with %s=%q and no auto-start override",
					dir, config.GasCityEndpointOriginKey, origin)
			}
			if got := StartHint(dir); got != "bd dolt start" {
				t.Fatalf("StartHint() = %q, want bd dolt start", got)
			}
			if got := RestartHint(dir); got != "bd dolt stop && bd dolt start" {
				t.Fatalf("RestartHint() = %q, want bd dolt stop && bd dolt start", got)
			}
			if got := StatusHint(dir); got != "bd dolt status" {
				t.Fatalf("StatusHint() = %q, want bd dolt status", got)
			}
		})
	}
}

func TestCityRefusalNamesOriginAndRefusedVerb(t *testing.T) {
	dir := storeWithOrigin(t, config.GasCityOriginInheritedCity)

	msg := CityRefusal(dir, "killall")
	for _, want := range []string{
		config.GasCityEndpointOriginKey + "=" + config.GasCityOriginInheritedCity,
		"`bd dolt killall` is refused",
		"`gc start`",
		"`gc stop`",
		"`gc doctor`",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("CityRefusal(killall) = %q, missing %q", msg, want)
		}
	}
}

// TestEnsureRunningRefusesToSpawnForCityOwnedStore proves the effect, not just
// the predicate: with no server listening, EnsureRunningDetailed on a
// city-owned store returns the city refusal instead of launching dolt.
func TestEnsureRunningRefusesToSpawnForCityOwnedStore(t *testing.T) {
	dir := storeWithOrigin(t, config.GasCityOriginManagedCity)

	port, startedByUs, err := EnsureRunningDetailed(dir)
	if err == nil {
		t.Fatalf("EnsureRunningDetailed(%s) started a server (port %d, startedByUs=%v) for a city-owned store", dir, port, startedByUs)
	}
	if startedByUs {
		t.Fatalf("EnsureRunningDetailed(%s) reported startedByUs=true while refusing", dir)
	}
	for _, want := range []string{"Gas City owns this store's server", "bd does not start it", "gc start", "gc doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q is missing %q", err.Error(), want)
		}
	}
}
