package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// storeWithConfig provisions a .beads directory whose config.yaml carries
// body, with no merged CLI config and no auto-start env override, so only the
// store's own file decides ownership.
func storeWithConfig(t *testing.T, body string) string {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return dir
}

// storeWithOrigin provisions a store stamped with origin, or unstamped when
// origin is "".
func storeWithOrigin(t *testing.T, origin config.GasCityEndpointOrigin) string {
	t.Helper()
	if origin == "" {
		return storeWithConfig(t, "")
	}
	return storeWithConfig(t, config.GasCityEndpointOriginKey+": "+string(origin)+"\n")
}

func mustHint(t *testing.T, name string, hint func(string) (string, error), dir string) string {
	t.Helper()
	got, err := hint(dir)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, dir, err)
	}
	return got
}

func TestCityOwnedStoreDisablesAutoStartAndRoutesHintsToGc(t *testing.T) {
	for _, origin := range []config.GasCityEndpointOrigin{
		config.GasCityOriginManagedCity,
		config.GasCityOriginCityCanonical,
		config.GasCityOriginInheritedCity,
	} {
		t.Run(string(origin), func(t *testing.T) {
			dir := storeWithOrigin(t, origin)

			disabled, err := IsAutoStartDisabled(dir)
			if err != nil || !disabled {
				t.Fatalf("IsAutoStartDisabled(%s) = (%v, %v) with %s=%q; the city owns the server",
					dir, disabled, err, config.GasCityEndpointOriginKey, origin)
			}
			if got := mustHint(t, "StartHint", StartHint, dir); got != "gc start" {
				t.Fatalf("StartHint() = %q, want gc start", got)
			}
			if got := mustHint(t, "RestartHint", RestartHint, dir); got != "gc stop && gc start" {
				t.Fatalf("RestartHint() = %q, want gc stop && gc start", got)
			}
			if got := mustHint(t, "StatusHint", StatusHint, dir); got != "gc doctor" {
				t.Fatalf("StatusHint() = %q, want gc doctor", got)
			}
		})
	}
}

func TestCityOwnedHintsFollowConfiguredCommands(t *testing.T) {
	dir := storeWithOrigin(t, config.GasCityOriginManagedCity)
	config.ResetForTesting()
	t.Setenv("BEADS_DIR", storeWithConfig(t, config.GasCityStartCommandKey+": gc up\n"))
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if got := mustHint(t, "StartHint", StartHint, dir); got != "gc up" {
		t.Fatalf("StartHint() = %q, want the configured gc up", got)
	}
}

func TestUnmanagedStoreKeepsUpstreamAutoStartAndHints(t *testing.T) {
	for _, origin := range []config.GasCityEndpointOrigin{"", config.GasCityOriginExplicit} {
		t.Run("origin="+string(origin), func(t *testing.T) {
			dir := storeWithOrigin(t, origin)

			disabled, err := IsAutoStartDisabled(dir)
			if err != nil || disabled {
				t.Fatalf("IsAutoStartDisabled(%s) = (%v, %v) with %s=%q and no auto-start override",
					dir, disabled, err, config.GasCityEndpointOriginKey, origin)
			}
			if got := mustHint(t, "StartHint", StartHint, dir); got != "bd dolt start" {
				t.Fatalf("StartHint() = %q, want bd dolt start", got)
			}
			if got := mustHint(t, "RestartHint", RestartHint, dir); got != "bd dolt stop && bd dolt start" {
				t.Fatalf("RestartHint() = %q, want bd dolt stop && bd dolt start", got)
			}
			if got := mustHint(t, "StatusHint", StatusHint, dir); got != "bd dolt status" {
				t.Fatalf("StatusHint() = %q, want bd dolt status", got)
			}
			if err := CityRefusal(dir, "start"); err != nil {
				t.Fatalf("CityRefusal(start) = %v for a store the city does not own", err)
			}
		})
	}
}

// TestInvalidOwnershipStampIsAnError proves an unknown origin and a malformed
// config.yaml surface as errors on every lifecycle decision instead of
// silently selecting upstream behaviour.
func TestInvalidOwnershipStampIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"unknown origin": config.GasCityEndpointOriginKey + ": somebody_else\n",
		"malformed yaml": "gc: [\nbroken\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := storeWithConfig(t, body)
			if _, err := IsAutoStartDisabled(dir); err == nil {
				t.Fatal("IsAutoStartDisabled returned no error")
			}
			for hintName, hint := range map[string]func(string) (string, error){
				"StartHint": StartHint, "RestartHint": RestartHint, "StatusHint": StatusHint,
			} {
				if got, err := hint(dir); err == nil {
					t.Fatalf("%s = %q with no error", hintName, got)
				}
			}
			if err := CityRefusal(dir, "stop"); err == nil || strings.Contains(err.Error(), "is refused") {
				t.Fatalf("CityRefusal = %v, want the ownership resolution error", err)
			}
			if _, _, err := EnsureRunningDetailed(dir); err == nil || strings.Contains(err.Error(), "Gas City owns") {
				t.Fatalf("EnsureRunningDetailed = %v, want the ownership resolution error", err)
			}
			if err := os.WriteFile(pidPath(dir), []byte("111"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := killStaleServersForDir(dir, []int{111, 222},
				func(int, string) bool { return true },
				func(pid int) error { t.Fatalf("killed %d despite an invalid ownership stamp", pid); return nil },
			); err == nil {
				t.Fatal("killStaleServersForDir returned no error")
			}
		})
	}
}

func TestCityRefusalNamesOriginAndRefusedVerb(t *testing.T) {
	dir := storeWithOrigin(t, config.GasCityOriginInheritedCity)

	err := CityRefusal(dir, "killall")
	if err == nil {
		t.Fatal("CityRefusal(killall) = nil for a city-owned store")
	}
	for _, want := range []string{
		config.GasCityEndpointOriginKey + "=" + string(config.GasCityOriginInheritedCity),
		"'bd dolt killall' is refused",
		"To start: gc start",
		"To restart: gc stop && gc start",
		"To check status: gc doctor",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("CityRefusal(killall) = %q, missing %q", err, want)
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
	for _, want := range []string{"Gas City owns this store's server", "bd does not start it", "To start: gc start", "To check status: gc doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q is missing %q", err.Error(), want)
		}
	}
}

// TestKillStaleServersLeavesCityOwnedStoreAlone proves bd never kills a
// process in a store the city owns, even with a PID file and an orphan.
func TestKillStaleServersLeavesCityOwnedStoreAlone(t *testing.T) {
	dir := storeWithOrigin(t, config.GasCityOriginCityCanonical)
	canonicalPID, orphanPID := 111, 222
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(canonicalPID)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := killStaleServersForDir(dir, []int{canonicalPID, orphanPID},
		func(int, string) bool { return true },
		func(pid int) error { t.Fatalf("killed %d in a city-owned store", pid); return nil },
	)
	if err != nil || len(got) != 0 {
		t.Fatalf("killStaleServersForDir = (%v, %v), want (nil, nil)", got, err)
	}
}
