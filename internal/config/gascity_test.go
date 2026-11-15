package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBeadsConfig writes <dir>/config.yaml with the given body and returns dir.
func writeBeadsConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return dir
}

// TestCityOwnsDolt_ReadsTheStoreConfig covers the closed origin set, read
// straight from <beadsDir>/config.yaml in the flat form the city stamps.
func TestCityOwnsDolt_ReadsTheStoreConfig(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	tests := []struct {
		origin GasCityEndpointOrigin
		want   bool
	}{
		{origin: GasCityOriginExplicit, want: false},
		{origin: GasCityOriginManagedCity, want: true},
		{origin: GasCityOriginCityCanonical, want: true},
		{origin: GasCityOriginInheritedCity, want: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.origin), func(t *testing.T) {
			dir := writeBeadsConfig(t, GasCityEndpointOriginKey+": "+string(tt.origin)+"\n")
			got, err := CityOwnsDolt(dir)
			if err != nil {
				t.Fatalf("CityOwnsDolt(%s): %v", dir, err)
			}
			if got != tt.want {
				t.Fatalf("CityOwnsDolt(%s) with %s=%q = %v, want %v",
					dir, GasCityEndpointOriginKey, tt.origin, got, tt.want)
			}
			origin, err := ReadGasCityEndpointOrigin(dir)
			if err != nil || origin != tt.origin {
				t.Fatalf("ReadGasCityEndpointOrigin(%s) = (%q, %v), want (%q, nil)", dir, origin, err, tt.origin)
			}
		})
	}

	t.Run("absent key keeps upstream behaviour", func(t *testing.T) {
		dir := writeBeadsConfig(t, "dolt.auto-start: false\n")
		owned, err := CityOwnsDolt(dir)
		if err != nil || owned {
			t.Fatalf("CityOwnsDolt(%s) = (%v, %v) without %s, want (false, nil)", dir, owned, err, GasCityEndpointOriginKey)
		}
	})
	t.Run("absent config.yaml keeps upstream behaviour", func(t *testing.T) {
		owned, err := CityOwnsDolt(t.TempDir())
		if err != nil || owned {
			t.Fatalf("CityOwnsDolt(no config.yaml) = (%v, %v), want (false, nil)", owned, err)
		}
	})
	t.Run("nested mapping is accepted too", func(t *testing.T) {
		dir := writeBeadsConfig(t, "gc:\n  endpoint_origin: "+string(GasCityOriginCityCanonical)+"\n")
		owned, err := CityOwnsDolt(dir)
		if err != nil || !owned {
			t.Fatalf("CityOwnsDolt(%s) = (%v, %v) for nested gc.endpoint_origin, want (true, nil)", dir, owned, err)
		}
	})
}

// TestCityOwnsDolt_RejectsInvalidInput proves that an unknown origin, an
// empty stamp, malformed YAML and a missing store directory are errors rather
// than a silent "not city-owned".
func TestCityOwnsDolt_RejectsInvalidInput(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	tests := []struct {
		name    string
		dir     func(t *testing.T) string
		wantErr string
	}{
		{"unknown origin", func(t *testing.T) string {
			return writeBeadsConfig(t, GasCityEndpointOriginKey+": somebody_else\n")
		}, `invalid gc.endpoint_origin "somebody_else"`},
		{"empty origin", func(t *testing.T) string {
			return writeBeadsConfig(t, GasCityEndpointOriginKey+": \"\"\n")
		}, `invalid gc.endpoint_origin ""`},
		{"padded origin is not normalized", func(t *testing.T) string {
			return writeBeadsConfig(t, GasCityEndpointOriginKey+": \" managed_city \"\n")
		}, `invalid gc.endpoint_origin " managed_city "`},
		{"malformed yaml", func(t *testing.T) string {
			return writeBeadsConfig(t, "gc: [\nbroken\n")
		}, "parsing"},
		{"no store directory", func(t *testing.T) string { return "" }, "no .beads directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owned, err := CityOwnsDolt(tt.dir(t))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CityOwnsDolt = (%v, %v), want an error containing %q", owned, err, tt.wantErr)
			}
		})
	}
}

// TestCityOwnsDolt_StoreIsTheSingleSource proves the merged CLI config never
// decides ownership: a stamp only viper carries is ignored, and a store's own
// stamp wins over a contrary merged value.
func TestCityOwnsDolt_StoreIsTheSingleSource(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	projectDir := writeBeadsConfig(t, GasCityEndpointOriginKey+": "+string(GasCityOriginInheritedCity)+"\n")
	t.Setenv("BEADS_DIR", projectDir)
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	explicitDir := writeBeadsConfig(t, GasCityEndpointOriginKey+": "+string(GasCityOriginExplicit)+"\n")
	owned, err := CityOwnsDolt(explicitDir)
	if err != nil || owned {
		t.Fatalf("CityOwnsDolt(explicit store) = (%v, %v) while viper carries inherited_city; the store must decide", owned, err)
	}

	Set(GasCityEndpointOriginKey, string(GasCityOriginExplicit))
	owned, err = CityOwnsDolt(projectDir)
	if err != nil || !owned {
		t.Fatalf("CityOwnsDolt(inherited store) = (%v, %v) after a viper override to explicit; the store must decide", owned, err)
	}
}

// TestGasCityCommand covers the declared defaults (with and without
// Initialize), a configured override, an empty override and an unknown key.
func TestGasCityCommand(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	ResetForTesting()
	t.Cleanup(ResetForTesting)
	for key, want := range map[string]string{
		GasCityStartCommandKey:   "gc start",
		GasCityRestartCommandKey: "gc stop && gc start",
		GasCityStatusCommandKey:  "gc doctor",
	} {
		got, err := GasCityCommand(key)
		if err != nil || got != want {
			t.Fatalf("GasCityCommand(%s) without Initialize = (%q, %v), want (%q, nil)", key, got, err, want)
		}
	}

	t.Setenv("BEADS_DIR", writeBeadsConfig(t, GasCityStartCommandKey+": gc up\n"))
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got, err := GasCityCommand(GasCityStartCommandKey); err != nil || got != "gc up" {
		t.Fatalf("GasCityCommand(start) with config override = (%q, %v), want (\"gc up\", nil)", got, err)
	}
	if got, err := GasCityCommand(GasCityStatusCommandKey); err != nil || got != "gc doctor" {
		t.Fatalf("GasCityCommand(status) after Initialize = (%q, %v), want the declared default", got, err)
	}

	Set(GasCityRestartCommandKey, " ")
	if _, err := GasCityCommand(GasCityRestartCommandKey); err == nil {
		t.Fatal("GasCityCommand(restart) configured blank returned no error")
	}
	if _, err := GasCityCommand("gc.unknown-command"); err == nil {
		t.Fatal("GasCityCommand(unknown key) returned no error")
	}
}
