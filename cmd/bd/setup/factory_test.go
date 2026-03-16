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

func TestUpdateBeadsSection(t *testing.T) {
	beadsSection := agents.RenderSection(agents.ProfileFull)

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "replace existing section",
			content: `# My Project

Some content

<!-- BEGIN BEADS INTEGRATION -->
Old content here
<!-- END BEADS INTEGRATION -->

More content after`,
			expected: `# My Project

Some content

` + beadsSection + `
More content after`,
		},
		{
			name:     "append when no markers exist",
			content:  "# My Project\n\nSome content",
			expected: "# My Project\n\nSome content\n\n" + beadsSection,
		},
		{
			name: "handle section at end of file",
			content: `# My Project

<!-- BEGIN BEADS INTEGRATION -->
Old content
<!-- END BEADS INTEGRATION -->`,
			expected: `# My Project

` + beadsSection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := updateBeadsSection(tt.content)
			if got != tt.expected {
				t.Errorf("updateBeadsSection() mismatch\ngot:\n%s\nwant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestRemoveBeadsSection(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "remove section in middle",
			content: `# My Project

<!-- BEGIN BEADS INTEGRATION -->
Beads content
<!-- END BEADS INTEGRATION -->

More content`,
			expected: `# My Project


More content`,
		},
		{
			name: "remove section at end",
			content: `# My Project

Content

<!-- BEGIN BEADS INTEGRATION -->
Beads content
<!-- END BEADS INTEGRATION -->`,
			expected: `# My Project

Content

`,
		},
		{
			name:     "no markers - return unchanged",
			content:  "# My Project\n\nNo beads section",
			expected: "# My Project\n\nNo beads section",
		},
		{
			name:     "only begin marker - return unchanged",
			content:  "# My Project\n<!-- BEGIN BEADS INTEGRATION -->\nContent",
			expected: "# My Project\n<!-- BEGIN BEADS INTEGRATION -->\nContent",
		},
		{
			name:     "preserve surrounding whitespace and unrelated content",
			content:  "Header\n\n" + agents.EmbeddedBeadsSection() + "\n\nFooter\n",
			expected: "Header\n\n\n\nFooter\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeBeadsSection(tt.content)
			if got != tt.expected {
				t.Errorf("removeBeadsSection() mismatch\ngot:\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestCreateNewAgentsFile(t *testing.T) {
	content := createNewAgentsFile()

	// Verify it contains required elements
	if !strings.Contains(content, "# Project Instructions for AI Agents") {
		t.Error("Missing header in new agents file")
	}

	if !containsBeadsMarker(content) {
		t.Error("Missing begin marker in new agents file")
	}

	if !strings.Contains(content, agentsEndMarker) {
		t.Error("Missing end marker in new agents file")
	}

	if !strings.Contains(content, "profile:full") {
		t.Error("Missing profile metadata in new agents file")
	}

	if !strings.Contains(content, "## Build & Test") {
		t.Error("Missing Build & Test section")
	}

	if !strings.Contains(content, "## Architecture Overview") {
		t.Error("Missing Architecture Overview section")
	}
}

func newFactoryTestEnv(t *testing.T) (factoryEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return factoryEnv{
		agentsPath: filepath.Join(dir, "AGENTS.md"),
		stdout:     stdout,
		stderr:     stderr,
	}, stdout, stderr
}

func stubFactoryEnvProvider(t *testing.T, env factoryEnv) {
	t.Helper()
	orig := factoryEnvProvider
	factoryEnvProvider = func() factoryEnv {
		return env
	}
	t.Cleanup(func() { factoryEnvProvider = orig })
}

func TestInstallFactoryCreatesNewFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	if err := installFactory(env); err != nil {
		t.Fatalf("installFactory returned error: %v", err)
	}
	data, err := os.ReadFile(env.agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	content := string(data)
	if !containsBeadsMarker(content) || !strings.Contains(content, agentsEndMarker) {
		t.Fatal("missing factory markers in new file")
	}
	if !strings.Contains(stdout.String(), "Factory.ai (Droid) integration installed") {
		t.Error("expected success message in stdout")
	}
}

func TestInstallFactoryUpdatesExistingSection(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	initial := `# Header

<!-- BEGIN BEADS INTEGRATION -->
Old content
<!-- END BEADS INTEGRATION -->

# Footer`
	if err := os.WriteFile(env.agentsPath, []byte(initial), 0644); err != nil {
		t.Fatalf("failed to seed AGENTS.md: %v", err)
	}
	if err := installFactory(env); err != nil {
		t.Fatalf("installFactory returned error: %v", err)
	}
	data, err := os.ReadFile(env.agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "Old content") {
		t.Error("old beads section should be replaced")
	}
	if !strings.Contains(content, "# Footer") {
		t.Error("content after beads section should remain")
	}
}

func TestInstallFactoryReportsWriteError(t *testing.T) {
	env, _, stderr := newFactoryTestEnv(t)
	if err := os.Mkdir(env.agentsPath, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := installFactory(env); err == nil {
		t.Fatal("expected error when agents path is directory")
	}
	if !strings.Contains(stderr.String(), "failed to read") {
		t.Error("expected error message in stderr")
	}
}

func TestCheckFactoryScenarios(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		err := checkFactory(env)
		if !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup factory") {
			t.Error("expected guidance message")
		}
	})

	t.Run("missing section", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		if err := os.WriteFile(env.agentsPath, []byte("# Project"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
		err := checkFactory(env)
		if !errors.Is(err, errBeadsSectionMissing) {
			t.Fatalf("expected errBeadsSectionMissing, got %v", err)
		}
		if !strings.Contains(stdout.String(), "no beads section") {
			t.Error("expected warning output")
		}
	})

	t.Run("success", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		// Use current rendered section (not legacy EmbeddedBeadsSection) so check reports "current"
		beadsSection := agents.RenderSection(agents.ProfileFull)
		if err := os.WriteFile(env.agentsPath, []byte(beadsSection), 0644); err != nil {
			t.Fatalf("failed to seed file: %v", err)
		}
		if err := checkFactory(env); err != nil {
			t.Fatalf("checkFactory returned error: %v", err)
		}
		if !strings.Contains(stdout.String(), "integration installed") {
			t.Error("expected success output")
		}
	})

	t.Run("stale legacy section", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		beadsSection := agents.EmbeddedBeadsSection()
		if err := os.WriteFile(env.agentsPath, []byte(beadsSection), 0644); err != nil {
			t.Fatalf("failed to seed file: %v", err)
		}
		err := checkFactory(env)
		if !errors.Is(err, errBeadsSectionStale) {
			t.Fatalf("expected errBeadsSectionStale, got %v", err)
		}
		if !strings.Contains(stdout.String(), "stale") {
			t.Error("expected stale output")
		}
	})
}

func TestRemoveFactoryScenarios(t *testing.T) {
	t.Run("remove section and keep file", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		beadsSection := agents.EmbeddedBeadsSection()
		content := "# Top\n\n" + beadsSection + "\n\n# Bottom"
		if err := os.WriteFile(env.agentsPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to seed AGENTS.md: %v", err)
		}
		if err := removeFactory(env); err != nil {
			t.Fatalf("removeFactory returned error: %v", err)
		}
		data, err := os.ReadFile(env.agentsPath)
		if err != nil {
			t.Fatalf("failed to read AGENTS.md: %v", err)
		}
		if strings.Contains(string(data), agentsBeginMarker) {
			t.Error("beads section should be removed")
		}
		if !strings.Contains(stdout.String(), "Removed beads section") {
			t.Error("expected removal message")
		}
	})

	t.Run("clear file when only beads", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		beadsSection := agents.EmbeddedBeadsSection()
		if err := os.WriteFile(env.agentsPath, []byte(beadsSection), 0644); err != nil {
			t.Fatalf("failed to seed AGENTS.md: %v", err)
		}
		if err := removeFactory(env); err != nil {
			t.Fatalf("removeFactory returned error: %v", err)
		}
		data, err := os.ReadFile(env.agentsPath)
		if err != nil {
			t.Fatalf("failed to read AGENTS.md after remove: %v", err)
		}
		if strings.TrimSpace(string(data)) != "" {
			t.Fatal("AGENTS.md should remain present but empty when only beads section existed")
		}
		if !strings.Contains(stdout.String(), "Removed beads section") {
			t.Error("expected removal message")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		env, stdout, _ := newFactoryTestEnv(t)
		if err := removeFactory(env); err != nil {
			t.Fatalf("removeFactory returned error: %v", err)
		}
		if !strings.Contains(stdout.String(), "No AGENTS.md file found") {
			t.Error("expected info message for missing file")
		}
	})
}

