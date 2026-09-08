package agents

import (
	"strings"
	"testing"
)

func TestEmbeddedDefault(t *testing.T) {
	content := EmbeddedDefault()

	if content == "" {
		t.Fatal("EmbeddedDefault() returned empty string")
	}

	required := []string{
		"# Agent Instructions",
		"## Quick Reference",
		"bd prime",
		"BEGIN BEADS INTEGRATION",
		"END BEADS INTEGRATION",
		"## Session Completion",
		"git push",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("EmbeddedDefault() missing %q", want)
		}
	}
}

// TestEmbeddedDefaultArchitectureSummary guards GH#3683: agents reading the
// generated AGENTS.md need an architecture statement at the top so they don't
// build wrong mental models (treating JSONL as source of truth, manually
// running bd import, etc.) and have to discover the four deeper architecture
// docs the hard way. The summary must appear before the Quick Reference and
// link to the canonical SYNC_CONCEPTS.md entry-point.
func TestEmbeddedDefaultArchitectureSummary(t *testing.T) {
	content := EmbeddedDefault()

	required := []string{
		"Architecture in one line",
		"refs/dolt/data",
		"passive export",
		"SYNC_CONCEPTS.md",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("EmbeddedDefault() missing architecture-summary fragment %q", want)
		}
	}

	archIdx := strings.Index(content, "Architecture in one line")
	quickRefIdx := strings.Index(content, "## Quick Reference")
	if archIdx == -1 || quickRefIdx == -1 {
		t.Fatal("missing required anchors")
	}
	if archIdx > quickRefIdx {
		t.Error("architecture summary should appear before Quick Reference (so agents see it first)")
	}
}

func TestEmbeddedBeadsSection(t *testing.T) {
	section := EmbeddedBeadsSection()

	if section == "" {
		t.Fatal("EmbeddedBeadsSection() returned empty string")
	}

	if !strings.HasPrefix(section, "<!-- BEGIN BEADS INTEGRATION -->") {
		t.Error("beads section should start with begin marker")
	}

	trimmed := strings.TrimSpace(section)
	if !strings.HasSuffix(trimmed, "<!-- END BEADS INTEGRATION -->") {
		t.Error("beads section should end with end marker")
	}

	required := []string{
		"bd create",
		"bd update",
		"bd close",
		"bd ready",
		"discovered-from",
	}
	for _, want := range required {
		if !strings.Contains(section, want) {
			t.Errorf("EmbeddedBeadsSection() missing %q", want)
		}
	}
}

func TestBeadsSectionContainsLanding(t *testing.T) {
	section := EmbeddedBeadsSection()
	if !strings.Contains(section, "Session Completion") {
		t.Error("beads section should contain session completion content within markers")
	}
}

func TestDefaultContainsBothSections(t *testing.T) {
	content := EmbeddedDefault()

	beadsIdx := strings.Index(content, "BEGIN BEADS INTEGRATION")
	completionIdx := strings.Index(content, "Session Completion")

	if beadsIdx == -1 {
		t.Fatal("missing beads integration section")
	}
	if completionIdx == -1 {
		t.Fatal("missing session completion section")
	}
	if beadsIdx > completionIdx {
		t.Error("beads section should come before session completion section")
	}
}

func TestEmbeddedDefaultManagedMarkerIsCurrent(t *testing.T) {
	content := EmbeddedDefault()

	idx := strings.Index(content, "<!-- BEGIN BEADS INTEGRATION")
	if idx == -1 {
		t.Fatal("missing managed section marker")
	}
	line := content[idx:]
	if nl := strings.Index(line, "\n"); nl != -1 {
		line = line[:nl]
	}

	meta := ParseMarker(line)
	if meta == nil {
		t.Fatalf("failed to parse managed marker %q", line)
	}
	if meta.Version != MarkerVersion {
		t.Errorf("marker version = %d, want %d", meta.Version, MarkerVersion)
	}
	if meta.Profile != ProfileFull {
		t.Errorf("marker profile = %q, want %q", meta.Profile, ProfileFull)
	}
	if meta.Hash != CurrentHash(ProfileFull) {
		t.Errorf("marker hash = %q, want %q", meta.Hash, CurrentHash(ProfileFull))
	}
}
