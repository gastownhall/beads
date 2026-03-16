package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

func TestContainsBeadsMarkerLegacy(t *testing.T) {
	content := "# Header\n\n<!-- BEGIN BEADS INTEGRATION -->\nContent\n<!-- END BEADS INTEGRATION -->\n"
	if !containsBeadsMarker(content) {
		t.Error("should detect legacy marker")
	}
}

func TestContainsBeadsMarkerNew(t *testing.T) {
	content := "# Header\n\n<!-- BEGIN BEADS INTEGRATION profile:full hash:a1b2c3d4 -->\nContent\n<!-- END BEADS INTEGRATION -->\n"
	if !containsBeadsMarker(content) {
		t.Error("should detect new-format marker")
	}
}

func TestContainsBeadsMarkerAbsent(t *testing.T) {
	content := "# Header\n\nNo beads here\n"
	if containsBeadsMarker(content) {
		t.Error("should not detect marker when absent")
	}
}

func TestUpdateBeadsSectionLegacyToNew(t *testing.T) {
	// Legacy marker should be replaced with new-format marker
	legacy := `# Header

<!-- BEGIN BEADS INTEGRATION -->
Old content here
<!-- END BEADS INTEGRATION -->

# Footer`

	updated := updateBeadsSection(legacy)

	// Should have new-format marker with profile and hash
	if !strings.Contains(updated, "profile:full") {
		t.Error("updated section should use new-format marker with profile:full")
	}
	if !strings.Contains(updated, "hash:") {
		t.Error("updated section should use new-format marker with hash")
	}

	// Should preserve surrounding content
	if !strings.Contains(updated, "# Header") {
		t.Error("should preserve header")
	}
	if !strings.Contains(updated, "# Footer") {
		t.Error("should preserve footer")
	}

	// Old content should be replaced
	if strings.Contains(updated, "Old content here") {
		t.Error("old content should be replaced")
	}
}

func TestUpdateBeadsSectionNewFormatUpdate(t *testing.T) {
	// New-format marker should also be replaceable
	content := `# Header

<!-- BEGIN BEADS INTEGRATION profile:full hash:oldoldhash -->
Stale content
<!-- END BEADS INTEGRATION -->

# Footer`

	updated := updateBeadsSection(content)

	if strings.Contains(updated, "oldoldhash") {
		t.Error("old hash should be replaced")
	}
	if strings.Contains(updated, "Stale content") {
		t.Error("stale content should be replaced")
	}
	if !strings.Contains(updated, "# Header") || !strings.Contains(updated, "# Footer") {
		t.Error("surrounding content should be preserved")
	}
}

func TestRemoveBeadsSectionLegacy(t *testing.T) {
	content := "Header\n<!-- BEGIN BEADS INTEGRATION -->\nContent\n<!-- END BEADS INTEGRATION -->\nFooter"
	result := removeBeadsSection(content)
	if strings.Contains(result, "BEGIN BEADS") {
		t.Error("markers should be removed")
	}
	if !strings.Contains(result, "Header") || !strings.Contains(result, "Footer") {
		t.Error("surrounding content should be preserved")
	}
}

func TestRemoveBeadsSectionNewFormat(t *testing.T) {
	content := "Header\n<!-- BEGIN BEADS INTEGRATION profile:full hash:a1b2c3d4 -->\nContent\n<!-- END BEADS INTEGRATION -->\nFooter"
	result := removeBeadsSection(content)
	if strings.Contains(result, "BEGIN BEADS") {
		t.Error("markers should be removed")
	}
	if !strings.Contains(result, "Header") || !strings.Contains(result, "Footer") {
		t.Error("surrounding content should be preserved")
	}
}

func TestUpdateBeadsSectionWithProfile(t *testing.T) {
	// Test with explicit profile parameter
	content := `# Header

<!-- BEGIN BEADS INTEGRATION -->
Old content
<!-- END BEADS INTEGRATION -->

# Footer`

	updated := updateBeadsSectionWithProfile(content, agents.ProfileMinimal)
	if !strings.Contains(updated, "profile:minimal") {
		t.Error("should use minimal profile")
	}
	if !strings.Contains(updated, "# Header") || !strings.Contains(updated, "# Footer") {
		t.Error("should preserve surrounding content")
	}
}