func TestWrapperExitsOnError(t *testing.T) {
	t.Run("InstallFactory", func(t *testing.T) {
		cap := stubSetupExit(t)
		env := factoryEnv{agentsPath: filepath.Join(t.TempDir(), "dir"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		if err := os.Mkdir(env.agentsPath, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}
		stubFactoryEnvProvider(t, env)
		InstallFactory()
		if !cap.called || cap.code != 1 {
			t.Fatal("InstallFactory should exit on error")
		}
	})

	t.Run("CheckFactory", func(t *testing.T) {
		cap := stubSetupExit(t)
		env := factoryEnv{agentsPath: filepath.Join(t.TempDir(), "missing"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		stubFactoryEnvProvider(t, env)
		CheckFactory()
		if !cap.called || cap.code != 1 {
			t.Fatal("CheckFactory should exit on error")
		}
	})

	t.Run("RemoveFactory", func(t *testing.T) {
		cap := stubSetupExit(t)
		env := factoryEnv{agentsPath: filepath.Join(t.TempDir(), "AGENTS.md"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		beadsSection := agents.EmbeddedBeadsSection()
		if err := os.WriteFile(env.agentsPath, []byte(beadsSection), 0644); err != nil {
			t.Fatalf("failed to seed file: %v", err)
		}
		if err := os.Chmod(env.agentsPath, 0o000); err != nil {
			t.Fatalf("failed to chmod file: %v", err)
		}
		stubFactoryEnvProvider(t, env)
		RemoveFactory()
		if !cap.called || cap.code != 1 {
			t.Fatal("RemoveFactory should exit on error")
		}
	})
}

func TestFactoryBeadsSectionContent(t *testing.T) {
	section := agents.EmbeddedBeadsSection()
	required := []string{"bd create", "bd update", "bd close", "bd ready", "discovered-from"}
	for _, token := range required {
		if !strings.Contains(section, token) {
			t.Errorf("EmbeddedBeadsSection() missing %q", token)
		}
	}
}

func TestFactoryMarkers(t *testing.T) {
	if !strings.Contains(agentsBeginMarker, "BEGIN") {
		t.Error("begin marker should mention BEGIN")
	}
	if !strings.Contains(agentsEndMarker, "END") {
		t.Error("end marker should mention END")
	}
}

func TestMarkersMatch(t *testing.T) {
	beadsSection := agents.EmbeddedBeadsSection()
	if !strings.HasPrefix(beadsSection, agentsBeginMarker) {
		t.Error("section should start with begin marker")
	}
	trimmed := strings.TrimSpace(beadsSection)
	if !strings.HasSuffix(trimmed, agentsEndMarker) {
		t.Error("section should end with end marker")
	}
}

func TestUpdateBeadsSectionPreservesWhitespace(t *testing.T) {
	beadsSection := agents.EmbeddedBeadsSection()
	content := "# Header\n\n" + beadsSection + "\n\n# Footer"
	updated := updateBeadsSection(content)
	if !strings.Contains(updated, "# Header") || !strings.Contains(updated, "# Footer") {
		t.Error("update should preserve surrounding content")
	}
}

func TestRoundTripAddRemoveLeavesNoBeadsContent(t *testing.T) {
	userContent := "# My Project\n\nSome user content here.\n"

	// Simulate bd init adding the beads section to an existing file
	withBeads := userContent + "\n" + agents.EmbeddedBeadsSection()

	// Verify beads content (including landing) is present
	if !strings.Contains(withBeads, "BEGIN BEADS INTEGRATION") {
		t.Fatal("beads markers should be present after adding section")
	}
	if !strings.Contains(withBeads, "Landing the Plane") {
		t.Fatal("landing-the-plane should be present inside beads section")
	}

	// Remove the beads section
	cleaned := removeBeadsSection(withBeads)

	// Verify no beads-added content remains
	if strings.Contains(cleaned, "BEGIN BEADS INTEGRATION") {
		t.Error("begin marker should not remain after removal")
	}
	if strings.Contains(cleaned, "END BEADS INTEGRATION") {
		t.Error("end marker should not remain after removal")
	}
	if strings.Contains(cleaned, "Landing the Plane") {
		t.Error("landing-the-plane should not remain after removal (must be inside markers)")
	}
	if strings.Contains(cleaned, "bd ready") {
		t.Error("beads commands should not remain after removal")
	}

	// User content should be preserved
	if !strings.Contains(cleaned, "# My Project") {
		t.Error("user content should be preserved after removal")
	}
}
