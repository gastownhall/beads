package setup

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// A shared file is one a recipe writes into but the repository owns too,
// such as .github/copilot-instructions.md (recipes.Recipe.SharedPaths). bd
// keeps its guidance there as one section between the beads integration
// markers that AGENTS.md uses, and leaves the rest of the file alone.

const (
	sharedBeginPrefix = "<!-- BEGIN BEADS INTEGRATION"
	sharedEndPrefix   = "<!-- END BEADS INTEGRATION"
)

// ErrMalformedSection is the error, wrapped with what is wrong, for a
// shared file whose beads markers do not make exactly one section.
var ErrMalformedSection = errors.New("malformed beads integration markers")

// SharedSectionState is what a shared file holds of bd's.
type SharedSectionState int

const (
	// SharedSectionNone: no beads markers.
	SharedSectionNone SharedSectionState = iota
	// SharedSectionPresent: exactly one complete beads section.
	SharedSectionPresent
	// SharedSectionMalformed: beads markers that do not make one section.
	SharedSectionMalformed
)

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
// one END comment after it; any other use of the markers is malformed.
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
	return start, endAt + len(agentsEndMarker), SharedSectionPresent, nil
}

// UpsertSharedSection returns content with body as its beads section. A
// file with no beads markers gets the section appended, set off by a blank
// line as AGENTS.md's is, its own bytes kept as they are; an empty one
// gets just the section. Malformed markers are an error, content unchanged.
func UpsertSharedSection(content, body string) (string, error) {
	wrapped := wrapSharedSection(body)
	_, _, state, err := sharedSectionSpan(content)
	switch {
	case err != nil:
		return content, err
	case state == SharedSectionPresent:
		return content, nil
	case content == "":
		return wrapped, nil
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
func InstallSharedFile(path, body string) (bool, error) {
	if err := refuseSharedSymlink(path); err != nil {
		return false, err
	}
	content, exists, err := readSharedFile(path)
	if err != nil {
		return false, err
	}
	updated, err := UpsertSharedSection(content, body)
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
