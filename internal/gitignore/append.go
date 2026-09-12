// Package gitignore provides formatting helpers for user-owned gitignore files.
package gitignore

import "bytes"

// AppendLineEnding preserves an unambiguous CRLF convention, following the
// append policy in #6343. Empty, delimiter-free, LF and mixed files
// default to LF; callers must leave existing bytes unchanged.
func AppendLineEnding(content []byte) string {
	lineFeeds := bytes.Count(content, []byte{'\n'})
	if lineFeeds > 0 && lineFeeds == bytes.Count(content, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// AppendLines appends logical lines using the existing file's line ending,
// preserving existing bytes and completing any unterminated final line.
// A trailing CR is completed with a bare LF.
// Callers choose their own blank lines, headers and patterns.
// With no lines, the returned slice aliases content without copying.
func AppendLines(content []byte, lines []string) []byte {
	if len(lines) == 0 {
		return content
	}
	lineEnding := AppendLineEnding(content)
	var buf bytes.Buffer
	buf.Write(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if content[len(content)-1] == '\r' {
			buf.WriteByte('\n') // Complete the existing CR without doubling it.
		} else {
			buf.WriteString(lineEnding)
		}
	}
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteString(lineEnding)
	}
	return buf.Bytes()
}
