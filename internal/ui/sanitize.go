package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeForTerminal removes ANSI escape sequences and control characters
// from a string to prevent terminal injection attacks. External tracker content
// (issue titles, descriptions) should be sanitized before terminal display.
//
// Preserves printable UTF-8 characters including common punctuation and emoji.
// Strips: ANSI CSI sequences (\x1b[...), OSC sequences (\x1b]...\x07),
// other escape sequences, and C0/C1 control characters (except \n and \t).
func SanitizeForTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	i := 0
	for i < len(s) {
		ch := s[i]

		// Strip ANSI escape sequences: ESC [ ... final_byte
		if ch == '\x1b' && i+1 < len(s) {
			next := s[i+1]
			if next == '[' {
				// CSI sequence: skip until final byte (0x40-0x7E)
				j := i + 2
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
					j++ // skip parameter bytes
				}
				if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7E {
					j++ // skip final byte
				}
				i = j
				continue
			}
			if next == ']' {
				// OSC sequence: skip until BEL (\x07) or ST (\x1b\x5c)
				j := i + 2
				for j < len(s) {
					if s[j] == '\x07' {
						j++
						break
					}
					if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			}
			// Other escape: skip ESC + one byte
			i += 2
			continue
		}

		// Allow newlines and tabs
		if ch == '\n' || ch == '\t' {
			b.WriteByte(ch)
			i++
			continue
		}

		// Strip C0 control characters (0x00-0x1F except \n, \t handled above)
		if ch < 0x20 {
			i++
			continue
		}

		// Strip DEL
		if ch == 0x7F {
			i++
			continue
		}

		// For multi-byte UTF-8, decode and check
		r := rune(ch)
		size := 1
		if ch >= 0x80 {
			// Decode UTF-8 rune
			r, size = utf8.DecodeRuneInString(s[i:])
			if r == unicode.ReplacementChar && size == 1 {
				// Invalid UTF-8 byte, skip
				i++
				continue
			}
			// Strip C1 control characters (U+0080-U+009F)
			if r >= 0x80 && r <= 0x9F {
				i += size
				continue
			}
		}

		b.WriteString(s[i : i+size])
		i += size
	}

	return b.String()
}
