package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/templates/agents"
)

// readFileBytesImpl is used in tests; avoids import cycle.
var readFileBytesImpl = os.ReadFile

// AGENTS.md integration markers for beads section
const (
	agentsBeginMarker = "<!-- BEGIN BEADS INTEGRATION -->"
	agentsEndMarker   = "<!-- END BEADS INTEGRATION -->"
)

var (
	errAgentsFileMissing   = errors.New("agents file not found")
	errBeadsSectionMissing = errors.New("beads section missing")
	errBeadsSectionStale   = errors.New("beads section is stale")
)

const muxAgentInstructionsURL = "https://mux.coder.com/AGENTS.md"

type agentsEnv struct {
	agentsPath string
	stdout     io.Writer
	stderr     io.Writer
	skipped    *bool
}

type agentsIntegration struct {
	name         string
	setupCommand string
	readHint     string
	docsURL      string
	profile      agents.Profile // "full" or "minimal"; empty defaults to "full"
}

func defaultAgentsEnv() agentsEnv {
	return agentsEnv{
		agentsPath: config.SafeAgentsFile(),
		stdout:     os.Stdout,
		stderr:     os.Stderr,
	}
}

// detectRenderOptsImpl checks whether a Dolt sync remote is configured and returns
// appropriate RenderOpts. When no remote is configured, the rendered template
// omits "bd dolt push" from session-completion instructions.
// Exposed as a variable so tests can override.
var detectRenderOptsImpl = func() agents.RenderOpts {
	return agents.RenderOpts{
		HasRemote: config.GetString("sync.remote") != "" || config.GetString("sync.git-remote") != "",
		NoPush:    config.GetBool("no-push"),
	}
}

func detectRenderOpts() agents.RenderOpts {
	return detectRenderOptsImpl()
}

// containsBeadsMarker returns true if content contains a BEGIN BEADS INTEGRATION marker
// (either legacy or new format with metadata).
func containsBeadsMarker(content string) bool {
	return strings.Contains(content, "<!-- BEGIN BEADS INTEGRATION")
}

// resolveProfile returns the integration's profile, defaulting to full.
func resolveProfile(integration agentsIntegration) agents.Profile {
	if integration.profile != "" {
		return integration.profile
	}
	return agents.ProfileFull
}

func agentsFileName(path string) string {
	base := filepath.Base(path)
	if base == "" || base == "." {
		return path
	}
	return base
}

func (env agentsEnv) markSkipped() {
	if env.skipped != nil {
		*env.skipped = true
	}
}

