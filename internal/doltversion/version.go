// Package doltversion owns discovery, hardened probing, and version parsing
// for the external `dolt` CLI executable that managed proxied-server mode
// spawns. It exists as a stdlib-only leaf package (no dependency on
// internal/doltserver or internal/config) so it can be imported early — from
// preflight checks that must run before any beads state is written — without
// dragging in the rest of the storage stack.
//
// The package intentionally separates three concerns that used to be
// entangled at call sites: *where* the dolt binary comes from (Resolve),
// *whether it actually works* (Probe / ProbeWithPolicy), and *what version it
// claims to be* (ParseVersion). Splitting them lets each be tested and
// reasoned about independently, and lets callers report precisely which step
// failed instead of a single opaque "dolt not usable" error.
package doltversion

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a comparable representation of a `dolt version` version string,
// e.g. "1.52.3" or "2.0.0-beta1". Segments holds the dotted numeric
// components in order (major, minor, patch, ...); Prerelease holds any
// suffix after a '-' (e.g. "beta1" or "dev"), preserved verbatim but ignored
// by AtLeast — a prerelease build of dolt 2.0.0 should still satisfy a >=
// 2.0.0 recommendation, since prerelease/dev builds are usually *newer* than
// the tagged version they're built from, not older. Raw retains the original
// input for error messages and display.
type Version struct {
	Segments   []int
	Prerelease string
	Raw        string
}

// ParseVersion extracts and generalizes the version-parsing logic that
// previously lived only in internal/doltserver/gc_config.go
// (doltVersionAtLeast): it reads the first line of `dolt version` output
// (subsequent lines, such as "database storage format: ..." printed when run
// inside a Dolt repo, are ignored), takes the last whitespace-separated
// field of that line as the version token, and parses it as a dotted
// sequence of non-negative integers with an optional "-<prerelease>" suffix.
//
// This is deliberately more permissive than a strict semver parser: real
// `dolt version` output has taken forms like "dolt version 1.52.3" and (for
// dev builds) "dolt version 1.52.3-dev-abcdef", and the goal here is to
// extract a usable, comparable version from whatever dolt prints rather than
// to validate that dolt's own output is well-formed semver.
//
// A consequence worth naming: any first line whose last field parses as a
// dotted integer sequence passes — "dolt build 2023" yields version "2023",
// which sails over RecommendedMin. Acceptable while the consumer is
// warn-only policy; if a later change makes this the parser behind a hard
// enforcement floor (the part-2 revalidation contract), tighten it to
// require at least two dotted segments or an explicit "version" marker
// before the token, so a stray trailing integer cannot satisfy a gate.
func ParseVersion(output string) (Version, error) {
	firstLine := output
	if idx := strings.IndexByte(output, '\n'); idx >= 0 {
		firstLine = output[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)

	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return Version{}, fmt.Errorf("%w: empty version output", ErrUnparseableVersion)
	}
	return parseToken(fields[len(fields)-1])
}

// parseToken parses a single version token (e.g. "1.52.3" or
// "2.0.0-beta1") into a Version, with no notion of "first line" or
// "whitespace-separated fields" — that framing belongs to ParseVersion,
// which extracts a token from raw `dolt version` output before calling
// this. It is factored out so MustParse can validate a literal version
// string directly, without going through ParseVersion's
// output-line-scraping (which would otherwise require MustParse to
// synthesize fake surrounding "dolt version " text just to satisfy
// ParseVersion's parsing shape, and would silently accept garbage like
// "a b 1.2" by treating everything before the last field as ignorable
// prose).
func parseToken(token string) (Version, error) {
	main, prerelease, _ := strings.Cut(token, "-")
	if main == "" {
		return Version{}, fmt.Errorf("%w: %q", ErrUnparseableVersion, token)
	}

	parts := strings.Split(main, ".")
	segments := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%w: %q", ErrUnparseableVersion, token)
		}
		segments = append(segments, n)
	}

	return Version{Segments: segments, Prerelease: prerelease, Raw: token}, nil
}

// MustParse parses s (a bare version token, e.g. "2.0.0" — not full `dolt
// version` command output) and panics on error. It exists for declaring
// policy constants (see RecommendedMin) where the input is a hardcoded
// literal and a parse failure would be a programming error caught
// immediately by any test or use of the package, not a runtime condition
// to handle gracefully. It calls parseToken directly rather than
// ParseVersion so a malformed literal like "a b 1.2" is rejected outright
// instead of ParseVersion's output-scraping treating "a b" as ignorable
// leading prose and silently accepting the trailing "1.2".
func MustParse(s string) Version {
	v, err := parseToken(s)
	if err != nil {
		panic(fmt.Sprintf("doltversion: MustParse(%q): %v", s, err))
	}
	return v
}

// AtLeast reports whether v is greater than or equal to other, comparing
// Segments numerically position by position (a missing trailing segment is
// treated as 0, so "1.52" >= "1.52.0"). Prerelease/build suffixes are
// ignored for this comparison: policy floors in this package (see
// RecommendedMin) are about the underlying storage-format-relevant release,
// and a "-dev" or "-beta1" build of that release or later should not be
// treated as failing the floor just because it isn't a clean tagged release.
func (v Version) AtLeast(other Version) bool {
	n := len(v.Segments)
	if len(other.Segments) > n {
		n = len(other.Segments)
	}
	for i := 0; i < n; i++ {
		var a, b int
		if i < len(v.Segments) {
			a = v.Segments[i]
		}
		if i < len(other.Segments) {
			b = other.Segments[i]
		}
		if a != b {
			return a > b
		}
	}
	return true
}

// String renders v back into a dotted-segment form with the prerelease
// suffix reattached (e.g. "1.52.3-dev"), for use in error and warning
// messages. It is reconstructed from Segments/Prerelease rather than
// returning Raw so that a Version built via MustParse (which has no Raw
// beyond what MustParse synthesizes) prints consistently.
func (v Version) String() string {
	parts := make([]string, len(v.Segments))
	for i, s := range v.Segments {
		parts[i] = strconv.Itoa(s)
	}
	s := strings.Join(parts, ".")
	if v.Prerelease != "" {
		s += "-" + v.Prerelease
	}
	return s
}
