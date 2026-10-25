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
	// sectionOnly creates a missing or empty file holding only the managed
	// section, without the project scaffolding createNewAgentsFileWithOpts
	// adds. For user-level files, which apply to every project.
	sectionOnly bool
	// renderOpts, when set, fixes the render options instead of detecting
	// them from the current directory's beads config. For user-level files,
	// which every project shares, so install and --check agree everywhere.
	renderOpts func() agents.RenderOpts
	// insertBefore, when set, keeps the section in an existing file ahead of
	// the line holding the first occurrence of this text, where Gemini CLI's
	// indexOf finds its memories heading: a new section is added there
	// instead of at the end, and one below it is stale and moved there. For
	// a file whose owner finds its own section that way and appends to it up
	// to the next "## " heading, which below it would be the managed
	// section's.
	insertBefore string
}

func (env agentsEnv) resolveRenderOpts() agents.RenderOpts {
	if env.renderOpts != nil {
		return env.renderOpts()
	}
	return detectRenderOpts()
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
	opts := env.resolveRenderOpts()

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
		if lifted, ok := liftBeadsSectionBelow(currentContent, env.insertBefore); ok {
			// Updated where it stands, the section would go on collecting
			// what the file's owner adds below insertBefore.
			currentContent = lifted
			_, _ = fmt.Fprintf(env.stdout, "  ℹ Moving the beads section ahead of %q\n", env.insertBefore)
		}
		if containsBeadsMarker(currentContent) {
			// Like bd init's updateAgentFile: write only when the section
			// actually changes, so a rerun leaves a current file alone.
			newContent, changed, err := agents.ReplaceSectionWithOpts(currentContent, profile, opts)
			switch {
			case err != nil:
				_, _ = fmt.Fprintf(env.stderr, "Error: failed to update beads section in %s: %v\n", env.agentsPath, err)
				return err
			case !changed:
				_, _ = fmt.Fprintf(env.stdout, "✓ %s already has the current beads section\n", agentsFile)
			default:
				if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
					_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
					return err
				}
				_, _ = fmt.Fprintf(env.stdout, "✓ Updated existing beads section in %s\n", agentsFile)
			}
		} else {
			newContent := addBeadsSection(currentContent, beadsSection, env.insertBefore)
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintf(env.stdout, "✓ Added beads section to existing %s\n", agentsFile)
		}
	} else {
		newContent := createNewAgentsFileWithOpts(profile, opts)
		if env.sectionOnly {
			newContent = beadsSection
		}
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

	currentHash := agents.CurrentHashWithOpts(checkProfile, env.resolveRenderOpts())
	// A section an install would move (liftBeadsSectionBelow) is stale where
	// it stands, however current its text.
	_, below := liftBeadsSectionBelow(content, env.insertBefore)
	if meta != nil && meta.Hash == currentHash && existingProf == checkProfile && !below {
		_, _ = fmt.Fprintf(env.stdout, "✓ %s integration installed: %s (current)\n", integration.name, env.agentsPath)
		return nil
	}

	// Stale or legacy section
	_, _ = fmt.Fprintf(env.stdout, "⚠ %s integration installed but stale: %s\n", integration.name, env.agentsPath)
	_, _ = fmt.Fprintf(env.stdout, "  Run: %s (to update)\n", integration.setupCommand)
	return errBeadsSectionStale
}

func removeAgents(env agentsEnv, integration agentsIntegration) error {
	_, _ = fmt.Fprintf(env.stdout, "Removing %s integration from %s...\n", integration.name, env.agentsPath)
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

	newContent, lifted := liftBeadsSectionBelow(content, env.insertBefore)
	if !lifted {
		newContent = removeBeadsSection(content)
	}

	if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed beads section from %s\n", agentsFile)
	return nil
}

