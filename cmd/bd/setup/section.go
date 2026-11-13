package setup

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Section management for an instruction file a recipe shares with the user
// (recipes.Recipe.SharedPaths): bd owns a marked section inside the file,
// wrapped in the same BEGIN/END BEADS INTEGRATION markers AGENTS.md uses,
// never the whole file, so the user's own guidance survives install, --check
// and --remove.

// ErrSharedSectionMalformed is returned when a shared instruction file holds
// beads markers that cannot be owned safely: a dangling marker, a reversed
// pair, a nested or duplicated pair, or an incomplete BEGIN comment.
var ErrSharedSectionMalformed = errors.New("malformed beads section markers")

// SharedSectionKind classifies what bd owns in a shared instruction file.
type SharedSectionKind int

const (
	// SharedSectionNone: no beads markers; the file holds only the user's
	// content (or nothing).
	SharedSectionNone SharedSectionKind = iota
	// SharedSectionPresent: exactly one complete BEGIN marker comment and
	// one END marker, in that order.
	SharedSectionPresent
	// SharedSectionLegacy: no markers, and the file is exactly the frozen
	// template an earlier bd release wrote to the whole file.
	SharedSectionLegacy
	// SharedSectionMalformed: beads markers are present but cannot be owned
	// safely.
	SharedSectionMalformed
)

// ClassifySharedSection classifies content, the bytes of a shared
// instruction file, by what bd owns in it. A present section needs exactly
// one complete BEGIN comment (bare, or carrying metadata as AGENTS.md
// sections write it) and one END marker, in that order: anything else with
// markers in it is malformed, whose span belongs to no one bd can name. A
// file with no markers is the legacy template only when it equals legacy
// exactly, after CRLF-to-LF normalization and trailing-whitespace trim, so
// user additions keep it the user's.
func ClassifySharedSection(content, legacy string) SharedSectionKind {
	begins := strings.Count(content, beginMarkerPrefix)
	ends := strings.Count(content, agentsEndMarker)
	switch {
	case begins == 0 && ends == 0:
		if isLegacySharedFile(content, legacy) {
			return SharedSectionLegacy
		}
		return SharedSectionNone
	case begins != 1 || ends != 1:
		// A dangling marker, or a nested or duplicated pair.
		return SharedSectionMalformed
	}
	begin := strings.Index(content, beginMarkerPrefix)
	end := strings.Index(content, agentsEndMarker)
	if end < begin {
		return SharedSectionMalformed
	}
	if !completeBeginMarkerLine(content[begin:]) {
		return SharedSectionMalformed
	}
	return SharedSectionPresent
}

// beginMarkerPrefix matches the BEGIN marker bare and carrying metadata, as
// findBeginMarker does.
const beginMarkerPrefix = "<!-- BEGIN BEADS INTEGRATION"

// completeBeginMarkerLine reports whether rest, starting at a BEGIN marker
// prefix, holds a complete marker comment: the marker's own line closes with
// "-->", as the bare marker and one carrying metadata both do. One that
// never closes, or closes on a later line, is incomplete.
func completeBeginMarkerLine(rest string) bool {
	line := rest
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	return strings.HasSuffix(strings.TrimRight(line, " \t\r"), "-->")
}

// isLegacySharedFile reports whether content is exactly the frozen legacy
// template legacy: each is normalized CRLF to LF and trimmed of trailing
// whitespace, so a checkout's line endings do not change what is recognized.
func isLegacySharedFile(content, legacy string) bool {
	if legacy == "" {
		return false
	}
	normalize := func(s string) string {
		return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\n\r\v\f")
	}
	return normalize(content) == normalize(legacy)
}

// wrapSharedSection wraps body in the bare beads integration markers, each
// on its own line, ending in a newline.
func wrapSharedSection(body string) string {
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return agentsBeginMarker + "\n" + body + agentsEndMarker + "\n"
}

