package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// TestRunDiagnosticsMacOSLegacyUserConfigOnlyOnDarwin pins the GH#8113
// registration: on darwin the check runs before the no-.beads early return,
// after Installation, in Data & Config; elsewhere it is absent, so Linux and
// Windows doctor output is unchanged.
func TestRunDiagnosticsMacOSLegacyUserConfigOnlyOnDarwin(t *testing.T) {
	find := func(result doctorResult) int {
		for i, c := range result.Checks {
			if c.Name == doctor.MacOSLegacyUserConfigCheckName {
				return i
			}
		}
		return -1
	}
	for _, tc := range []struct {
		goos string
		want bool
	}{
		{goos: "darwin", want: true},
		{goos: "ios", want: true},
		{goos: "linux"},
		{goos: "windows"},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			legacy := filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte("actor: alice\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := macOSLegacyUserConfigGOOS
			macOSLegacyUserConfigGOOS = tc.goos
			t.Cleanup(func() { macOSLegacyUserConfigGOOS = old })

			result := runDiagnostics(t.TempDir()) // no .beads: early return path
			idx := find(result)
			if !tc.want {
				if idx >= 0 {
					t.Fatalf("check present on %s: %+v", tc.goos, result.Checks[idx])
				}
				return
			}
			if idx <= 0 {
				t.Fatalf("check index = %d, want > 0 (after Installation); checks %+v", idx, result.Checks)
			}
			if result.Checks[0].Name != "Installation" {
				t.Errorf("Checks[0] = %q, want Installation", result.Checks[0].Name)
			}
			got := result.Checks[idx]
			if got.Category != doctor.CategoryData || got.Status != statusWarning {
				t.Errorf("check = %+v, want a Data & Config warning", got)
			}
		})
	}
}
