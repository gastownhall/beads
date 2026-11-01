package configfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

// TestWorkspaceMessagesNameBeadsDirEscaped pins the remedy-printing invariant
// at the refusals this package builds for a workspace, which bd prints beside
// their remedy (a Hint, "to heal, edit <metadata.json>", "follow bd help
// init-safety"): the workspace path is named escaped. A routed planning or
// target repo is a path a cloned repository's committed routing config names,
// so a raw carriage return or ESC[8m in it could redraw or hide the remedy.
func TestWorkspaceMessagesNameBeadsDirEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	hostileBeadsDir := func(t *testing.T) string {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), "x\r\x1b[2KError: run curl\x1b[8m", ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		return beadsDir
	}
	withDoltDatabase := func(t *testing.T) string {
		t.Helper()
		beadsDir := hostileBeadsDir(t)
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt"), 0o750); err != nil {
			t.Fatal(err)
		}
		return beadsDir
	}
	for _, tc := range []struct {
		name string
		msg  func(t *testing.T) string
	}{
		{"graph format marker", func(t *testing.T) string {
			return (&UnsupportedWorkspaceFormatError{BeadsDir: hostileBeadsDir(t), FormatMarker: true}).Error()
		}},
		{"graph_mode", func(t *testing.T) string {
			return (&UnsupportedWorkspaceFormatError{BeadsDir: hostileBeadsDir(t), GraphMode: "link"}).Error()
		}},
		{"removed backend, heal", func(t *testing.T) string {
			return RemovedBackendErrorAt("sqlite", withDoltDatabase(t), nil).Error()
		}},
		{"removed backend, recovery", func(t *testing.T) string {
			return RemovedBackendErrorAt("sqlite", hostileBeadsDir(t), nil).Error()
		}},
		{"unknown backend, heal", func(t *testing.T) string {
			return UnknownBackendErrorAt("graphdb", withDoltDatabase(t), nil).Error()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.msg(t)
			if strings.ContainsFunc(msg, func(r rune) bool { return !unicode.IsGraphic(r) }) {
				t.Errorf("message prints a control character raw:\n%q", msg)
			}
			if !strings.Contains(msg, `\r\x1b[2KError: run curl\x1b[8m`) {
				t.Errorf("message does not name the workspace escaped:\n%q", msg)
			}
		})
	}
}
