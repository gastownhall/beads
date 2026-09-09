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