func TestInstallAgentsWithProfileCreatesNew(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	integration := agentsIntegration{
		name:         "TestAgent",
		setupCommand: "bd setup testagent",
		profile:      agents.ProfileMinimal,
	}
	if err := installAgents(env, integration); err != nil {
		t.Fatalf("installAgents returned error: %v", err)
	}
	data, err := readFileBytes(env.agentsPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "profile:minimal") {
		t.Error("new file should use minimal profile from integration")
	}
}

func TestInstallAgentsDefaultsToFullProfile(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	integration := agentsIntegration{
		name:         "TestAgent",
		setupCommand: "bd setup testagent",
		// no profile set — should default to full
	}
	if err := installAgents(env, integration); err != nil {
		t.Fatalf("installAgents returned error: %v", err)
	}
	data, err := readFileBytes(env.agentsPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "profile:full") {
		t.Error("default profile should be full")
	}
}

// readFileBytes is a test helper to read file content
func readFileBytes(path string) ([]byte, error) {
	return readFileBytesImpl(path)
}

func TestExistingBeadsProfileLegacy(t *testing.T) {
	content := "# Header\n<!-- BEGIN BEADS INTEGRATION -->\nContent\n<!-- END BEADS INTEGRATION -->\n"
	got := existingBeadsProfile(content)
	if got != agents.ProfileFull {
		t.Errorf("legacy marker should return ProfileFull, got %q", got)
	}
}

func TestExistingBeadsProfileFull(t *testing.T) {
	content := "<!-- BEGIN BEADS INTEGRATION profile:full hash:abcd1234 -->\nContent\n<!-- END BEADS INTEGRATION -->\n"
	got := existingBeadsProfile(content)
	if got != agents.ProfileFull {
		t.Errorf("expected ProfileFull, got %q", got)
	}
}

func TestExistingBeadsProfileMinimal(t *testing.T) {
	content := "<!-- BEGIN BEADS INTEGRATION profile:minimal hash:deadbeef -->\nContent\n<!-- END BEADS INTEGRATION -->\n"
	got := existingBeadsProfile(content)
	if got != agents.ProfileMinimal {
		t.Errorf("expected ProfileMinimal, got %q", got)
	}
}

func TestExistingBeadsProfileNoMarker(t *testing.T) {
	content := "# Just a file\nNo markers\n"
	got := existingBeadsProfile(content)
	if got != agents.ProfileFull {
		t.Errorf("no marker should default to ProfileFull, got %q", got)
	}
}

