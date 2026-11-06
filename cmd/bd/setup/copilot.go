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

func RemoveCopilot() error {
	return removeCopilot(copilotEnvProvider())
}

// removeCopilot takes the recipe's two files apart by ownership: the plugin
// manifest bd owns goes whole (with its directory, when that left it empty;
// setup.RemoveEmptyDir keeps a symlinked one and one holding anything else),
// and from the shared instructions file only the marked beads section is
// taken, leaving every other byte of the user's file alone.
func removeCopilot(env copilotEnv) error {
	removed := false

	if err := env.removeFile(env.manifestPath); err == nil {
		removed = true
		_, _ = fmt.Fprintf(env.stdout, "✓ Removed %s\n", env.manifestPath)
		_ = RemoveEmptyDir(filepath.Dir(env.manifestPath))
	} else if !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to remove %s: %v\n", env.manifestPath, err)
		return err
	}

	sectionRemoved, err := copilotRemoveInstructions(env)
	if err != nil {
		return err
	}

	if !removed && !sectionRemoved {
		_, _ = fmt.Fprintln(env.stdout, "No integration files found")
		return nil
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed %s integration\n", copilotIntegration.name)
	return nil
}

// copilotRemoveInstructions takes the beads section out of
// .github/copilot-instructions.md and reports whether it took anything out.
// A missing file and an unmarked one are the user's: removeAgents reports
// them and touches nothing. A destination symlink is left alone, since
// removing through it would unlink or rewrite its target, and markers that
// are incomplete or reversed are an error that changes nothing. For a valid
// section removeAgents keeps the text around it, with its documented
// normalization of the line breaks that set the section off; a file the
// section was the whole of is deleted, since a Copilot install of a missing
// file created it to hold only the section.
func copilotRemoveInstructions(env copilotEnv) (bool, error) {
	path := env.instructionsPath

	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", path, err)
			return false, err
		}
		return false, removeAgents(env.agentsEnv(), copilotIntegration)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_, _ = fmt.Fprintf(env.stdout, "Leaving %s alone: it is a symbolic link; take the beads section out of the file it points to\n", path)
		return false, nil
	}
	data, err := env.readFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", path, err)
		return false, err
	}
	content := string(data)
	if !containsBeadsMarker(content) {
		return false, removeAgents(env.agentsEnv(), copilotIntegration)
	}
	if copilotMarkersMalformed(content) {
		_, _ = fmt.Fprintf(env.stdout, "⚠ %s has malformed beads section markers; nothing removed\n", path)
		_, _ = fmt.Fprintf(env.stdout, "  Fix or remove the BEGIN/END BEADS INTEGRATION markers, then re-run '%s --remove'.\n", copilotIntegration.setupCommand)
		return false, errCopilotMarkersMalformed
	}

	if err := removeAgents(env.agentsEnv(), copilotIntegration); err != nil {
		return false, err
	}

	// The shared helper leaves the emptied file in place (a Gemini or Claude
	// section-only file stays); a Copilot install created this file to hold
	// only the section, so deleting it restores that lifecycle. Only a
	// directory this deletion emptied goes (RemoveEmptyDir keeps a
	// symlinked one, and one holding anything else).
	if data, err := env.readFile(path); err == nil && strings.TrimSpace(string(data)) == "" {
		if err := env.removeFile(path); err != nil && !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to remove %s: %v\n", path, err)
			return true, err
		}
		_ = RemoveEmptyDir(filepath.Dir(path))
		_, _ = fmt.Fprintf(env.stdout, "✓ Deleted the emptied %s\n", agentsFileName(path))
	}
	return true, nil
}
