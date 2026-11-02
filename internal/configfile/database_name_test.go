package configfile

import (
	"runtime"
	"strings"
	"testing"
)

func TestMetadataDatabaseNames(t *testing.T) {
	invalid := []string{"", ".", "..", "/absolute", `a\b`, "a/b", "bad\x00name", "../../victim/name-with-hyphen", "..\\victim"}
	if runtime.GOOS == "windows" {
		invalid = append(invalid, `C:\abs`, "C:rel", `\\host\share`)
	}
	for _, name := range invalid {
		err := ValidateDatabaseName(name)
		if err == nil || !strings.Contains(err.Error(), "dolt_database") {
			t.Errorf("ValidateDatabaseName(%q) = %v, want a dolt_database error", name, err)
		}
	}
	// Legacy names with dots and hyphens predate sanitization and stay valid.
	for _, name := range []string{"beads", "my-project", "my.project", "2026-notes", "2026.notes", "...", "a..b", strings.Repeat("a", 65)} {
		if err := ValidateDatabaseName(name); err != nil {
			t.Errorf("ValidateDatabaseName(%q) = %v, want nil", name, err)
		}
	}
}
