package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionChangesStructure(t *testing.T) {
	// Verify versionChanges is properly structured
	if len(versionChanges) == 0 {
		t.Fatal("versionChanges should not be empty")
	}

	for i, vc := range versionChanges {
		if vc.Version == "" {
			t.Errorf("versionChanges[%d] has empty Version", i)
		}
		if vc.Date == "" {
			t.Errorf("versionChanges[%d] has empty Date", i)
		}
		if len(vc.Changes) == 0 {
			t.Errorf("versionChanges[%d] has no changes", i)
		}

		// Verify version format (should be like "0.22.1")
		if len(vc.Version) < 5 {
			t.Errorf("versionChanges[%d] has invalid Version format: %s", i, vc.Version)
		}

		// Verify date format (should be like "2025-11-06")
		if len(vc.Date) != 10 {
			t.Errorf("versionChanges[%d] has invalid Date format: %s", i, vc.Date)
		}

		// Verify each change is non-empty
		for j, change := range vc.Changes {
			if change == "" {
				t.Errorf("versionChanges[%d].Changes[%d] is empty", i, j)
			}
		}
	}
}

func TestVersionChangesJSON(t *testing.T) {
	// Test that versionChanges can be marshaled to JSON
	data, err := json.Marshal(versionChanges)
	if err != nil {
		t.Fatalf("Failed to marshal versionChanges to JSON: %v", err)
	}

	// Test that it can be unmarshaled back
	var unmarshaled []VersionChange
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal versionChanges from JSON: %v", err)
	}

	// Verify structure is preserved
	if len(unmarshaled) != len(versionChanges) {
		t.Errorf("Unmarshaled length %d != original length %d", len(unmarshaled), len(versionChanges))
	}

	// Spot check first entry
	if len(unmarshaled) > 0 && len(versionChanges) > 0 {
		if unmarshaled[0].Version != versionChanges[0].Version {
			t.Errorf("Version mismatch: %s != %s", unmarshaled[0].Version, versionChanges[0].Version)
		}
		if len(unmarshaled[0].Changes) != len(versionChanges[0].Changes) {
			t.Errorf("Changes count mismatch: %d != %d", len(unmarshaled[0].Changes), len(versionChanges[0].Changes))
		}
	}
}

func TestVersionChangesCoverage(t *testing.T) {
	// Ensure we have at least 3 recent versions documented
	if len(versionChanges) < 3 {
		t.Errorf("Should document at least 3 recent versions, found %d", len(versionChanges))
	}

	// Ensure each version has at least one change documented
	for i, vc := range versionChanges {
		if len(vc.Changes) < 1 {
			t.Errorf("versionChanges[%d] (v%s) should have at least 1 change, found %d", i, vc.Version, len(vc.Changes))
		}
	}
}

// TestVersionChangesOnCompleteNotAdvertised: bd info --whats-new must not
// advertise on_complete as working runtime expansion. bd never ran it and now
// refuses formulas that set it (GH#3782).
func TestVersionChangesOnCompleteNotAdvertised(t *testing.T) {
	found := false
	for _, vc := range versionChanges {
		for _, change := range vc.Changes {
			if !strings.Contains(change, "on_complete") {
				continue
			}
			found = true
			if strings.Contains(change, "Runtime expansion") {
				t.Errorf("%s still advertises runtime expansion: %q", vc.Version, change)
			}
			if !strings.Contains(change, "refuses") {
				t.Errorf("%s on_complete entry does not say bd refuses it: %q", vc.Version, change)
			}
		}
	}
	if !found {
		t.Error("no versionChanges entry mentions on_complete; the 0.37.0 entry should be corrected in place, not removed")
	}
}
