package fix

import (
	"testing"
)

// TestFixFunctions_RequireBeadsDir verifies all fix functions properly validate
// that a .beads directory exists before attempting fixes.
// This replaces 10+ individual "missing .beads directory" subtests.
func TestFixFunctions_RequireBeadsDir(t *testing.T) {
	funcs := []struct {
		name string
		fn   func(string) error
	}{
		{"GitHooks", GitHooks},
		{"DatabaseVersion", DatabaseVersion},
		{"SchemaCompatibility", SchemaCompatibility},
		{"ChildParentDependencies", func(dir string) error { return ChildParentDependencies(dir, false) }},
		{"OrphanedDependencies", func(dir string) error { return OrphanedDependencies(dir, false) }},
	}

	for _, tc := range funcs {
		t.Run(tc.name, func(t *testing.T) {
			// Use a temp directory without .beads
			dir := t.TempDir()
			err := tc.fn(dir)
			if err == nil {
				t.Errorf("%s should return error for missing .beads directory", tc.name)
			}
		})
	}
}

// The following tests created SQLite databases directly via openDB() to test
// fix functions. Since the fix functions use openAnyDB() which supports both
// SQLite and Dolt, these tests will be re-enabled with Dolt fixtures when the
// fix functions are fully converted to Dolt (bd-o0u.5).

func TestChildParentDependencies_NoBadDeps(t *testing.T) {
	t.Skip("SQLite fixture test; will be converted with fix functions in bd-o0u.5")
}

func TestChildParentDependencies_FixesBadDeps(t *testing.T) {
	t.Skip("SQLite fixture test; will be converted with fix functions in bd-o0u.5")
}

func TestChildParentDependencies_PreservesParentChildType(t *testing.T) {
	t.Skip("SQLite fixture test; will be converted with fix functions in bd-o0u.5")
}
