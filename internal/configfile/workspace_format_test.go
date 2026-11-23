package configfile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckDiscoveredForOpenLeavesUnloadableMetadataToTheCaller pins what the
// pre-check every open runs before Load admits without reading further: a
// workspace with no metadata, and one whose metadata, in either file, does
// not load, which the caller's own load reports. It leaves the workspace as
// it found it: a legacy config.json is not migrated.
func TestCheckDiscoveredForOpenLeavesUnloadableMetadataToTheCaller(t *testing.T) {
	if err := CheckDiscoveredForOpen(t.TempDir()); err != nil {
		t.Fatalf("CheckDiscoveredForOpen(no metadata) = %v, want nil", err)
	}
	for _, name := range []string{ConfigFileName, LegacyConfigFileName} {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(`{"backend":`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := CheckDiscoveredForOpen(beadsDir); err != nil {
				t.Fatalf("CheckDiscoveredForOpen(unparseable %s) = %v, want nil", name, err)
			}
			entries, err := os.ReadDir(beadsDir)
			if err != nil || len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("pre-check changed the workspace: %v, %v", entries, err)
			}
		})
	}
}
