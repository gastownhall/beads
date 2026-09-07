package config

import (
	"os"
	"path/filepath"
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

// TestCityOwnsDolt_LibraryModeReadsTheStoreConfig covers consumers that never
// call Initialize(): ownership is read straight from <beadsDir>/config.yaml,
// in the flat form the city stamps, and an empty beadsDir consults nothing.
func TestCityOwnsDolt_LibraryModeReadsTheStoreConfig(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "explicit endpoint is not city-owned", origin: "explicit", want: false},
		{name: "managed city root", origin: GasCityOriginManagedCity, want: true},
		{name: "city canonical rig", origin: GasCityOriginCityCanonical, want: true},
		{name: "inherited city rig", origin: GasCityOriginInheritedCity, want: true},
		{name: "unknown value is not city-owned", origin: "somebody_else", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeBeadsConfig(t, GasCityEndpointOriginKey+": "+tt.origin+"\n")
			if got := CityOwnsDolt(dir); got != tt.want {
				t.Fatalf("CityOwnsDolt(%s) with %s=%q = %v, want %v",
					dir, GasCityEndpointOriginKey, tt.origin, got, tt.want)
			}
			if got := GasCityEndpointOrigin(dir); got != tt.origin {
				t.Fatalf("GasCityEndpointOrigin(%s) = %q, want %q", dir, got, tt.origin)
			}
		})
	}

	t.Run("absent key keeps upstream behaviour", func(t *testing.T) {
		dir := writeBeadsConfig(t, "dolt.auto-start: false\n")
		if CityOwnsDolt(dir) {
			t.Fatalf("CityOwnsDolt(%s) = true without %s", dir, GasCityEndpointOriginKey)
		}
	})
	t.Run("nested mapping is accepted too", func(t *testing.T) {
		dir := writeBeadsConfig(t, "gc:\n  endpoint_origin: "+GasCityOriginCityCanonical+"\n")
		if !CityOwnsDolt(dir) {
			t.Fatalf("CityOwnsDolt(%s) = false for nested gc.endpoint_origin", dir)
		}
	})
	t.Run("empty beadsDir consults no file", func(t *testing.T) {
		if CityOwnsDolt("") {
			t.Fatal("CityOwnsDolt(\"\") = true with no viper and no store dir")
		}
	})
}

// TestCityOwnsDolt_CLIModeReadsTheMergedConfig covers every CLI path: after
// Initialize() the merged viper config carries the city's flat key and wins
// over any store directory passed alongside it.
func TestCityOwnsDolt_CLIModeReadsTheMergedConfig(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	projectDir := writeBeadsConfig(t, GasCityEndpointOriginKey+": "+GasCityOriginInheritedCity+"\n")
	t.Setenv("BEADS_DIR", projectDir)
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if got := GasCityEndpointOrigin(""); got != GasCityOriginInheritedCity {
		t.Fatalf("GasCityEndpointOrigin(\"\") after Initialize = %q, want %q", got, GasCityOriginInheritedCity)
	}
	if !CityOwnsDolt("") {
		t.Fatal("CityOwnsDolt(\"\") = false although the merged config carries inherited_city")
	}

	otherDir := writeBeadsConfig(t, GasCityEndpointOriginKey+": explicit\n")
	if !CityOwnsDolt(otherDir) {
		t.Fatal("merged CLI config must win over a store directory passed alongside it")
	}

	Set(GasCityEndpointOriginKey, "  explicit \n")
	if CityOwnsDolt("") {
		t.Fatal("an explicit override must switch ownership back to upstream behaviour")
	}
}
