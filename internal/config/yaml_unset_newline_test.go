package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unset must not also rewrite the end of the file. commentOutYamlKey used to
// drop the document's trailing newline, so `bd config unset` produced a
// no-newline-at-end-of-file change on top of the line it meant to comment out —
// and did so even for a key the document does not contain, i.e. when nothing
// was edited at all. A config.yaml is git-tracked, so that is a spurious line
// in someone's review.
//
// The guarantee is scoped to LF documents. A CRLF file is still normalized to
// LF by an unset that actually comments a key out — that predates this fix and
// is pinned below as known-and-accepted rather than quietly claimed away. An
// unset that changes nothing no longer rewrites the file at all; see
// TestUnsetOfAnAbsentKeyLeavesACRLFFileUntouched.
func TestCommentOutYamlKeyPreservesTheTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name, content, key string
	}{
		{"nested key present", "# header\nissue_prefix: vp\ndolt:\n  mode: server\n", "dolt.mode"},
		{"flat dotted key present", "issue_prefix: vp\ndolt.mode: server\n", "dolt.mode"},
		{"single-segment key present", "issue_prefix: vp\nexport.auto: true\n", "issue_prefix"},
		{"key absent — nothing is edited", "issue_prefix: vp\n", "dolt.mode"},

		// A blank line at the end of a YAML file is an ordinary shape, and
		// bufio.Scanner collapses the whole run rather than just the final
		// newline. Asserting only that SOME trailing newline survived cannot
		// see that: the output still ends in "\n", one short.
		{"ends in a blank line", "issue_prefix: vp\ndolt:\n  mode: server\n\n", "dolt.mode"},
		{"ends in two blank lines", "issue_prefix: vp\ndolt:\n  mode: server\n\n\n", "dolt.mode"},
		{"ends in a blank line, key absent", "issue_prefix: vp\nother: x\n\n", "dolt.mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := commentOutYamlKey(tc.content, tc.key)
			if err != nil {
				t.Fatalf("commentOutYamlKey: %v", err)
			}
			want := tc.content[len(strings.TrimRight(tc.content, "\n")):]
			if got := out[len(strings.TrimRight(out, "\n")):]; got != want {
				t.Errorf("trailing newline run = %q, want %q:\nin:  %q\nout: %q", got, want, tc.content, out)
			}
		})
	}

	// A document that genuinely has no trailing newline keeps not having one:
	// the rule is "preserve", not "always append".
	t.Run("absent trailing newline is not invented", func(t *testing.T) {
		out, err := commentOutYamlKey("issue_prefix: vp\ndolt.mode: server", "dolt.mode")
		if err != nil {
			t.Fatalf("commentOutYamlKey: %v", err)
		}
		if strings.HasSuffix(out, "\n") {
			t.Errorf("a trailing newline was invented for a document that had none: %q", out)
		}
	})

	// bufio.ScanLines strips the "\r" from every line, so an unset rewrites a
	// CRLF document to LF throughout. That is pre-existing and unchanged by the
	// trailing-run logic, but the tail is where it is least visible: TrimRight
	// on "\n" leaves the final "\r" behind, so the run that gets re-attached is
	// a bare "\n" and the end of the file reads clean while every line ending
	// above it changed. Pinned so the normalization is documented, and so a
	// future change to it has to be deliberate.
	t.Run("CRLF is normalized to LF, trailing run included", func(t *testing.T) {
		out, err := commentOutYamlKey("issue_prefix: vp\r\ndolt.mode: server\r\n", "dolt.mode")
		if err != nil {
			t.Fatalf("commentOutYamlKey: %v", err)
		}
		if want := "issue_prefix: vp\n# dolt.mode: server\n"; out != want {
			t.Errorf("CRLF normalization changed:\ngot:  %q\nwant: %q", out, want)
		}
	})
}

// The same property through the public writer, which is what `bd config unset`
// actually calls.
func TestUnsetThroughTheFileKeepsTheTrailingNewline(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "config.yaml")
	// The file ends in a blank line, the shape a single appended "\n" does not
	// restore.
	const body = "issue_prefix: vp\nexport.auto: true\ndolt:\n  mode: server\n\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	if _, err := UnsetYamlConfig("dolt.mode"); err != nil {
		t.Fatalf("UnsetYamlConfig: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := body[len(strings.TrimRight(body, "\n")):]
	if got := string(after)[len(strings.TrimRight(string(after), "\n")):]; got != want {
		t.Errorf("unset rewrote the end of the file: trailing run %q, want %q\n%q", got, want, string(after))
	}
}