func TestCheckAgentsDetectsStale(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	// Write a section with a bogus hash so it's stale
	content := "<!-- BEGIN BEADS INTEGRATION profile:full hash:00000000 -->\nOld content\n<!-- END BEADS INTEGRATION -->\n"
	if err := os.WriteFile(env.agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	integration := agentsIntegration{
		name:         "TestAgent",
		setupCommand: "bd setup testagent",
		profile:      agents.ProfileFull,
	}
	err := checkAgents(env, integration)
	if !errors.Is(err, errBeadsSectionStale) {
		t.Fatalf("expected errBeadsSectionStale, got %v", err)
	}
	if !strings.Contains(stdout.String(), "stale") {
		t.Error("expected stale message in stdout")
	}
}

func TestCheckAgentsCurrent(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	section := agents.RenderSection(agents.ProfileFull)
	if err := os.WriteFile(env.agentsPath, []byte(section), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	integration := agentsIntegration{
		name:         "TestAgent",
		setupCommand: "bd setup testagent",
		profile:      agents.ProfileFull,
	}
	if err := checkAgents(env, integration); err != nil {
		t.Fatalf("expected nil error for current section, got %v", err)
	}
	if !strings.Contains(stdout.String(), "current") {
		t.Error("expected (current) in output")
	}
}

func TestCheckAgentsMinimalAcceptsFullProfile(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	section := agents.RenderSection(agents.ProfileFull)
	if err := os.WriteFile(env.agentsPath, []byte(section), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	integration := agentsIntegration{
		name:         "ClaudeCode",
		setupCommand: "bd setup claude",
		profile:      agents.ProfileMinimal,
	}
	if err := checkAgents(env, integration); err != nil {
		t.Fatalf("expected full profile to be accepted for minimal integration, got %v", err)
	}
}

func TestCheckAgentsMissingUsesTargetFileName(t *testing.T) {
	stdout := &bytes.Buffer{}
	env := agentsEnv{
		agentsPath: filepath.Join(t.TempDir(), "CLAUDE.md"),
		stdout:     stdout,
		stderr:     &bytes.Buffer{},
	}
	integration := agentsIntegration{name: "ClaudeCode", setupCommand: "bd setup claude", profile: agents.ProfileMinimal}
	err := checkAgents(env, integration)
	if !errors.Is(err, errAgentsFileMissing) {
		t.Fatalf("expected errAgentsFileMissing, got %v", err)
	}
	if !strings.Contains(stdout.String(), "CLAUDE.md not found") {
		t.Fatalf("expected target filename in output, got: %s", stdout.String())
	}
}

func TestInstallAgentsPreservesFullProfile(t *testing.T) {
	// Simulate: file already has full profile, requesting minimal install
	env, stdout, _ := newFactoryTestEnv(t)
	fullSection := agents.RenderSection(agents.ProfileFull)
	if err := os.WriteFile(env.agentsPath, []byte(fullSection), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	integration := agentsIntegration{
		name:         "MinimalAgent",
		setupCommand: "bd setup minimalagent",
		profile:      agents.ProfileMinimal,
	}
	if err := installAgents(env, integration); err != nil {
		t.Fatalf("installAgents: %v", err)
	}
	data, err := readFileBytes(env.agentsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	// Should preserve full profile, not downgrade to minimal
	if !strings.Contains(content, "profile:full") {
		t.Error("should preserve full profile when minimal requested on file with full")
	}
	if !strings.Contains(stdout.String(), "preserving") {
		t.Error("expected informational message about preserving full profile")
	}
}

func TestInstallAgentsSymlinkSafety(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "AGENTS.md")
	linkPath := filepath.Join(dir, "CLAUDE.md")

	// Write full profile content to the real file
	fullSection := agents.RenderSection(agents.ProfileFull)
	if err := os.WriteFile(realFile, []byte(fullSection), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Create symlink
	if err := os.Symlink(realFile, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := agentsEnv{
		agentsPath: linkPath, // install targets the symlink
		stdout:     stdout,
		stderr:     stderr,
	}
	integration := agentsIntegration{
		name:         "ClaudeCode",
		setupCommand: "bd setup claude",
		profile:      agents.ProfileMinimal,
	}
	if err := installAgents(env, integration); err != nil {
		t.Fatalf("installAgents via symlink: %v", err)
	}

	// Read the real file — should still have full profile
	data, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatalf("read real file: %v", err)
	}
	if !strings.Contains(string(data), "profile:full") {
		t.Error("symlink target should preserve full profile")
	}
}

func TestLegacyToNewMigrationViaInstall(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	// Seed with legacy markers
	legacy := "# Header\n\n<!-- BEGIN BEADS INTEGRATION -->\nOld content\n<!-- END BEADS INTEGRATION -->\n\n# Footer"
	if err := os.WriteFile(env.agentsPath, []byte(legacy), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	integration := agentsIntegration{
		name:         "Factory.ai",
		setupCommand: "bd setup factory",
		profile:      agents.ProfileFull,
	}
	if err := installAgents(env, integration); err != nil {
		t.Fatalf("installAgents: %v", err)
	}
	data, err := readFileBytes(env.agentsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	// Should now have versioned markers
	if !strings.Contains(content, "profile:full") {
		t.Error("legacy markers should be upgraded to versioned format")
	}
	if !strings.Contains(content, "hash:") {
		t.Error("upgraded section should contain hash")
	}
	if strings.Contains(content, "Old content") {
		t.Error("old legacy content should be replaced")
	}
	if !strings.Contains(content, "# Header") || !strings.Contains(content, "# Footer") {
		t.Error("surrounding content should be preserved")
	}
}
