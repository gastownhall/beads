//go:build linux

package git

import (
	"bytes"
	"os"
	"regexp"
	"strings"
)

// gitConfigEntry is one key line of a git config file, as scanGitConfig
// understood it.
type gitConfigEntry struct {
	section    string  // lower-cased section name
	subsection *string // nil for [section]; case preserved
	key        string  // lower-cased variable name
	hasValue   bool    // false for a bare "key" line (boolean true)
	rawValue   string  // value text after '=', as written, comment included
	value      string  // rawValue without its comment, trimmed and unquoted
}

var (
	gitConfigHeader    = regexp.MustCompile(`^\[([A-Za-z0-9-]+)(?: "([^"\\]*)")?\][ \t]*(?:[#;].*)?$`)
	gitConfigKeyLine   = regexp.MustCompile(`^[ \t]*([A-Za-z][A-Za-z0-9-]*)[ \t]*(?:=(.*)|(?:[#;].*)?)$`)
	gitConfigCommentLn = regexp.MustCompile(`^[ \t]*(?:[#;].*)?$`)
)

// scanGitConfig reads a git config file with a deliberately narrow grammar
// and calls visit for each key line; it returns false (the caller then asks
// git) when the file is unreadable, a visit returns false, or ANY line falls
// outside that grammar — Git might honor such a line differently or refuse
// the file. A missing file is an empty config, as for Git.
//
// Understood: a UTF-8 BOM at the start of the file (Git skips it), CRLF line
// endings, blank and comment lines, "[section]" and `[section "sub"]`
// headers at the start of a line (optionally followed by a comment), and
// "key", "key = value" lines inside a section. Values containing a backslash
// or an odd number of double quotes (escapes, continuations, unterminated
// strings) are not understood.
func scanGitConfig(path string, visit func(gitConfigEntry) bool) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- a git config file Git itself would read
	if os.IsNotExist(err) {
		return true
	}
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	section, haveSection := "", false
	var subsection *string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.ContainsRune(line, '\r') {
			return false
		}
		if gitConfigCommentLn.MatchString(line) {
			continue
		}
		if m := gitConfigHeader.FindStringSubmatch(line); m != nil {
			section, haveSection = strings.ToLower(m[1]), true
			subsection = nil
			if strings.Contains(line[:strings.IndexByte(line, ']')], `"`) {
				sub := m[2]
				subsection = &sub
			}
			continue
		}
		m := gitConfigKeyLine.FindStringSubmatchIndex(line)
		if m == nil || !haveSection {
			return false
		}
		e := gitConfigEntry{section: section, subsection: subsection, key: strings.ToLower(line[m[2]:m[3]])}
		if m[4] >= 0 {
			e.hasValue = true
			e.rawValue = line[m[4]:m[5]]
			value, ok := parseGitConfigValue(e.rawValue)
			if !ok {
				return false
			}
			e.value = value
		}
		if !visit(e) {
			return false
		}
	}
	return true
}

// parseGitConfigValue strips a trailing comment, surrounding whitespace and
// balanced double quotes from a value it fully understands.
func parseGitConfigValue(raw string) (string, bool) {
	if strings.ContainsRune(raw, '\\') || strings.Count(raw, `"`)%2 != 0 {
		return "", false
	}
	var b strings.Builder
	inQuote := false
	for _, c := range raw {
		switch {
		case c == '"':
			inQuote = !inQuote
			continue
		case !inQuote && (c == '#' || c == ';'):
			return strings.TrimSpace(b.String()), true
		}
		b.WriteRune(c)
	}
	return strings.TrimSpace(b.String()), true
}
