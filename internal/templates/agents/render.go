package agents

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
)

// Profile identifies which template variant to render.
type Profile string

const (
	// ProfileFull is the command-heavy profile for hookless agents (Codex, Factory, Mux, etc.).
	ProfileFull Profile = "full"
	// ProfileMinimal is the pointer-only profile for hook-enabled agents (Claude, Gemini).
	ProfileMinimal Profile = "minimal"
)

//go:embed defaults/beads-section-minimal.md
var beadsSectionMinimal string

// SectionMeta holds metadata parsed from a BEGIN BEADS INTEGRATION marker.
type SectionMeta struct {
	Profile Profile
	Hash    string
}

// RenderSection returns the beads integration section for the given profile,
// wrapped in markers that include profile and hash metadata for freshness detection.
func RenderSection(profile Profile) string {
	body := templateBody(profile)
	hash := computeHash(body)
	beginMarker := fmt.Sprintf("<!-- BEGIN BEADS INTEGRATION profile:%s hash:%s -->", profile, hash)
	return beginMarker + "\n" + body + "\n<!-- END BEADS INTEGRATION -->\n"
}

// CurrentHash returns the hash of the current template body for a profile.
// Callers can compare this against a parsed marker's hash to detect staleness.
func CurrentHash(profile Profile) string {
	return computeHash(templateBody(profile))
}

// ParseMarker parses a BEGIN BEADS INTEGRATION marker line and returns its metadata.
// Returns nil if the line is not a valid begin marker.
// Supports both legacy (no metadata) and new (profile + hash) formats.
func ParseMarker(line string) *SectionMeta {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "<!-- BEGIN BEADS INTEGRATION") {
		return nil
	}

	meta := &SectionMeta{}

	// Extract the content between "<!-- BEGIN BEADS INTEGRATION" and "-->"
	inner := strings.TrimPrefix(line, "<!-- BEGIN BEADS INTEGRATION")
	inner = strings.TrimSuffix(inner, "-->")
	inner = strings.TrimSpace(inner)

	if inner == "" {
		// Legacy format: <!-- BEGIN BEADS INTEGRATION -->
		return meta
	}

	// Parse key:value pairs
	for _, part := range strings.Fields(inner) {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		switch k {
		case "profile":
			meta.Profile = Profile(v)
		case "hash":
			meta.Hash = v
		}
	}

	return meta
}

// templateBody returns the raw body content (without markers) for a profile.
func templateBody(profile Profile) string {
	switch profile {
	case ProfileMinimal:
		return strings.TrimRight(beadsSectionMinimal, "\n")
	default:
		// Full profile uses the same body as the legacy beads-section.md
		// Strip the existing markers from the embedded content
		body := strings.TrimRight(beadsSection, "\n")
		body = strings.TrimPrefix(body, "<!-- BEGIN BEADS INTEGRATION -->\n")
		body = strings.TrimSuffix(body, "\n<!-- END BEADS INTEGRATION -->")
		return body
	}
}

// computeHash returns the first 8 hex chars of the SHA-256 of the body.
func computeHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return fmt.Sprintf("%x", h[:4])
}
