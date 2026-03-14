package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/templates/agents"
	"github.com/steveyegge/beads/internal/utils"
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
		agentsPath: "AGENTS.md",
		stdout:     os.Stdout,
		stderr:     os.Stderr,
	}
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

func installAgents(env agentsEnv, integration agentsIntegration) error {
	_, _ = fmt.Fprintf(env.stdout, "Installing %s integration...\n", integration.name)

	profile := resolveProfile(integration)

	// Resolve symlinks so that e.g. CLAUDE.md -> AGENTS.md writes to the real target.
	// This uses the existing atomicWriteFile path which also calls ResolveForWrite,
	// but we need the resolved path here to read the current content from the right place.
	resolvedPath, err := utils.ResolveForWrite(env.agentsPath)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: resolve path %s: %v\n", env.agentsPath, err)
		return err
	}

	var currentContent string
	data, err := os.ReadFile(resolvedPath) // #nosec G304 -- resolvedPath is derived from env.agentsPath via ResolveForWrite
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

	beadsSection := agents.RenderSection(profile)

	if currentContent != "" {
		if containsBeadsMarker(currentContent) {
			newContent := updateBeadsSectionWithProfile(currentContent, profile)
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintln(env.stdout, "✓ Updated existing beads section in AGENTS.md")
		} else {
			newContent := currentContent + "\n\n" + beadsSection
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintln(env.stdout, "✓ Added beads section to existing AGENTS.md")
		}
	} else {
		newContent := createNewAgentsFileWithProfile(profile)
		if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
			return err
		}
		_, _ = fmt.Fprintln(env.stdout, "✓ Created new AGENTS.md with beads integration")
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
	data, err := os.ReadFile(env.agentsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintln(env.stdout, "✗ AGENTS.md not found")
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", integration.setupCommand)
		return errAgentsFileMissing
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	content := string(data)
	if !containsBeadsMarker(content) {
		_, _ = fmt.Fprintln(env.stdout, "⚠ AGENTS.md exists but no beads section found")
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

	currentHash := agents.CurrentHash(profile)
	if meta != nil && meta.Hash == currentHash && existingProf == profile {
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
	data, err := os.ReadFile(env.agentsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintln(env.stdout, "No AGENTS.md file found")
		return nil
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	content := string(data)
	if !containsBeadsMarker(content) {
		_, _ = fmt.Fprintln(env.stdout, "No beads section found in AGENTS.md")
		return nil
	}

	newContent := removeBeadsSection(content)

	if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
		return err
	}
	_, _ = fmt.Fprintln(env.stdout, "✓ Removed beads section from AGENTS.md")
	return nil
}

// updateBeadsSection replaces the beads section in existing content using the full profile.
// Kept for backward compatibility with existing callers and tests.
func updateBeadsSection(content string) string {
	return updateBeadsSectionWithProfile(content, agents.ProfileFull)
}

// updateBeadsSectionWithProfile replaces the beads section with the given profile.
// Handles both legacy markers (exact match) and new-format markers (prefix match with metadata).
func updateBeadsSectionWithProfile(content string, profile agents.Profile) string {
	beadsSection := agents.RenderSection(profile)

	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)

	if start == -1 || end == -1 || start > end {
		// Markers not found or invalid, append instead
		return content + "\n\n" + beadsSection
	}

	// Replace section between markers (including end marker line)
	endOfEndMarker := end + len(agentsEndMarker)
	// Find the next newline after end marker
	nextNewline := strings.Index(content[endOfEndMarker:], "\n")
	if nextNewline != -1 {
		endOfEndMarker += nextNewline + 1
	}

	return content[:start] + beadsSection + content[endOfEndMarker:]
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
	beadsSection := agents.RenderSection(profile)

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
