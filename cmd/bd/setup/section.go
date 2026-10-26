package setup

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrUnbalancedSection reports a file that carries a beads BEGIN or END marker
// without its partner. Refusing beats guessing a range: a stray BEGIN followed
// by a later END would otherwise make every later run and every --remove treat
// the text between them as the section and drop it.
var ErrUnbalancedSection = errors.New("beads section markers are unbalanced")

// ManagedSection wraps body in the beads integration markers so it can be
// inserted into - and later replaced inside - a file the user also owns.
func ManagedSection(body string) string {
	return agentsBeginMarker + "\n" + strings.TrimRight(body, "\n") + "\n" + agentsEndMarker + "\n"
}

// ContainsManagedSection reports whether content already carries a beads section.
func ContainsManagedSection(content string) bool {
	return containsBeadsMarker(content)
}

// UpsertManagedSection returns content with its beads section set to body.
// A file that does not have a section yet gets one appended, so unrelated
// user-authored content is preserved - unless it is byte-for-byte the unmarked
// template a previous version wrote, which is beads' own content and is
// migrated to the section in place. The bool reports whether an existing
// section was replaced (as opposed to one being added).
//
// Content holding a BEGIN marker with no END (or the reverse) is refused with
// ErrUnbalancedSection: appending would supply the missing marker and the next
// run, or --remove, would then span from the stray marker to it and delete
// whatever the user wrote in between.
func UpsertManagedSection(content, body string) (string, bool, error) {
	section := ManagedSection(body)
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)
	if !containsBeadsMarker(content) && end != -1 {
		// An END without a BEGIN is just as unbalanced as the reverse.
		return "", false, fmt.Errorf("%w: found an END marker without a matching BEGIN - fix or remove the marker by hand", ErrUnbalancedSection)
	}
	if !containsBeadsMarker(content) {
		if strings.TrimSpace(content) == "" {
			return section, false, nil
		}
		// Legacy upgrade: before beads managed a section, setup wrote the
		// plain template over the file. That content is beads', not the
		// user's, so replace it instead of appending the guidance twice.
		if isLegacyTemplate(content, body) {
			return section, true, nil
		}
		return strings.TrimRight(content, "\n") + "\n\n" + section, false, nil
	}

	if start == -1 || end == -1 || start > end {
		return "", false, fmt.Errorf("%w: found a BEGIN marker without a matching END (or the reverse) - fix or remove the marker by hand", ErrUnbalancedSection)
	}

	after := end + len(agentsEndMarker)
	switch {
	case after+1 < len(content) && content[after] == '\r' && content[after+1] == '\n':
		after += 2
	case after < len(content) && (content[after] == '\r' || content[after] == '\n'):
		after++
	}
	return content[:start] + section + content[after:], true, nil
}

// isLegacyTemplate reports whether content is exactly the body a previous
// version of setup wrote as the whole file, ignoring trailing whitespace. The
// file is then beads' own output rather than user content, so it can be
// migrated to the managed section instead of preserved alongside it.
func isLegacyTemplate(content, body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	return strings.TrimRight(content, " \t\r\n") == strings.TrimRight(body, " \t\r\n")
}

// RemoveManagedSection drops the beads section from content. The bool reports
// whether anything other than whitespace is left, so callers can tell a
// beads-only file (safe to delete) from a shared one. Unbalanced markers are
// left alone (and reported as ErrUnbalancedSection) rather than guessed at, so
// a stray BEGIN cannot make removal eat the text up to some later END.
func RemoveManagedSection(content string) (string, bool, error) {
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)
	if start != -1 || end != -1 {
		if start == -1 || end == -1 || start > end {
			return "", false, fmt.Errorf("%w: refusing to remove a section between an unbalanced marker pair", ErrUnbalancedSection)
		}
	}
	remaining := removeBeadsSection(content)
	return remaining, strings.TrimSpace(remaining) != "", nil
}

// SectionAction describes what an InstallManagedSectionFile call did.
type SectionAction string

const (
	// SectionCreated means the file did not exist (or was empty) and now holds
	// only the beads section.
	SectionCreated SectionAction = "created"
	// SectionAdded means a beads section was appended to a user-authored file.
	SectionAdded SectionAction = "added"
	// SectionUpdated means an existing beads section was replaced in place.
	SectionUpdated SectionAction = "updated"
)

// InstallManagedSectionFile writes body as the beads section of path, leaving
// the rest of an existing file untouched.
func InstallManagedSectionFile(path, body string) (SectionAction, error) {
	content := ""
	if data, err := os.ReadFile(path); err == nil { // #nosec G304 -- setup destination is trusted
		content = string(data)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	wasEmpty := strings.TrimSpace(content) == ""
	updated, replaced, err := UpsertManagedSection(content, body)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := atomicWriteFile(path, []byte(updated)); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	switch {
	case replaced:
		return SectionUpdated, nil
	case wasEmpty:
		return SectionCreated, nil
	default:
		return SectionAdded, nil
	}
}

// WriteManagedSectionFile writes content back to path through the same atomic,
// symlink-refusing path InstallManagedSectionFile uses, so `--remove` and
// install agree on what a writable destination is.
func WriteManagedSectionFile(path, content string) error {
	return atomicWriteFile(path, []byte(content), 0o644)
}
