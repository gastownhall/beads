package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/recipes"
	"github.com/steveyegge/beads/internal/templates/agents"
)

// The built-in Copilot CLI recipe pairs a plugin manifest bd owns with a
// shared instructions file it does not: .github/copilot-instructions.md is
// the standard home of a repository's own Copilot instructions, so bd setup
// copilot manages a marked section there (agents.go) instead of overwriting
// the file (GH#7431). The manifest is written whole, as the recipe's generic
// install wrote it.
const (
	copilotManifestPath     = ".copilot-plugin/plugin.json"
	copilotInstructionsPath = ".github/copilot-instructions.md"
)

var (
	copilotEnvProvider = defaultCopilotEnv

	errCopilotManifestMissing    = errors.New("copilot plugin manifest not installed")
	errCopilotInstructionsLink   = errors.New("copilot instructions file is a symbolic link")
	errCopilotMarkersMalformed   = errors.New("copilot beads section markers are malformed")
)

// copilotIntegration describes the managed section installAgents keeps in
// .github/copilot-instructions.md. The plugin's hooks run bd prime at session
// start, so like Claude and Gemini the section takes the minimal profile.
var copilotIntegration = agentsIntegration{
	name:         "GitHub Copilot CLI",
	setupCommand: "bd setup copilot",
	profile:      agents.ProfileMinimal,
}

// copilotEnv holds the seams the Copilot adapter works through: the fixed
// paths of the built-in recipe's two files, the writers its messages go to
// and the file operations it makes, so tests stay hermetic.
type copilotEnv struct {
	stdout           io.Writer
	stderr           io.Writer
	manifestPath     string
	instructionsPath string
	manifestContent  func() (string, error)
	ensureDir        func(string, os.FileMode) error
	readFile         func(string) ([]byte, error)
	writeFile        func(string, []byte) error
	removeFile       func(string) error
}

func defaultCopilotEnv() copilotEnv {
	return copilotEnv{
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		manifestPath:     copilotManifestPath,
		instructionsPath: copilotInstructionsPath,
		manifestContent:  copilotManifestContent,
		ensureDir:        EnsureDir,
		readFile:         os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		removeFile: os.Remove,
	}
}

// copilotManifestContent returns the plugin manifest from the built-in
// recipe, whose contents the plugins package supplies: the adapter keeps no
// manifest of its own.
func copilotManifestContent() (string, error) {
	recipe, ok := recipes.BuiltinRecipes["copilot"]
	if !ok {
		return "", fmt.Errorf("built-in copilot recipe not found")
	}
	return recipes.ContentForPath(recipe, copilotManifestPath)
}

// agentsEnv is the instructions-file half of the recipe: the shared
// .github/copilot-instructions.md with a section-only lifecycle, rendered
// with the render-option detection every other project-level file uses, so
// install and --check agree.
func (env copilotEnv) agentsEnv() agentsEnv {
	return agentsEnv{
		agentsPath: env.instructionsPath,
		stdout:     env.stdout,
		stderr:     env.stderr,
		// A missing or empty file becomes just the managed section, without
		// the AGENTS.md scaffolding a project instructions file gets.
		sectionOnly: true,
	}
}

func InstallCopilot() error {
	return installCopilot(copilotEnvProvider())
}

func installCopilot(env copilotEnv) error {
	manifest, err := env.manifestContent()
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: resolve plugin manifest: %v\n", err)
		return err
	}

	_, _ = fmt.Fprintln(env.stdout, "Installing GitHub Copilot CLI plugin manifest...")
	if err := env.ensureDir(filepath.Dir(env.manifestPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: create directory: %v\n", err)
		return err
	}
	if err := env.writeFile(env.manifestPath, []byte(manifest)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.manifestPath, err)
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Copilot plugin manifest installed\n  File: %s\n", env.manifestPath)

	// The shared instructions file keeps every byte the user wrote; only the
	// marked section is bd's. An error here leaves the manifest installed,
	// as the recipe's non-transactional generic install did.
	if err := env.ensureDir(filepath.Dir(env.instructionsPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: create directory: %v\n", err)
		return err
	}
	return installAgents(env.agentsEnv(), copilotIntegration)
}

func CheckCopilot() error {
	return checkCopilot(copilotEnvProvider())
}

// checkCopilot reports the recipe's two files separately: the manifest bd
// owns is present or missing, and the shared instructions file holds a
// current beads section (checkAgents: missing, no section, stale, current).
// A file the section check would misread — a destination symlink, or markers
// that are incomplete or reversed — is refused with its remedy instead of
// being reported installed.
func checkCopilot(env copilotEnv) error {
	if _, err := os.Stat(env.manifestPath); err != nil {
		if !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", env.manifestPath, err)
			return err
		}
		_, _ = fmt.Fprintf(env.stdout, "✗ %s integration not installed\n", copilotIntegration.name)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", copilotIntegration.setupCommand)
		_, _ = fmt.Fprintf(env.stdout, "  Missing: %s\n", env.manifestPath)
		return errCopilotManifestMissing
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Plugin manifest installed\n  File: %s\n", env.manifestPath)

	if err := copilotPrecheckInstructions(env); err != nil {
		return err
	}
	return checkAgents(env.agentsEnv(), copilotIntegration)
}

// copilotPrecheckInstructions refuses the states checkAgents cannot report
// honestly: a .github/copilot-instructions.md that is a symbolic link (the
// check would read the link's target and judge it the integration's file),
// and one whose beads markers are incomplete or reversed (a section that
// looks current but cannot be used or updated). Each refusal names the path
// and the remedy; nothing is modified.
func copilotPrecheckInstructions(env copilotEnv) error {
	path := env.instructionsPath
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil // checkAgents reports the missing file
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", path, err)
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_, _ = fmt.Fprintf(env.stdout, "✗ %s is a symbolic link\n", path)
		_, _ = fmt.Fprintf(env.stdout, "  Replace the link with a regular file and re-run '%s'.\n", copilotIntegration.setupCommand)
		return errCopilotInstructionsLink
	}
	data, err := env.readFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", path, err)
		return err
	}
	if content := string(data); copilotMarkersMalformed(content) {
		_, _ = fmt.Fprintf(env.stdout, "⚠ %s has malformed beads section markers\n", path)
		_, _ = fmt.Fprintf(env.stdout, "  Fix or remove the BEGIN/END BEADS INTEGRATION markers, then re-run '%s'.\n", copilotIntegration.setupCommand)
		return errCopilotMarkersMalformed
	}
	return nil
}

// copilotMarkersMalformed reports whether content holds a BEGIN BEADS
// INTEGRATION marker whose END marker is missing or comes before it.
func copilotMarkersMalformed(content string) bool {
	begin := findBeginMarker(content)
	if begin == -1 {
		return false
	}
	end := strings.Index(content, agentsEndMarker)
	return end == -1 || end < begin
}