func installAgents(env agentsEnv, integration agentsIntegration) error {
	_, _ = fmt.Fprintf(env.stdout, "Installing %s integration...\n", integration.name)
	agentsFile := agentsFileName(env.agentsPath)

	// Never inject managed sections through symlinks. Following symlink targets
	// can unexpectedly mutate other instruction files and, in some workflows,
	// corrupt tracked symlink entries.
	if info, err := os.Lstat(env.agentsPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(env.agentsPath)
		targetHint := ""
		if readErr == nil && target != "" {
			targetHint = fmt.Sprintf(" to %s", target)
		}
		_, _ = fmt.Fprintf(env.stderr, "Warning: %s is a symlink%s; skipping managed section injection to preserve link mode/content. Update the target file directly, or replace the symlink with a regular file and re-run '%s'.\n", agentsFile, targetHint, integration.setupCommand)
		env.markSkipped()
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", env.agentsPath, err)
		return err
	}

	profile := resolveProfile(integration)
	opts := detectRenderOpts()

	var currentContent string
	data, err := os.ReadFile(env.agentsPath) // #nosec G304 -- env.agentsPath is trusted setup destination
	if err == nil {
		currentContent = string(data)
	} else if !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	// Profile precedence: if the file already has a full profile and we're
	// requesting minimal, preserve full to avoid information loss (e.g. when
	// CLAUDE.md is a symlink to AGENTS.md and both Claude and Codex target it).
	if currentContent != "" && containsBeadsMarker(currentContent) {
		existingProfile := existingBeadsProfile(currentContent)
		if existingProfile == agents.ProfileFull && profile == agents.ProfileMinimal {
			_, _ = fmt.Fprintf(env.stdout, "  ℹ File already has full profile; preserving (higher-information) content\n")
			profile = agents.ProfileFull
		}
	}

	beadsSection := agents.RenderSectionWithOpts(profile, opts)

	if currentContent != "" {
		if containsBeadsMarker(currentContent) {
			newContent := updateBeadsSectionWithOpts(currentContent, profile, opts)
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintf(env.stdout, "✓ Updated existing beads section in %s\n", agentsFile)
		} else {
			newContent := currentContent + "\n\n" + beadsSection
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintf(env.stdout, "✓ Added beads section to existing %s\n", agentsFile)
		}
	} else {
		newContent := createNewAgentsFileWithOpts(profile, opts)
		if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
			return err
		}
		_, _ = fmt.Fprintf(env.stdout, "✓ Created new %s with beads integration\n", agentsFile)
	}

	_, _ = fmt.Fprintf(env.stdout, "\n✓ %s integration installed\n", integration.name)
	_, _ = fmt.Fprintf(env.stdout, "  File: %s\n", env.agentsPath)
	if integration.readHint != "" {
		_, _ = fmt.Fprintf(env.stdout, "\n%s\n", integration.readHint)
	}
	if integration.docsURL != "" {
		_, _ = fmt.Fprintf(env.stdout, "Review guide: %s\n", integration.docsURL)
	}
	_, _ = fmt.Fprintln(env.stdout, "No additional configuration needed!")
	return nil
}

func checkAgents(env agentsEnv, integration agentsIntegration) error {
	agentsFile := agentsFileName(env.agentsPath)

	data, err := os.ReadFile(env.agentsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stdout, "✗ %s not found\n", agentsFile)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", integration.setupCommand)
		return errAgentsFileMissing
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	content := string(data)
	if !containsBeadsMarker(content) {
		_, _ = fmt.Fprintf(env.stdout, "⚠ %s exists but no beads section found\n", agentsFile)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s (to add beads section)\n", integration.setupCommand)
		return errBeadsSectionMissing
	}

	// Section exists — check freshness via profile and hash
	profile := resolveProfile(integration)
	existingProf := existingBeadsProfile(content)

	// Extract hash from marker
	idx := findBeginMarker(content)
	line := content[idx:]
	if nl := strings.Index(line, "\n"); nl != -1 {
		line = line[:nl]
	}
	meta := agents.ParseMarker(line)

	checkProfile := profile
	if profile == agents.ProfileMinimal && existingProf == agents.ProfileFull {
		// Accept full profile as current when a minimal integration targets the same
		// file (typically via symlinks like CLAUDE.md -> AGENTS.md).
		checkProfile = agents.ProfileFull
	}

	currentHash := agents.CurrentHashWithOpts(checkProfile, detectRenderOpts())
	if meta != nil && meta.Hash == currentHash && existingProf == checkProfile {
		_, _ = fmt.Fprintf(env.stdout, "✓ %s integration installed: %s (current)\n", integration.name, env.agentsPath)
		return nil
	}

	// Stale or legacy section
	_, _ = fmt.Fprintf(env.stdout, "⚠ %s integration installed but stale: %s\n", integration.name, env.agentsPath)
	_, _ = fmt.Fprintf(env.stdout, "  Run: %s (to update)\n", integration.setupCommand)
	return errBeadsSectionStale
}

func removeAgents(env agentsEnv, integration agentsIntegration) error {
	_, _ = fmt.Fprintf(env.stdout, "Removing %s integration...\n", integration.name)
	agentsFile := agentsFileName(env.agentsPath)
	data, err := os.ReadFile(env.agentsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stdout, "No %s file found\n", agentsFile)
		return nil
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	content := string(data)
	if !containsBeadsMarker(content) {
		_, _ = fmt.Fprintf(env.stdout, "No beads section found in %s\n", agentsFile)
		return nil
	}

	newContent := removeBeadsSection(content)

	if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed beads section from %s\n", agentsFile)
	return nil
}

