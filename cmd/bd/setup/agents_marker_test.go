package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

// TestAgentsSectionRemoveReaddIsStable pins what --remove and a later setup
// leave behind: removing the section leaves no blank-line hole, setup appends
// it again after one blank line, and from then on each --remove and each setup
// leaves the same bytes, however often they alternate. A file the user wrote
// gets back what it held before setup added the section.
func TestAgentsSectionRemoveReaddIsStable(t *testing.T) {
	stubDetectRenderOpts(t)
	integration := agentsIntegration{
		name:         "TestAgent",
		setupCommand: "bd setup testagent",
		profile:      agents.ProfileMinimal,
	}
	section := agents.RenderSection(agents.ProfileMinimal)
	scaffold := createNewAgentsFileWithProfile(agents.ProfileMinimal)

	tests := []struct {
		name        string
		seed        string // "" means setup creates the file
		wantRemoved string
	}{
		{
			name:        "file setup created",
			wantRemoved: strings.Replace(scaffold, section+"\n\n", "", 1),
		},
		{
			name:        "user file",
			seed:        "# My Project\n\nHouse rules live here.\n",
			wantRemoved: "# My Project\n\nHouse rules live here.\n",
		},
		{
			name:        "user file without a final newline",
			seed:        "# My Project",
			wantRemoved: "# My Project\n",
		},
		{
			name:        "user file ending in blank lines",
			seed:        "# My Project\n\n\n",
			wantRemoved: "# My Project\n",
		},
		{
			name:        "CRLF user file",
			seed:        "# My Project\r\n\r\nHouse rules live here.\r\n",
			wantRemoved: "# My Project\r\n\r\nHouse rules live here.\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newFactoryTestEnv(t)
			if tt.seed != "" {
				if err := os.WriteFile(env.agentsPath, []byte(tt.seed), 0o644); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			step := func(name string, run func(agentsEnv, agentsIntegration) error) string {
				t.Helper()
				if err := run(env, integration); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				data, err := os.ReadFile(env.agentsPath)
				if err != nil {
					t.Fatalf("read after %s: %v", name, err)
				}
				return string(data)
			}

			step("setup", installAgents)
			removed := step("remove", removeAgents)
			if removed != tt.wantRemoved {
				t.Fatalf("after remove:\ngot:  %q\nwant: %q", removed, tt.wantRemoved)
			}
			readded := step("setup again", installAgents)
			if want := removed + "\n" + section; readded != want {
				t.Fatalf("after setup again:\ngot:  %q\nwant: %q", readded, want)
			}
			for cycle := 2; cycle <= 3; cycle++ {
				if got := step("remove", removeAgents); got != removed {
					t.Fatalf("remove #%d changed the result:\ngot:  %q\nwant: %q", cycle, got, removed)
				}
				if got := step("setup", installAgents); got != readded {
					t.Fatalf("setup #%d changed the result:\ngot:  %q\nwant: %q", cycle+1, got, readded)
				}
			}
		})
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
	stubDetectRenderOpts(t)

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
	stubDetectRenderOpts(t)

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

	// Seed the target with content that should remain unchanged.
	original := "# Existing target content\n"
	if err := os.WriteFile(realFile, []byte(original), 0644); err != nil {
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

	// Symlink should still be a symlink.
	if info, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("lstat symlink: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected CLAUDE.md to remain a symlink")
	}

	// Read the real file — should be untouched (no managed section injection).
	data, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatalf("read real file: %v", err)
	}
	if string(data) != original {
		t.Errorf("symlink target changed:\n got: %q\nwant: %q", string(data), original)
	}
	if strings.Contains(string(data), "BEGIN BEADS INTEGRATION") {
		t.Error("symlink target should not receive managed section")
	}
	if stdout.String() != "Installing ClaudeCode integration...\n" {
		t.Errorf("expected stdout to contain only install start, got: %q", stdout.String())
	}
	stderrText := stderr.String()
	if !strings.Contains(stderrText, "CLAUDE.md is a symlink to "+realFile) {
		t.Errorf("expected warning to include symlink target, got: %s", stderrText)
	}
	if !strings.Contains(stderrText, "re-run 'bd setup claude'") {
		t.Errorf("expected warning to include corrective command, got: %s", stderrText)
	}
}