// upsertSharedSectionText returns content with body installed as its beads
// section. A missing or empty file takes the wrapped body alone; a legacy
// file is replaced by it, so the guidance is not duplicated; a file with
// other content keeps it as a prefix with the section appended; a present
// section is replaced in place, leaving the bytes outside its markers
// untouched. Malformed markers are an error.
func upsertSharedSectionText(content, body, legacy string) (string, error) {
	wrapped := wrapSharedSection(body)
	switch ClassifySharedSection(content, legacy) {
	case SharedSectionMalformed:
		return "", ErrSharedSectionMalformed
	case SharedSectionPresent:
		begin := findBeginMarker(content)
		end := strings.Index(content, agentsEndMarker) + len(agentsEndMarker)
		// The wrapper's final newline stays out of the replacement: the
		// bytes after the END marker, none when the section ends the file,
		// are the user's and keep their own line endings.
		return content[:begin] + strings.TrimSuffix(wrapped, "\n") + content[end:], nil
	case SharedSectionLegacy:
		return wrapped, nil
	default:
		if content == "" {
			return wrapped, nil
		}
		// The user's content stays a prefix; the section is appended set off
		// by a blank line, as addBeadsSection does for AGENTS.md.
		return addBeadsSection(content, wrapped, ""), nil
	}
}

// UpsertSharedSection reads the shared instruction file at path and writes
// it back with body installed as its beads section, through the atomic
// writer, which refuses a file symlink. A missing file is an empty one. A
// file already holding exactly the section install writes is left alone —
// bytes and timestamps — so reinstall is byte-stable. It reports whether it
// wrote the file.
func UpsertSharedSection(path, body, legacy string) (bool, error) {
	if err := RefuseSharedSymlink(path); err != nil {
		return false, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a bd setup recipe's own path
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	newContent, err := upsertSharedSectionText(content, body, legacy)
	if err != nil {
		return false, err
	}
	if newContent == content {
		return false, nil
	}
	if err := atomicWriteFile(path, []byte(newContent)); err != nil {
		return false, fmt.Errorf("write file: %w", err)
	}
	return true, nil
}

// RefuseSharedSymlink refuses a symbolic link at a shared instruction
// file's path, a dangling one included (Lstat tells those apart from a
// missing file): upserting or removing a section there would act on
// whatever the link leads to instead of a file in this repository. A
// read-only --check may still follow the link.
func RefuseSharedSymlink(path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("shared instruction file is a symbolic link: %s", path)
	}
	return nil
}

// RemoveSharedSection takes bd's section out of the shared instruction file
// at path. A file with no markers is left alone and reports nothing removed;
// a legacy whole-file install, and a file holding nothing but the section,
// are deleted (deleted true); otherwise the remainder — the user's text — is
// written back through the atomic writer, with the blank lines that set the
// section off taken with it, as removeBeadsSection does. Malformed markers
// are an error and change nothing. The caller drops the file's directory
// only when the file itself was deleted.
func RemoveSharedSection(path, legacy string) (removedSection, deleted bool, err error) {
	if err := RefuseSharedSymlink(path); err != nil {
		return false, false, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a bd setup recipe's own path
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	switch ClassifySharedSection(content, legacy) {
	case SharedSectionMalformed:
		return false, false, ErrSharedSectionMalformed
	case SharedSectionNone:
		return false, false, nil
	case SharedSectionLegacy:
		if err := os.Remove(path); err != nil {
			return false, false, fmt.Errorf("remove file: %w", err)
		}
		return true, true, nil
	default:
		remainder := removeBeadsSection(content)
		if strings.TrimSpace(remainder) == "" {
			if err := os.Remove(path); err != nil {
				return false, false, fmt.Errorf("remove file: %w", err)
			}
			return true, true, nil
		}
		if err := atomicWriteFile(path, []byte(remainder)); err != nil {
			return false, false, fmt.Errorf("write file: %w", err)
		}
		return true, false, nil
	}
}
