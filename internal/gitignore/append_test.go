package gitignore

import "testing"

func TestAppendLineEnding(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"empty", "", "\n"},
		{"delimiter-free", "local", "\n"},
		{"LF", "a\n", "\n"},
		{"CRLF", "a\r\n", "\r\n"},
		{"CRLF with unterminated final line", "node_modules/\r\nbuild/", "\r\n"},
		{"mixed", "a\r\nb\r\nc\n", "\n"},
		{"BOM LF", "\xef\xbb\xbfa\n", "\n"},
		{"BOM CRLF", "\xef\xbb\xbfa\r\n", "\r\n"},
		{"lone CR", "\r", "\n"},
		{"CR-only", "a\rb\rc\r", "\n"},
		{"interior CR", "a\rb\r\n", "\r\n"},
		{"CRCRLF", "a\r\r\n", "\r\n"},
		{"UTF16-like", "a\x00\r\x00\n\x00", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AppendLineEnding([]byte(tc.content)); got != tc.want {
				t.Errorf("AppendLineEnding(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}