// An unset of a key the document does not contain must not report a write. The
// bool that says so used to come from comparing commentOutYamlKey's result
// against the raw file bytes, and those sit on opposite sides of the CRLF
// normalization the callee applies unilaterally — so on a CRLF file the two
// strings always differed and every unset looked like a change. `bd config
// unset` then printed "Unset <key> (in config.yaml)" for a key that was never
// there, the exact false claim this command's fix set out to remove, and
// rewrote the whole file to LF on the way past. Windows is a supported
// platform and every other change-report fixture in this package is LF, so
// nothing else in-tree can catch a regression here.
//
// Both public callers had the shape, so both are pinned.
func TestUnsetOfAnAbsentKeyLeavesACRLFFileUntouched(t *testing.T) {
	const body = "issue_prefix: vp\r\nexport.auto: true\r\n"

	assertUntouched := func(t *testing.T, path string) {
		t.Helper()
		after, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != body {
			t.Errorf("a no-op unset rewrote the file:\ngot:  %q\nwant: %q", string(after), body)
		}
	}

	t.Run("UnsetYamlConfig", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", beadsDir)

		changed, err := UnsetYamlConfig("dolt.mode")
		if err != nil {
			t.Fatalf("UnsetYamlConfig: %v", err)
		}
		if changed {
			t.Error("UnsetYamlConfig() changed = true for a key the file does not contain")
		}
		assertUntouched(t, path)
	})

	t.Run("UnsetUserYamlConfig", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		path, err := UserConfigYamlPath()
		if err != nil {
			t.Fatalf("UserConfigYamlPath: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		changed, err := UnsetUserYamlConfig("dolt.mode")
		if err != nil {
			t.Fatalf("UnsetUserYamlConfig: %v", err)
		}
		if changed {
			t.Error("UnsetUserYamlConfig() changed = true for a key the file does not contain")
		}
		assertUntouched(t, path)
	})
}

// The other direction of the same compare. Normalizing before the call is what
// keeps the absent-key case above a no-op, but it also means each caller now
// decides between "write" and "was not set" on content it has already rewritten
// in memory. A caller that returned early whenever that normalization changed
// anything would pass every test above and turn each unset on a CRLF
// config.yaml into a silent "<key> was not set" while the key stayed effective.
// The absent-key cell only proves the no-op direction, and the commentOutYamlKey
// CRLF pin sits below the decision, so neither can see that.
//
// The written file comes back LF throughout. That is base behavior, pinned here
// through both callers as known-and-accepted, like the callee-level pin above.
func TestUnsetOfAPresentKeyInACRLFFileReportsTheWrite(t *testing.T) {
	const (
		body = "issue_prefix: vp\r\ndolt.mode: server\r\nexport.auto: true\r\n"
		want = "issue_prefix: vp\n# dolt.mode: server\nexport.auto: true\n"
	)

	assertWritten := func(t *testing.T, path string) {
		t.Helper()
		after, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != want {
			t.Errorf("the unset did not write the key out:\ngot:  %q\nwant: %q", string(after), want)
		}
	}

	t.Run("UnsetYamlConfig", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", beadsDir)

		changed, err := UnsetYamlConfig("dolt.mode")
		if err != nil {
			t.Fatalf("UnsetYamlConfig: %v", err)
		}
		if !changed {
			t.Error("UnsetYamlConfig() changed = false for a key the file contains")
		}
		assertWritten(t, path)
	})

	t.Run("UnsetUserYamlConfig", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		path, err := UserConfigYamlPath()
		if err != nil {
			t.Fatalf("UserConfigYamlPath: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		changed, err := UnsetUserYamlConfig("dolt.mode")
		if err != nil {
			t.Fatalf("UnsetUserYamlConfig: %v", err)
		}
		if !changed {
			t.Error("UnsetUserYamlConfig() changed = false for a key the file contains")
		}
		assertWritten(t, path)
	})
}