// updateBeadsSection replaces the beads section in existing content using the full profile.
// Kept for backward compatibility with existing callers and tests.
func updateBeadsSection(content string) string {
	return updateBeadsSectionWithProfile(content, agents.ProfileFull)
}

// updateBeadsSectionWithProfile replaces the beads section with the given profile.
// Delegates to the canonical agents.ReplaceSection. Returns an error string on
// malformed markers (logged by callers) instead of silently appending.
func updateBeadsSectionWithProfile(content string, profile agents.Profile) string {
	return updateBeadsSectionWithOpts(content, profile, agents.DefaultRenderOpts())
}

// updateBeadsSectionWithOpts replaces the beads section with the given profile and render opts.
func updateBeadsSectionWithOpts(content string, profile agents.Profile, opts agents.RenderOpts) string {
	replaced, _, err := agents.ReplaceSectionWithOpts(content, profile, opts)
	if err != nil {
		return content
	}
	return replaced
}

// removeBeadsSection removes the beads section from content
func removeBeadsSection(content string) string {
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)

	if start == -1 || end == -1 || start > end {
		return content
	}

	// Remove exactly the managed section, including a single trailing newline
	// immediately after the end marker if present. We intentionally do NOT trim
	// surrounding whitespace or unrelated content to keep user file content intact.
	endOfEndMarker := end + len(agentsEndMarker)
	if endOfEndMarker < len(content) {
		switch content[endOfEndMarker] {
		case '\r':
			endOfEndMarker++
			if endOfEndMarker < len(content) && content[endOfEndMarker] == '\n' {
				endOfEndMarker++
			}
		case '\n':
			endOfEndMarker++
		}
	}

	return content[:start] + content[endOfEndMarker:]
}

// findBeginMarker returns the index of the BEGIN BEADS INTEGRATION marker in content,
// matching both legacy (exact) and new (with metadata) formats via prefix match.
// Returns -1 if not found.
func findBeginMarker(content string) int {
	return strings.Index(content, "<!-- BEGIN BEADS INTEGRATION")
}

// existingBeadsProfile extracts the profile from an existing beads section's
// begin marker. Returns ProfileFull if the marker contains "profile:full" or
// if it's a legacy marker (legacy sections contain full content).
// Returns ProfileMinimal only if explicitly marked as such.
func existingBeadsProfile(content string) agents.Profile {
	idx := findBeginMarker(content)
	if idx == -1 {
		return agents.ProfileFull
	}
	line := content[idx:]
	if nl := strings.Index(line, "\n"); nl != -1 {
		line = line[:nl]
	}
	meta := agents.ParseMarker(line)
	if meta == nil || meta.Profile == "" {
		// Legacy marker — assume full (it contains all the content)
		return agents.ProfileFull
	}
	return meta.Profile
}

// createNewAgentsFile creates a new AGENTS.md with a basic template using the full profile.
func createNewAgentsFile() string {
	return createNewAgentsFileWithProfile(agents.ProfileFull)
}

// createNewAgentsFileWithProfile creates a new AGENTS.md with the given profile.
func createNewAgentsFileWithProfile(profile agents.Profile) string {
	return createNewAgentsFileWithOpts(profile, agents.DefaultRenderOpts())
}

// createNewAgentsFileWithOpts creates a new AGENTS.md with the given profile and render opts.
func createNewAgentsFileWithOpts(profile agents.Profile, opts agents.RenderOpts) string {
	beadsSection := agents.RenderSectionWithOpts(profile, opts)

	return `# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

` + beadsSection + `

## Build & Test

_Add your build and test commands here_

` + "```bash" + `
# Example:
# npm install
# npm test
` + "```" + `

## Architecture Overview

_Add a brief overview of your project architecture_

## Conventions & Patterns

_Add your project-specific conventions here_
`
}
