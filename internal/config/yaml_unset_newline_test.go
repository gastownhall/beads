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
// LF by any unset — that predates this fix and is pinned below as
// known-and-accepted rather than quietly claimed away.
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
	if err := UnsetYamlConfig("dolt.mode"); err != nil {
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