// addBeadsSection adds section to content that has none. It goes at the end,
// set off with one blank line as bd init's updateAgentFile does, so that
// removeBeadsSection gives the file back as it was. When before is set and
// content contains it, the section goes ahead of the line holding its first
// occurrence instead, followed by a blank line when one precedes it, which
// removeBeadsSection also undoes.
func addBeadsSection(content, section, before string) string {
	if before != "" {
		if at := strings.Index(content, before); at >= 0 {
			at = strings.LastIndexByte(content[:at], '\n') + 1
			sep := "\n"
			if at > 0 && !strings.HasSuffix(content[:at], "\n\n") {
				sep = ""
			}
			return content[:at] + section + sep + content[at:]
		}
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + "\n" + section
}

// liftBeadsSectionBelow takes the beads section out of content, as
// removeBeadsSection does, when it sits below the line holding the first
// occurrence of before, and reports whether it did. Below it, the section's
// first "## " heading, which every beads section opens with, ends the owner's
// section that before starts, so the owner's entries land inside the section,
// between its BEGIN marker's line and that heading. They are not the
// section's own, so they stay where they are. A BEGIN marker that shares its
// line with other text is left alone.
func liftBeadsSectionBelow(content, before string) (string, bool) {
	at := strings.Index(content, before)
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)
	if before == "" || at < 0 || start < at || end < start || !strings.HasSuffix(content[:start], "\n") {
		return content, false
	}
	section := content[start:end]
	body := strings.IndexByte(section, '\n') + 1
	entries := 0
	if body > 0 && !strings.HasPrefix(section[body:], "## ") {
		entries = strings.Index(section[body:], "\n## ") + 1
	}
	// Put the entries ahead of the BEGIN marker's line, outside the section.
	content = content[:start] + section[body:body+entries] + section[:body] + content[start+body+entries:]
	return removeBeadsSection(content), true
}

// updateBeadsSection replaces the beads section in existing content using the full profile.
// Kept for backward compatibility with existing callers and tests.
func updateBeadsSection(content string) string {
	return updateBeadsSectionWithProfile(content, agents.ProfileFull)
}

// updateBeadsSectionWithProfile replaces the beads section with the given profile.
// Delegates to the canonical agents.ReplaceSection. When the section cannot be
// replaced (no BEGIN marker, or malformed markers) it returns content unchanged
// and reports nothing; a caller that must report that calls
// agents.ReplaceSectionWithOpts directly, as installAgents does.
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

// removeBeadsSection removes the beads section from content together with the
// blank lines that set it off, so removing it leaves no hole. Only line breaks
// next to the section are dropped, never text: the text before and after it is
// rejoined with one blank line if the section was set off by any (with none if
// it was not), a section at the top of the file leaves no leading blank lines,
// and one at the bottom leaves the file ending in a single line break. This
// undoes installAgents' append, so adding the section to a file and removing it
// gives the file back.
//
// A marker that shares its line with other text is cut out exactly, with the
// line break after the END marker, and nothing around it is touched.
func removeBeadsSection(content string) string {
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)

	if start == -1 || end == -1 || start > end {
		return content
	}

	before := content[:start]
	after := content[end+len(agentsEndMarker):]
	head := strings.TrimRight(before, "\r\n")
	tail := strings.TrimLeft(after, "\r\n")
	if (head != "" && head == before) || (tail != "" && tail == after) {
		after = strings.TrimPrefix(after, "\r")
		return before + strings.TrimPrefix(after, "\n")
	}

	eol := "\n"
	if strings.HasPrefix(before[len(head):], "\r\n") {
		eol = "\r\n"
	}
	switch {
	case head == "":
		return tail
	case tail == "":
		return head + eol
	case strings.Count(before[len(head):], "\n")+strings.Count(after[:len(after)-len(tail)], "\n") > 2:
		// Besides the line break ending the line before the section and the
		// one ending the END marker's line, there was a blank line.
		return head + eol + eol + tail
	default:
		return head + eol + tail
	}
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
