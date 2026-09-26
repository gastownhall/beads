package gitignore

import "testing"

func TestAppendLineEnding(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"empty", "", "\n"},
		{"delimiter-free", "local", "\n"},
		{"LF", "a\n", "\n"},
		{"CRLF", "a\r\n", "\r\n"},
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

func TestAppendLines(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		lines         []string
		want          string
	}{
		{"no lines", "last\r", nil, "last\r"},
		{"empty", "", []string{"rule/"}, "rule/\n"},
		{"blank header", "", []string{"", "# managed", "rule/"}, "\n# managed\nrule/\n"},
		{"LF unterminated", "local", []string{"rule/"}, "local\nrule/\n"},
		{"LF", "local\n", []string{"rule/"}, "local\nrule/\n"},
		{"CRLF", "local\r\n", []string{"", "# managed", "rule/"}, "local\r\n\r\n# managed\r\nrule/\r\n"},
		{"CRLF unterminated", "local\r\nlast", []string{"rule/"}, "local\r\nlast\r\nrule/\r\n"},
		{"CRLF pending CR", "local\r\nlast\r", []string{"rule/"}, "local\r\nlast\r\nrule/\r\n"},
		{"only pending CR", "last\r", []string{"rule/"}, "last\r\nrule/\n"},
		{"mixed", "a\r\nb\n", []string{"rule/"}, "a\r\nb\nrule/\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(tc.content)
			if got := AppendLines(content, tc.lines); string(got) != tc.want {
				t.Errorf("AppendLines() = %q, want %q", got, tc.want)
			}
			if string(content) != tc.content {
				t.Errorf("input changed to %q", content)
			}
		})
	}
}
