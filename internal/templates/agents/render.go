package agents

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
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

// MarkerVersion is the current format version for BEGIN BEADS INTEGRATION markers.
// Bump this when the marker format itself changes (not when template content changes).
const MarkerVersion = 1

var (
	// ErrNoSection is returned by ReplaceSection when no BEGIN marker exists.
	ErrNoSection = errors.New("no beads section markers found")
	// ErrMalformedMarkers is returned when markers exist but are invalid
	// (e.g., END before BEGIN, or BEGIN without END).
	ErrMalformedMarkers = errors.New("malformed beads section markers")
)

//go:embed defaults/beads-section-minimal.md
var beadsSectionMinimal string

// SectionMeta holds metadata parsed from a BEGIN BEADS INTEGRATION marker.
type SectionMeta struct {
	Version int
	Profile Profile
	Hash    string
}

// RenderSection returns the beads integration section for the given profile,
// wrapped in markers that include version, profile, and hash metadata for freshness detection.
func RenderSection(profile Profile) string {
	body := templateBody(profile)
	hash := computeHash(body)
	beginMarker := fmt.Sprintf("<!-- BEGIN BEADS INTEGRATION v:%d profile:%s hash:%s -->", MarkerVersion, profile, hash)
	return beginMarker + "\n" + body + "\n<!-- END BEADS INTEGRATION -->\n"
}

// ReplaceSection replaces an existing beads integration section in content with a
// freshly rendered section for the given profile. Returns the (possibly unchanged)
// content, whether it was modified, and any error.
//
// Errors:
//   - ErrNoSection: no BEGIN marker found (caller should append instead)
//   - ErrMalformedMarkers: BEGIN exists but END is missing or appears before BEGIN
func ReplaceSection(content string, profile Profile) (string, bool, error) {
	beginIdx := strings.Index(content, "<!-- BEGIN BEADS INTEGRATION")
	if beginIdx == -1 {
		return content, false, ErrNoSection
	}

	endMarker := "<!-- END BEADS INTEGRATION -->"
	endIdx := strings.Index(content, endMarker)
	if endIdx == -1 {
		return "", false, fmt.Errorf("%w: BEGIN marker at offset %d but no END marker", ErrMalformedMarkers, beginIdx)
	}
	if endIdx < beginIdx {
		return "", false, fmt.Errorf("%w: END marker at offset %d before BEGIN at %d", ErrMalformedMarkers, endIdx, beginIdx)
	}

	// Check if already current (hash freshness)
	firstLine := content[beginIdx:]
	if nl := strings.Index(firstLine, "\n"); nl != -1 {
		firstLine = firstLine[:nl]
	}
	meta := ParseMarker(firstLine)
	if meta != nil && meta.Hash == CurrentHash(profile) && meta.Profile == profile {
		return content, false, nil // already up to date
	}

	// Replace section: consume exactly one trailing newline after END marker
	endOfEndMarker := endIdx + len(endMarker)
	if endOfEndMarker < len(content) && content[endOfEndMarker] == '\n' {
		endOfEndMarker++
	}

	replaced := content[:beginIdx] + RenderSection(profile) + content[endOfEndMarker:]
	return replaced, true, nil
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
		case "v":
			if n, err := strconv.Atoi(v); err == nil {
				meta.Version = n
			}
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
