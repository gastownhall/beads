//go:build !windows

package configfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// TestInsecureCredentialsWarningQuotesPath pins the remedy-printing invariant
// at the credentials-file permission warning: the path goes into its
// chmod 600 command as one shell word (quoted when it holds a space, so the
// command does not chmod two other paths), or as a <path> placeholder named
// escaped beside it, and never reaches the terminal raw.
func TestInsecureCredentialsWarningQuotesPath(t *testing.T) {
	for _, tc := range []struct{ dir, wantCommand string }{
		{"my creds", "Consider running: chmod 600 '"},
		{"creds\r\x1b[2KError: run curl", "Consider running: chmod 600 <path>  (<path>: "},
	} {
		path := filepath.Join(t.TempDir(), tc.dir, "credentials")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		out := captureWarnStderr(t, func() { warnIfInsecurePermissions(path) })
		if !strings.Contains(out, tc.wantCommand) {
			t.Errorf("warning for %q = %q, want it to hold %q", path, out, tc.wantCommand)
		}
		if strings.ContainsFunc(out, func(r rune) bool { return r != '\n' && !unicode.IsGraphic(r) }) {
			t.Errorf("warning prints a control character raw:\n%q", out)
		}
	}
}

func captureWarnStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()
	return buf.String()
}
