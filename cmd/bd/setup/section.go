package setup

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// A shared file is one a recipe writes into but the repository owns too,
// such as .github/copilot-instructions.md (recipes.Recipe.SharedPaths). bd
// keeps its guidance there as one section between the beads integration
// markers that AGENTS.md uses, and leaves the rest of the file alone.

const (
	sharedBeginPrefix = "<!-- BEGIN BEADS INTEGRATION"
	sharedEndPrefix   = "<!-- END BEADS INTEGRATION"
)

// utf8BOM is the byte order mark some editors save at the start of a UTF-8
// file. It is not the file's text: a marker right after it starts the first
// line, and bd keeps it where it is.
const utf8BOM = "\ufeff"

// splitBOM splits a leading byte order mark (utf8BOM) off content.
func splitBOM(content string) (bom, text string) {
	if strings.HasPrefix(content, utf8BOM) {
		return utf8BOM, content[len(utf8BOM):]
	}
	return "", content
}

// ErrMalformedSection is the error, wrapped with what is wrong, for a
// shared file whose beads markers do not make exactly one section.
var ErrMalformedSection = errors.New("malformed beads integration markers")

// ErrRefuseSymlinkWrite is the error for a shared file that is a symbolic
// link, which install and remove do not write or delete through.
var ErrRefuseSymlinkWrite = errRefuseSymlinkWrite

// SharedSectionState is what a shared file holds of bd's.
type SharedSectionState int

const (
	// SharedSectionNone: no beads markers.
	SharedSectionNone SharedSectionState = iota
	// SharedSectionPresent: exactly one complete beads section.
	SharedSectionPresent
	// SharedSectionMalformed: beads markers that do not make one section.
	SharedSectionMalformed
	// SharedSectionLegacy: no markers, and the whole file is the template an
	// older bd wrote unmarked (recipes.Recipe.LegacyContents).
	SharedSectionLegacy
)

// isLegacySharedFile reports whether content is exactly legacy, the whole
// file an older bd wrote, but for a leading byte order mark, CRLF line
// breaks and trailing whitespace. A file holding legacy and anything else is
// the user's.
func isLegacySharedFile(content, legacy string) bool {
	normalize := func(s string) string {
		_, s = splitBOM(s)
		return strings.TrimRightFunc(strings.ReplaceAll(s, "\r\n", "\n"), unicode.IsSpace)
	}
	return legacy != "" && normalize(content) == normalize(legacy)
}

// classifySharedSection is sharedSectionSpan, with a marker-free file that
// is exactly legacy classified as SharedSectionLegacy.
func classifySharedSection(content, legacy string) (start, end int, state SharedSectionState, err error) {
	start, end, state, err = sharedSectionSpan(content)
	if state == SharedSectionNone && isLegacySharedFile(content, legacy) {
		state = SharedSectionLegacy
	}
	return start, end, state, err
}

// wrapSharedSection puts body between the beads markers, each on its own
// line, ending in a line break.
func wrapSharedSection(body string) string {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return agentsBeginMarker + "\n" + body + agentsEndMarker + "\n"
}

// sharedSectionSpan classifies content and, for a present section, returns
// where it starts (its BEGIN comment) and ends (just past its END comment).
// A section is present only when content holds one BEGIN comment, bare or
// with the metadata bd writes into AGENTS.md, closed on its own line, and
// one END comment after it, each alone on its line (markerAloneOnLine), as
// bd writes them. Any other use of the markers is malformed, so a mention
// of them in the file's own text, inline or indented, is never taken for
// bd's section.
func sharedSectionSpan(content string) (start, end int, state SharedSectionState, err error) {
	begins := strings.Count(content, sharedBeginPrefix)
	ends := strings.Count(content, sharedEndPrefix)
	if begins == 0 && ends == 0 {
		return 0, 0, SharedSectionNone, nil
	}
	malformed := func(format string, args ...any) (int, int, SharedSectionState, error) {
		return 0, 0, SharedSectionMalformed, fmt.Errorf("%w: %s", ErrMalformedSection, fmt.Sprintf(format, args...))
	}
	if begins != 1 || ends != 1 {
		return malformed("found %d BEGIN and %d END markers, want one of each", begins, ends)
	}
	start = strings.Index(content, sharedBeginPrefix)
	endAt := strings.Index(content, agentsEndMarker)
	if endAt < 0 {
		return malformed("the END marker is not %s", agentsEndMarker)
	}
	if endAt < start {
		return malformed("the END marker comes before the BEGIN marker")
	}
	rest := content[start+len(sharedBeginPrefix):]
	closeAt := strings.Index(rest, "-->")
	if !strings.HasPrefix(rest, " ") || closeAt < 0 || strings.ContainsAny(rest[:closeAt], "\r\n") || start+len(sharedBeginPrefix)+closeAt > endAt {
		return malformed("the BEGIN marker is not one complete comment on its line")
	}
	end = endAt + len(agentsEndMarker)
	if !markerAloneOnLine(content, start, start+len(sharedBeginPrefix)+closeAt+len("-->")) {
		return malformed("the BEGIN marker is not alone on its line")
	}
	if !markerAloneOnLine(content, endAt, end) {
		return malformed("the END marker is not alone on its line")
	}
	return start, end, SharedSectionPresent, nil
}

// markerAloneOnLine reports whether the marker content[from:to] is alone on
// its line: it starts the line (the first one, behind a byte order mark
// too), and nothing but spaces, tabs or CRs follows it before the next LF or
// the end of content.
func markerAloneOnLine(content string, from, to int) bool {
	if from > 0 && content[from-1] != '\n' && content[:from] != utf8BOM {
		return false
	}
	line, _, _ := strings.Cut(content[to:], "\n")
	return strings.Trim(line, " \t\r") == ""
}

