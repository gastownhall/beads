package agents

import (
	"strings"
	"testing"
)

func TestProfileConstants(t *testing.T) {
	if ProfileFull != "full" {
		t.Errorf("ProfileFull = %q, want %q", ProfileFull, "full")
	}
	if ProfileMinimal != "minimal" {
		t.Errorf("ProfileMinimal = %q, want %q", ProfileMinimal, "minimal")
	}
}

func TestRenderSectionFull(t *testing.T) {
	section := RenderSection(ProfileFull)
	if section == "" {
		t.Fatal("RenderSection(full) returned empty string")
	}

	// Must start with begin marker containing profile and hash metadata
	if !strings.HasPrefix(section, "<!-- BEGIN BEADS INTEGRATION") {
		t.Error("section should start with begin marker")
	}

	// Must contain profile metadata
	if !strings.Contains(section, "profile:full") {
		t.Error("begin marker should contain profile:full")
	}

	// Must contain hash metadata
	if !strings.Contains(section, "hash:") {
		t.Error("begin marker should contain hash:")
	}

	// Must end with end marker
	trimmed := strings.TrimSpace(section)
	if !strings.HasSuffix(trimmed, "<!-- END BEADS INTEGRATION -->") {
		t.Error("section should end with end marker")
	}

	// Full profile must contain command references
	for _, want := range []string{"bd create", "bd update", "bd close", "bd ready", "discovered-from"} {
		if !strings.Contains(section, want) {
			t.Errorf("full profile missing %q", want)
		}
	}
}

func TestRenderSectionMinimal(t *testing.T) {
	section := RenderSection(ProfileMinimal)
	if section == "" {
		t.Fatal("RenderSection(minimal) returned empty string")
	}

	// Must start with begin marker containing profile and hash metadata
	if !strings.HasPrefix(section, "<!-- BEGIN BEADS INTEGRATION") {
		t.Error("section should start with begin marker")
	}
	if !strings.Contains(section, "profile:minimal") {
		t.Error("begin marker should contain profile:minimal")
	}
	if !strings.Contains(section, "hash:") {
		t.Error("begin marker should contain hash:")
	}

	// Minimal should reference bd prime
	if !strings.Contains(section, "bd prime") {
		t.Error("minimal profile should reference bd prime")
	}

	// Minimal should NOT contain full command references
	if strings.Contains(section, "### Issue Types") {
		t.Error("minimal profile should not contain full issue types section")
	}
	if strings.Contains(section, "### Priorities") {
		t.Error("minimal profile should not contain full priorities section")
	}
}

func TestRenderSectionHashStability(t *testing.T) {
	// Same profile should produce same hash
	s1 := RenderSection(ProfileFull)
	s2 := RenderSection(ProfileFull)
	if s1 != s2 {
		t.Error("RenderSection should be deterministic (same input, same output)")
	}

	// Different profiles should produce different hashes
	full := RenderSection(ProfileFull)
	minimal := RenderSection(ProfileMinimal)
	meta1 := ParseMarker(strings.SplitN(full, "\n", 2)[0])
	meta2 := ParseMarker(strings.SplitN(minimal, "\n", 2)[0])
	if meta1.Hash == meta2.Hash {
		t.Error("different profiles should produce different hashes")
	}
}

func TestRenderSectionFullBackcompat(t *testing.T) {
	// Full profile should contain everything the legacy EmbeddedBeadsSection had
	section := RenderSection(ProfileFull)
	legacy := EmbeddedBeadsSection()

	// Check that key content pieces from legacy are present in full
	for _, want := range []string{
		"bd create",
		"bd update",
		"bd close",
		"bd ready",
		"discovered-from",
		"Landing the Plane",
	} {
		if !strings.Contains(legacy, want) {
			t.Fatalf("test precondition: legacy section missing %q", want)
		}
		if !strings.Contains(section, want) {
			t.Errorf("full profile missing legacy content %q", want)
		}
	}
}

func TestParseMarker(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   SectionMeta
		wantOK bool
	}{
		{
			name:   "new format with profile and hash",
			line:   "<!-- BEGIN BEADS INTEGRATION profile:full hash:a1b2c3d4 -->",
			want:   SectionMeta{Profile: ProfileFull, Hash: "a1b2c3d4"},
			wantOK: true,
		},
		{
			name:   "new format minimal profile",
			line:   "<!-- BEGIN BEADS INTEGRATION profile:minimal hash:deadbeef -->",
			want:   SectionMeta{Profile: ProfileMinimal, Hash: "deadbeef"},
			wantOK: true,
		},
		{
			name:   "legacy format (no metadata)",
			line:   "<!-- BEGIN BEADS INTEGRATION -->",
			want:   SectionMeta{Profile: "", Hash: ""},
			wantOK: true,
		},
		{
			name:   "not a marker",
			line:   "## Some heading",
			wantOK: false,
		},
		{
			name:   "end marker (not begin)",
			line:   "<!-- END BEADS INTEGRATION -->",
			wantOK: false,
		},
		{
			name:   "empty string",
			line:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMarker(tt.line)
			if tt.wantOK {
				if got == nil {
					t.Fatal("ParseMarker returned nil, expected non-nil")
				}
				if got.Profile != tt.want.Profile {
					t.Errorf("Profile = %q, want %q", got.Profile, tt.want.Profile)
				}
				if got.Hash != tt.want.Hash {
					t.Errorf("Hash = %q, want %q", got.Hash, tt.want.Hash)
				}
			} else {
				if got != nil {
					t.Errorf("ParseMarker returned %+v, expected nil", got)
				}
			}
		})
	}
}

func TestIsStaleFreshness(t *testing.T) {
	// Render a section and parse its marker — should not be stale
	section := RenderSection(ProfileFull)
	firstLine := strings.SplitN(section, "\n", 2)[0]
	meta := ParseMarker(firstLine)
	if meta == nil {
		t.Fatal("ParseMarker returned nil for rendered section")
	}

	currentHash := CurrentHash(ProfileFull)
	if meta.Hash != currentHash {
		t.Errorf("rendered hash %q != current hash %q (should be fresh)", meta.Hash, currentHash)
	}

	// Legacy marker (no hash) should be considered stale
	legacyMeta := ParseMarker("<!-- BEGIN BEADS INTEGRATION -->")
	if legacyMeta == nil {
		t.Fatal("ParseMarker returned nil for legacy marker")
	}
	if legacyMeta.Hash == currentHash {
		t.Error("legacy marker with empty hash should not match current hash")
	}
}
