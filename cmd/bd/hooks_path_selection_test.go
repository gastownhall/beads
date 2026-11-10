package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHooksPATHBinaryWarning covers the install-time PATH diagnostic
// (GH#5922): one stderr warning when PATH selects another bd file or none at
// all, and silence when it selects this very file — including through a
// symlink or a hardlink. The comparison only stats the candidates; the PATH
// side is never executed.
func TestHooksPATHBinaryWarning(t *testing.T) {
	selfBD := func(t *testing.T) string {
		t.Helper()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "bd")
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("warns once naming both paths when PATH selects another bd", func(t *testing.T) {
		self, other := selfBD(t), selfBD(t)
		msg := bdPathSelectionWarning(self, nil, false, other)
		if msg == "" {
			t.Fatal("bdPathSelectionWarning(different bd) = \"\", want a warning")
		}
		if !strings.Contains(msg, self) || !strings.Contains(msg, other) {
			t.Errorf("warning must name the self path and the PATH path:\n%s", msg)
		}
		if got := strings.Count(msg, "warning"); got != 1 {
			t.Errorf("warning mentions \"warning\" %d times, want exactly one diagnostic:\n%s", got, msg)
		}
	})

	t.Run("warns naming self when PATH selects no bd", func(t *testing.T) {
		self := selfBD(t)
		msg := bdPathSelectionWarning(self, nil, false, "")
		if msg == "" {
			t.Fatal("bdPathSelectionWarning(missing bd) = \"\", want a warning")
		}
		if !strings.Contains(msg, self) {
			t.Errorf("warning must name the self path:\n%s", msg)
		}
	})

	t.Run("stays silent inside the test harness", func(t *testing.T) {
		self, other := selfBD(t), selfBD(t)
		if msg := bdPathSelectionWarning(self, nil, true, other); msg != "" {
			t.Errorf("bdPathSelectionWarning(in test harness) = %q, want silence", msg)
		}
	})

	t.Run("stays silent for a test-binary self", func(t *testing.T) {
		other := selfBD(t)
		if msg := bdPathSelectionWarning("/rules/cache/bd_test", nil, false, other); msg != "" {
			t.Errorf("bdPathSelectionWarning(bd_test self) = %q, want silence", msg)
		}
	})

	t.Run("stays silent when self identity is unavailable", func(t *testing.T) {
		other := selfBD(t)
		if msg := bdPathSelectionWarning("", errors.New("exec: no such file"), false, other); msg != "" {
			t.Errorf("bdPathSelectionWarning(unresolvable self) = %q, want silence", msg)
		}
	})

	t.Run("never executes the PATH candidate", func(t *testing.T) {
		self := selfBD(t)
		dir := t.TempDir()
		marker := filepath.Join(dir, "ran")
		quoted := "'" + strings.ReplaceAll(marker, "'", `'\''`) + "'"
		stub := "#!/bin/sh\nprintf 'ran\\n' >> " + quoted + "\nexit 0\n"
		decoy := filepath.Join(dir, "bd")
		if err := os.WriteFile(decoy, []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}

		msg := bdPathSelectionWarning(self, nil, false, decoy)
		if msg == "" {
			t.Fatal("bdPathSelectionWarning(decoy) = \"\", want a warning")
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("the PATH candidate was executed while being compared")
		}
	})
}
