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

//go:embed defaults/beads-section-codex.md
var beadsSectionCodex string

// SectionMeta holds metadata parsed from a BEGIN BEADS INTEGRATION marker.
type SectionMeta struct {
	Version int
	Profile Profile
	Hash    string
}

// RenderOpts controls conditional content in rendered templates.
type RenderOpts struct {
	// HasRemote indicates whether a Dolt remote is configured.
	// When false, "bd dolt push" is omitted from session-completion instructions.
	HasRemote bool
}

// DefaultRenderOpts returns opts that assume a remote is configured,
// preserving backward-compatible behavior for callers that don't pass opts.
func DefaultRenderOpts() RenderOpts {
	return RenderOpts{HasRemote: true}
}

// RenderSection returns the beads integration section for the given profile,
// wrapped in markers that include version, profile, and hash metadata for freshness detection.
// Assumes a Dolt remote is configured (backward-compatible). Use RenderSectionWithOpts
// to control conditional content.
func RenderSection(profile Profile) string {
	return RenderSectionWithOpts(profile, DefaultRenderOpts())
}

// RenderSectionWithOpts renders the beads integration section with conditional content
// controlled by opts. When opts.HasRemote is false, "bd dolt push" is omitted from
// session-completion instructions.
func RenderSectionWithOpts(profile Profile, opts RenderOpts) string {
	body := templateBodyWithOpts(profile, opts)
	hash := computeHash(body)
	beginMarker := fmt.Sprintf("<!-- BEGIN BEADS INTEGRATION v:%d profile:%s hash:%s -->", MarkerVersion, profile, hash)
	return beginMarker + "\n" + body + "\n<!-- END BEADS INTEGRATION -->\n"
}

// CodexSectionBody returns the setup-managed Codex guidance body without
// Codex-specific markers.
func CodexSectionBody() string {
	return normalizeEmbeddedMarkdown(beadsSectionCodex)
}

// ReplaceSection replaces an existing beads integration section in content with a
// freshly rendered section for the given profile. Returns the (possibly unchanged)
// content, whether it was modified, and any error.
// Assumes a Dolt remote is configured (backward-compatible). Use ReplaceSectionWithOpts
// to control conditional content.
//
// Errors:
//   - ErrNoSection: no BEGIN marker found (caller should append instead)
//   - ErrMalformedMarkers: BEGIN exists but END is missing or appears before BEGIN
func ReplaceSection(content string, profile Profile) (string, bool, error) {
	return ReplaceSectionWithOpts(content, profile, DefaultRenderOpts())
}

// ReplaceSectionWithOpts replaces an existing beads integration section in content with a
// freshly rendered section for the given profile and opts.
func ReplaceSectionWithOpts(content string, profile Profile, opts RenderOpts) (string, bool, error) {
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
	if meta != nil && meta.Hash == CurrentHashWithOpts(profile, opts) && meta.Profile == profile {
		return content, false, nil // already up to date
	}

	// Replace section: consume exactly one trailing newline after END marker
	endOfEndMarker := endIdx + len(endMarker)
	if endOfEndMarker < len(content) && content[endOfEndMarker] == '\n' {
		endOfEndMarker++
	}

	replaced := content[:beginIdx] + RenderSectionWithOpts(profile, opts) + content[endOfEndMarker:]
	return replaced, true, nil
}

// CurrentHash returns the hash of the current template body for a profile.
// Callers can compare this against a parsed marker's hash to detect staleness.
// Assumes a Dolt remote is configured (backward-compatible).
func CurrentHash(profile Profile) string {
	return CurrentHashWithOpts(profile, DefaultRenderOpts())
}

// CurrentHashWithOpts returns the hash for a profile with the given render opts.
func CurrentHashWithOpts(profile Profile, opts RenderOpts) string {
	return computeHash(templateBodyWithOpts(profile, opts))
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
// Backward-compatible wrapper that assumes a Dolt remote is configured.
func templateBody(profile Profile) string {
	return templateBodyWithOpts(profile, DefaultRenderOpts())
}

// templateBodyWithOpts returns the raw body content (without markers) for a
// profile, with conditional content controlled by opts.
func templateBodyWithOpts(profile Profile, opts RenderOpts) string {
	var body string
	switch profile {
	case ProfileMinimal:
		body = normalizeEmbeddedMarkdown(beadsSectionMinimal)
	default:
		// Full profile uses the same body as the legacy beads-section.md
		// Strip the existing markers from the embedded content. Normalize CRLF→LF
		// first so a Windows-checkout build (where //go:embed picks up CRLF bytes
		// from the working tree) still matches the LF-only prefix/suffix below.
		// Without this, the legacy markers stay in the body and RenderSection
		// wraps them again, producing doubled markers in the installed file (#3552).
		body = strings.ReplaceAll(beadsSection, "\r\n", "\n")
		body = strings.TrimRight(body, "\n")
		body = strings.TrimPrefix(body, "<!-- BEGIN BEADS INTEGRATION -->\n")
		body = strings.TrimSuffix(body, "\n<!-- END BEADS INTEGRATION -->")
	}

	if !opts.HasRemote {
		body = stripDoltPushReferences(body)
	}

	return body
}

func normalizeEmbeddedMarkdown(content string) string {
	return strings.TrimRight(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

// stripDoltPushReferences removes "bd dolt push" directives from the template body.
// Strips the indented code-block line from session completion, and the
// informational Auto-Sync bullet that references dolt push/pull.
func stripDoltPushReferences(body string) string {
	// Session completion code block: "   bd dolt push\n"
	body = strings.ReplaceAll(body, "   bd dolt push\n", "")
	// Auto-Sync informational bullet (full profile only)
	body = strings.ReplaceAll(body, "- Use `bd dolt push`/`bd dolt pull` for remote sync\n", "")
	return body
}

// computeHash returns the first 8 hex chars of the SHA-256 of the body.
func computeHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return fmt.Sprintf("%x", h[:4])
}
