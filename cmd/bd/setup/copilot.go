package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/templates/agents"
)

// The built-in copilot recipe's files. The plugin manifest is bd's own; the
// instructions file is the repository's, where Copilot reads its custom
// instructions, so bd keeps only a managed section in it (GH#7431).
const (
	CopilotManifestPath     = ".copilot-plugin/plugin.json"
	CopilotInstructionsPath = ".github/copilot-instructions.md"
)

var errCopilotMalformedSection = errors.New("malformed beads section markers")

var copilotAgentsIntegration = agentsIntegration{
	name:         "GitHub Copilot CLI",
	setupCommand: "bd setup copilot",
	profile:      agents.ProfileMinimal,
}

type copilotEnv struct {
	stdout           io.Writer
	stderr           io.Writer
	manifestPath     string
	instructionsPath string
	// manifest is the plugin manifest install writes, the built-in recipe's.
	manifest string
	// renderOpts, when set, fixes the section's render options instead of
	// detecting them from the current directory's beads config.
	renderOpts func() agents.RenderOpts
}

func defaultCopilotEnv(manifest string) copilotEnv {
	return copilotEnv{
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		manifestPath:     CopilotManifestPath,
		instructionsPath: CopilotInstructionsPath,
		manifest:         manifest,
	}
}

// instructionsEnv is the agentsEnv that manages the beads section in the
// instructions file. A missing or empty file gets the section alone, without
// the AGENTS.md scaffolding.
func (env copilotEnv) instructionsEnv() agentsEnv {
	return agentsEnv{
		agentsPath:  env.instructionsPath,
		stdout:      env.stdout,
		stderr:      env.stderr,
		sectionOnly: true,
		renderOpts:  env.renderOpts,
	}
}

// InstallCopilot writes the Copilot CLI plugin manifest and adds or updates
// the beads section in .github/copilot-instructions.md, keeping the rest of
// that file as it is.
func InstallCopilot(manifest string) error {
	return installCopilot(defaultCopilotEnv(manifest))
}

func installCopilot(env copilotEnv) error {
	// Refuse markers the section cannot be found by before writing anything:
	// an END marker alone would otherwise get a section appended after it.
	if err := validateCopilotInstructions(env); err != nil {
		return err
	}

	for _, path := range []string{env.manifestPath, env.instructionsPath} {
		if err := EnsureDir(filepath.Dir(path), 0o755); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
			return err
		}
	}
	if err := os.WriteFile(env.manifestPath, []byte(env.manifest), 0o644); err != nil { // #nosec G306 -- config files need to be readable
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.manifestPath, err)
		return err
	}

	skipped := false
	instructions := env.instructionsEnv()
	instructions.skipped = &skipped
	integration := copilotAgentsIntegration
	integration.readHint = "Plugin manifest: " + env.manifestPath
	if err := installAgents(instructions, integration); err != nil {
		return err
	}
	if skipped {
		_, _ = fmt.Fprintf(env.stdout, "\n✓ GitHub Copilot CLI plugin manifest installed: %s\n", env.manifestPath)
		_, _ = fmt.Fprintf(env.stdout, "  Instructions skipped: %s is a symlink\n", env.instructionsPath)
	}
	return nil
}

// validateCopilotInstructions fails, saying so, when the instructions file's
// beads markers are malformed. A missing file passes, and so does a symlink,
// which bd neither reads nor writes through.
func validateCopilotInstructions(env copilotEnv) error {
	info, err := os.Lstat(env.instructionsPath)
	if os.IsNotExist(err) || (err == nil && info.Mode()&os.ModeSymlink != 0) {
		return nil
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", env.instructionsPath, err)
		return err
	}
	data, err := os.ReadFile(env.instructionsPath) // #nosec G304 -- bd's own setup destination
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.instructionsPath, err)
		return err
	}
	if err := validateCopilotSection(string(data)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %s: %v\n", env.instructionsPath, err)
		_, _ = fmt.Fprintf(env.stderr, "  Fix or delete the beads markers by hand, then run: %s\n", copilotAgentsIntegration.setupCommand)
		return err
	}
	return nil
}

// validateCopilotSection reports a beads section bd cannot tell the extent
// of: a BEGIN marker with no END marker, an END marker with no BEGIN marker,
// or an END marker ahead of the BEGIN marker.
func validateCopilotSection(content string) error {
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)
	switch {
	case start < 0 && end < 0:
		return nil
	case start < 0:
		return fmt.Errorf("%w: END marker without a BEGIN marker", errCopilotMalformedSection)
	case end < 0:
		return fmt.Errorf("%w: BEGIN marker without an END marker", errCopilotMalformedSection)
	case end < start:
		return fmt.Errorf("%w: END marker before the BEGIN marker", errCopilotMalformedSection)
	}
	return nil
}

var (
	errCopilotNotInstalled       = errors.New("copilot integration not installed")
	errCopilotInstructionsLinked = errors.New("copilot instructions are a symlink")
)

// CheckCopilot reports whether the Copilot CLI integration is installed: the
// plugin manifest exists and .github/copilot-instructions.md holds a current
// beads section. A file without the section, whatever else it holds, is not
// installed.
func CheckCopilot() error {
	return checkCopilot(defaultCopilotEnv(""))
}

func checkCopilot(env copilotEnv) error {
	setupCommand := copilotAgentsIntegration.setupCommand
	if info, err := os.Lstat(env.instructionsPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		_, _ = fmt.Fprintf(env.stdout, "✗ %s is a symlink; bd keeps its beads section only in a regular file\n", env.instructionsPath)
		_, _ = fmt.Fprintf(env.stdout, "  Replace the symlink with a regular file, then run: %s\n", setupCommand)
		return errCopilotInstructionsLinked
	}

	var missing []string
	if _, err := os.Stat(env.manifestPath); os.IsNotExist(err) {
		missing = append(missing, env.manifestPath)
	}
	if _, err := os.Lstat(env.instructionsPath); os.IsNotExist(err) {
		missing = append(missing, env.instructionsPath)
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(env.stdout, "✗ %s integration not installed\n", copilotAgentsIntegration.name)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		for _, path := range missing {
			_, _ = fmt.Fprintf(env.stdout, "  Missing: %s\n", path)
		}
		return errCopilotNotInstalled
	}

	if err := validateCopilotInstructions(env); err != nil {
		return err
	}
	if err := checkAgents(env.instructionsEnv(), copilotAgentsIntegration); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "  File: %s\n", env.manifestPath)
	return nil
}