func TestInstallAgentsInspectError(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "not-dir")
	if err := os.WriteFile(notDir, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := agentsEnv{
		agentsPath: filepath.Join(notDir, "AGENTS.md"),
		stdout:     stdout,
		stderr:     stderr,
	}
	integration := agentsIntegration{
		name:         "TestAgent",
		setupCommand: "bd setup testagent",
		profile:      agents.ProfileMinimal,
	}

	err := installAgents(env, integration)
	if err == nil {
		t.Fatal("expected inspect error")
	}
	if os.IsNotExist(err) {
		t.Fatalf("expected non-not-exist error, got: %v", err)
	}
	if !strings.Contains(stderr.String(), "Error: failed to inspect") {
		t.Errorf("expected inspect error on stderr, got: %s", stderr.String())
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

// seedAgentsFileWithOldMtime writes content to path with mode 0600 and
// backdates it, so a test can tell whether installAgents rewrote it:
// atomicWriteFile replaces the file with a new one (new inode, fresh mtime).
func seedAgentsFileWithOldMtime(t *testing.T, path, content string) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

// assertAgentsFileUntouched fails if path no longer holds content, or was
// rewritten since seedAgentsFileWithOldMtime returned seed: replaced by a new
// file, given a new mtime, or had its mode reset.
func assertAgentsFileUntouched(t *testing.T, path, content string, seed os.FileInfo) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != content {
		t.Errorf("%s changed:\n got: %q\nwant: %q", path, data, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !os.SameFile(seed, info) {
		t.Errorf("%s was replaced by a new file (inode changed)", path)
	}
	if !info.ModTime().Equal(seed.ModTime()) {
		t.Errorf("%s was rewritten: mtime %v, want %v", path, info.ModTime(), seed.ModTime())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %o, want 0600", path, info.Mode().Perm())
	}
}

// A rerun of bd setup on a file whose beads section is already current must
// leave the file alone and say so, as bd init's updateAgentFile does. It used
// to write the same bytes back (fresh mtime, new inode, mode reset to 0644)
// and report "Updated existing beads section".
func TestInstallAgentsLeavesCurrentSectionUntouched(t *testing.T) {
	stubDetectRenderOpts(t)
	tests := []struct {
		name      string
		installed agents.Profile
		requested agents.Profile
	}{
		{name: "same profile", installed: agents.ProfileMinimal, requested: agents.ProfileMinimal},
		{name: "full profile kept for a minimal integration", installed: agents.ProfileFull, requested: agents.ProfileMinimal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, stderr := newFactoryTestEnv(t)
			content := "# My Project\n\n" + agents.RenderSection(tt.installed) + "\n## Notes\n"
			seed := seedAgentsFileWithOldMtime(t, env.agentsPath, content)

			integration := agentsIntegration{name: "TestAgent", setupCommand: "bd setup testagent", profile: tt.requested}
			if err := installAgents(env, integration); err != nil {
				t.Fatalf("installAgents: %v", err)
			}

			assertAgentsFileUntouched(t, env.agentsPath, content, seed)
			out := stdout.String()
			if !strings.Contains(out, "✓ AGENTS.md already has the current beads section\n") {
				t.Errorf("expected the section to be reported current, got:\n%s", out)
			}
			if strings.Contains(out, "Updated existing beads section") {
				t.Errorf("reported an update to a file it left unchanged:\n%s", out)
			}
			if stderr.Len() != 0 {
				t.Errorf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

// malformedMarkerContents are agents-file contents whose beads section cannot
// be replaced: the END marker is missing, or comes before BEGIN.
var malformedMarkerContents = []struct {
	name    string
	content string
}{
	{
		name:    "missing END marker",
		content: "# My Project\n\n<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:00000000 -->\nOlder guidance; its END marker was lost.\n",
	},
	{
		name:    "END before BEGIN",
		content: "# My Project\n\n<!-- END BEADS INTEGRATION -->\nStray text.\n<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:00000000 -->\nOlder guidance.\n",
	},
}

// A section with malformed markers cannot be replaced. installAgents used to
// write the file back unchanged and report "Updated existing beads section";
// like bd init's updateAgentFile it now leaves the file alone and returns the
// error.
func TestInstallAgentsReportsMalformedMarkers(t *testing.T) {
	stubDetectRenderOpts(t)
	for _, tt := range malformedMarkerContents {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, stderr := newFactoryTestEnv(t)
			seed := seedAgentsFileWithOldMtime(t, env.agentsPath, tt.content)

			integration := agentsIntegration{name: "TestAgent", setupCommand: "bd setup testagent", profile: agents.ProfileFull}
			err := installAgents(env, integration)
			if !errors.Is(err, agents.ErrMalformedMarkers) {
				t.Fatalf("installAgents error = %v, want %v", err, agents.ErrMalformedMarkers)
			}

			assertAgentsFileUntouched(t, env.agentsPath, tt.content, seed)
			if strings.Contains(stdout.String(), "Updated existing beads section") {
				t.Errorf("reported an update to a file it could not update:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "malformed beads section markers") {
				t.Errorf("expected the malformed markers on stderr, got: %q", stderr.String())
			}
		})
	}
}

// bd setup claude treats a CLAUDE.md it cannot update as non-fatal: hooks are
// already installed, so it warns and returns nil, leaving the file alone.
// When CLAUDE.md is an @AGENTS.md stub, the file it cannot update is AGENTS.md,
// and the beads block still in the stub must stay: stripping it is safe only
// once AGENTS.md holds the current section.
func TestInstallClaudeWarnsOnMalformedMarkers(t *testing.T) {
	stubDetectRenderOpts(t)
	for _, tt := range malformedMarkerContents {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, stderr := newClaudeTestEnv(t)
			claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
			seed := seedAgentsFileWithOldMtime(t, claudePath, tt.content)

			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude returned error: %v", err)
			}

			assertAgentsFileUntouched(t, claudePath, tt.content, seed)
			if strings.Contains(stdout.String(), "Updated existing beads section") {
				t.Errorf("reported an update to a file it could not update:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "Warning: failed to update "+claudeInstructionsFile) {
				t.Errorf("expected a non-fatal warning on stderr, got: %q", stderr.String())
			}
		})
		t.Run(tt.name+" in the redirected AGENTS.md", func(t *testing.T) {
			env, stdout, stderr := newClaudeTestEnv(t)
			claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
			claudeContent := staleClaudeStubWithBlock()
			claudeSeed := seedAgentsFileWithOldMtime(t, claudePath, claudeContent)
			agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
			agentsSeed := seedAgentsFileWithOldMtime(t, agentsPath, tt.content)

			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude returned error: %v", err)
			}

			assertAgentsFileUntouched(t, agentsPath, tt.content, agentsSeed)
			// AGENTS.md was not updated, so the stub's block is the only
			// well-formed beads section the project has.
			assertAgentsFileUntouched(t, claudePath, claudeContent, claudeSeed)
			if strings.Contains(stdout.String(), "Removed stale beads block") {
				t.Errorf("reported stripping %s although AGENTS.md was not updated:\n%s", claudeInstructionsFile, stdout.String())
			}
			if !strings.Contains(stderr.String(), "Warning: failed to update AGENTS.md") {
				t.Errorf("expected the warning to name AGENTS.md, the file that failed, got: %q", stderr.String())
			}
		})
	}
}

// bd setup mux fails on an AGENTS.md it cannot update, before installing its
// .mux hooks.
func TestInstallMuxFailsOnMalformedMarkers(t *testing.T) {
	stubDetectRenderOpts(t)
	for _, tt := range malformedMarkerContents {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newFactoryTestEnv(t)
			seed := seedAgentsFileWithOldMtime(t, env.agentsPath, tt.content)

			err := installMux(env, false, false)
			if !errors.Is(err, agents.ErrMalformedMarkers) {
				t.Fatalf("installMux error = %v, want %v", err, agents.ErrMalformedMarkers)
			}

			assertAgentsFileUntouched(t, env.agentsPath, tt.content, seed)
			if _, statErr := os.Stat(muxProjectDir(env.agentsPath)); !os.IsNotExist(statErr) {
				t.Errorf("expected no .mux hooks directory, stat err = %v", statErr)
			}
		})
	}
}