// UpsertSharedSection returns content with body as its beads section. An
// existing section is replaced in place, from its BEGIN marker to its END
// marker, the bytes before and after it untouched, so running it again
// changes nothing. A file with no beads markers gets the section appended,
// set off by a blank line as AGENTS.md's is, its own bytes kept as they
// are; an empty one, or one that is exactly legacy, gets just the section,
// behind the byte order mark it starts with, if any. Malformed markers are
// an error, content unchanged.
func UpsertSharedSection(content, body, legacy string) (string, error) {
	wrapped := wrapSharedSection(body)
	start, end, state, err := classifySharedSection(content, legacy)
	bom, text := splitBOM(content)
	switch {
	case err != nil:
		return content, err
	case state == SharedSectionLegacy:
		return bom + wrapped, nil
	case state == SharedSectionPresent:
		// The line break that ends the END marker's line stays in content.
		return content[:start] + strings.TrimSuffix(wrapped, "\n") + content[end:], nil
	case text == "":
		return bom + wrapped, nil
	default:
		return addBeadsSection(content, wrapped, ""), nil
	}
}

// refuseSharedSymlink refuses a shared file that is a symbolic link, a
// dangling one included: bd does not write or delete through it.
func refuseSharedSymlink(path string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errRefuseSymlinkWrite
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// readSharedFile returns path's content, and "" for a missing file.
func readSharedFile(path string) (string, bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a built-in recipe's path
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// InstallSharedFile puts body into path as its beads section
// (UpsertSharedSection) and reports whether it wrote the file. It writes
// only when the content changes, atomically, keeping the file's mode.
func InstallSharedFile(path, body, legacy string) (bool, error) {
	if err := refuseSharedSymlink(path); err != nil {
		return false, err
	}
	content, exists, err := readSharedFile(path)
	if err != nil {
		return false, err
	}
	updated, err := UpsertSharedSection(content, body, legacy)
	if err != nil {
		return false, err
	}
	if exists && updated == content {
		return false, nil
	}
	if err := atomicWriteFile(path, []byte(updated)); err != nil {
		return false, err
	}
	return true, nil
}

// CheckSharedFile reads path, following a link since it only reads, and
// returns what it holds of bd's. A missing file is the os.ReadFile error;
// malformed markers return SharedSectionMalformed and an
// ErrMalformedSection error. A file that is exactly legacy is bd's.
func CheckSharedFile(path, legacy string) (SharedSectionState, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a built-in recipe's path
	if err != nil {
		return SharedSectionNone, err
	}
	_, _, state, err := classifySharedSection(string(data), legacy)
	return state, err
}

// StripSharedSection returns content without its beads section, removed by
// removeBeadsSection, which drops the line breaks that set the section off
// and rejoins the text around it; a byte order mark the file starts with
// stays. stripped reports a section was removed, and empty that nothing but
// whitespace is left, so the file is bd's alone. A file that is exactly
// legacy is bd's alone too. Malformed markers are an error, content
// unchanged.
func StripSharedSection(content, legacy string) (remainder string, stripped, empty bool, err error) {
	_, end, state, err := classifySharedSection(content, legacy)
	switch {
	case state == SharedSectionLegacy:
		return "", true, true, nil
	case err != nil || state != SharedSectionPresent:
		return content, false, false, err
	}
	// Nothing but spaces, tabs or CRs can follow the END marker on its line
	// (markerAloneOnLine). They go with the section, so removeBeadsSection
	// sees the marker end its line and rejoins the text around it.
	line, _, _ := strings.Cut(content[end:], "\n")
	bom, text := splitBOM(content[:end] + content[end+len(line):])
	text = removeBeadsSection(text)
	return bom + text, true, strings.TrimSpace(text) == "", nil
}

// SharedRemoval is what RemoveSharedFile did.
type SharedRemoval int

const (
	// SharedRemovedNothing: the file was missing or held no section.
	SharedRemovedNothing SharedRemoval = iota
	// SharedRemovedSection: the section went; the rest of the file stays.
	SharedRemovedSection
	// SharedRemovedFile: the file held nothing else, and was deleted.
	SharedRemovedFile
)

// RemoveSharedFile takes bd's section out of path (StripSharedSection),
// deleting the file only when nothing but whitespace (or a byte order mark)
// is left, or when it is exactly legacy.
func RemoveSharedFile(path, legacy string) (SharedRemoval, error) {
	if err := refuseSharedSymlink(path); err != nil {
		return SharedRemovedNothing, err
	}
	content, exists, err := readSharedFile(path)
	if err != nil || !exists {
		return SharedRemovedNothing, err
	}
	remainder, stripped, empty, err := StripSharedSection(content, legacy)
	switch {
	case err != nil:
		return SharedRemovedNothing, err
	case !stripped:
		return SharedRemovedNothing, nil
	case empty:
		if err := os.Remove(path); err != nil {
			return SharedRemovedNothing, err
		}
		return SharedRemovedFile, nil
	}
	if err := atomicWriteFile(path, []byte(remainder)); err != nil {
		return SharedRemovedNothing, err
	}
	return SharedRemovedSection, nil
}

// PreflightSharedFile reports, by a nil error, that install and remove can
// work on path: it is not a symbolic link (a dangling one included), it is
// missing or readable, and its beads markers are not malformed. A recipe
// runs it on every shared file before changing any of its files, so a
// refusal leaves them all as they were.
func PreflightSharedFile(path string) error {
	if err := refuseSharedSymlink(path); err != nil {
		return err
	}
	content, _, err := readSharedFile(path)
	if err != nil {
		return err
	}
	_, _, _, err = sharedSectionSpan(content)
	return err
}
