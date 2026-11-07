package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Neither an unset nor a set may also rewrite the end of the file.
// commentOutYamlKey and updateYamlKey's flat-key path both used to drop the
// document's trailing newline, so `bd config unset` and `bd config set` each
// produced a no-newline-at-end-of-file change on top of the line they meant to
// edit — and unset did so even for a key the document does not contain, i.e.
// when nothing was edited at all. A config.yaml is git-tracked, so that is a
// spurious line in someone's review. The unset tests come first; the set tests
// follow them, starting at TestUpdateYamlKeyPreservesTheTrailingNewline.
//
// The guarantee is scoped to LF documents. A CRLF file is still normalized to
// LF by an unset that actually comments a key out, and by any set of a
// single-segment key — that predates both fixes and is pinned below as
// known-and-accepted rather than quietly claimed away. An unset that changes
// nothing no longer rewrites the file at all; see
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

	// commentOutYamlKey normalizes CRLF to LF before anything else, so an unset
	// rewrites a CRLF document to LF throughout. That is pre-existing and
	// unchanged by the trailing-run logic, which measures the run after the
	// normalization: each trailing "\r\n" comes back as one "\n", so the end of
	// the file reads clean while every line ending above it changed. Pinned so
	// the normalization is documented, and so a future change to it has to be
	// deliberate.
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

// The user-global config.yaml goes through the same line matcher, so a value
// that wraps onto the next line is refused there as well, with nothing written
// and no change reported. node_id has a single segment, so this is the
// top-level half of the refusal.
func TestUnsetUserYamlConfigRefusesAValueThatSpansLines(t *testing.T) {
	const body = "node_id: \"mini\n  box\"\n"

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

	changed, err := UnsetUserYamlConfig("node_id")
	if err == nil {
		after, _ := os.ReadFile(path) //nolint:gosec,errcheck // test-owned temp path; diagnostic
		t.Fatalf("UnsetUserYamlConfig reported success on a value that spans two lines:\n%q", string(after))
	}
	if !strings.Contains(err.Error(), "node_id") || !strings.Contains(err.Error(), "spans more than one line") {
		t.Errorf("the error names neither the key nor the shape: %v", err)
	}
	if changed {
		t.Error("UnsetUserYamlConfig() changed = true for an unset it refused")
	}
	after, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("a refused unset still changed the file:\ngot:  %q\nwant: %q", string(after), body)
	}
}

// The user-global unset checks the edit by its effect too, before it writes.
// node_id is not in the first document, so no refusal before the edit looks
// at it, but the line matcher still finds it in the second, where commenting
// out its first line would leave `box"` behind.
func TestUnsetUserYamlConfigRefusesAnEditThatChangesMoreThanTheKey(t *testing.T) {
	const body = "metrics:\n  disabled: true\n---\nnode_id: \"mini\n  box\"\n"

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

	changed, err := UnsetUserYamlConfig("node_id")
	if err == nil {
		after, _ := os.ReadFile(path) //nolint:gosec,errcheck // test-owned temp path; diagnostic
		t.Fatalf("UnsetUserYamlConfig reported success on an edit that breaks the second document:\n%q", string(after))
	}
	if !strings.Contains(err.Error(), "node_id") || !strings.Contains(err.Error(), "more of this file than the key") {
		t.Errorf("the error names neither the key nor the reason: %v", err)
	}
	if changed {
		t.Error("UnsetUserYamlConfig() changed = true for an unset it refused")
	}
	after, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("a refused unset still changed the file:\ngot:  %q\nwant: %q", string(after), body)
	}
}

// The user-global unset of a key the file does not hold is the same no-op as
// the project one, though a line inside another value looks like the key:
// (false, nil) and the file as it was, not a refusal to edit notes.
func TestUnsetUserYamlConfigOfAnAbsentKeyLeavesTheFileAlone(t *testing.T) {
	const body = "metrics:\n  disabled: true\nnotes: \"see\nnode_id: mini\"\n"

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

	changed, err := UnsetUserYamlConfig("node_id")
	if err != nil {
		t.Fatalf("UnsetUserYamlConfig on a key the file does not hold: %v", err)
	}
	if changed {
		t.Error("UnsetUserYamlConfig() changed = true for a key the file does not hold")
	}
	after, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("the no-op unset changed the file:\ngot:  %q\nwant: %q", string(after), body)
	}
}

// The set twin of TestCommentOutYamlKeyPreservesTheTrailingNewline.
// updateYamlKey's flat-key path read the document with bufio.Scanner and joined
// the lines back with "\n" — the shape commentOutYamlKey used to have — so it
// came back exactly one newline short of any terminated document. Every key
// here is a single segment, because only those reach that path: updateYamlKey
// hands every dotted key to updateNestedYamlKey, a different writer, so a
// dotted-key row would pass without running the code under test at all.
func TestUpdateYamlKeyPreservesTheTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name, content, key string
	}{
		{"key present", "issue_prefix: vp\ndolt.mode: server\n", "issue_prefix"},
		{"commented key present", "# actor: \"\"\nother: x\n", "actor"},
		{"key on the last line", "other: x\nactor: a\n", "actor"},
		{"key absent, appended", "issue_prefix: vp\n", "actor"},

		// As on the unset side, a run of blank lines lost exactly one newline,
		// so the output still ended in "\n" and only an exact comparison of the
		// run can see the loss. An absent key appended to a file that ends like
		// this is not a row here: its blank lines end up ahead of the new key,
		// so the run after the key is not the file's run. The appended-key
		// subtest below pins those outputs exactly.
		{"ends in a blank line", "actor: a\nother: x\n\n", "actor"},
		{"ends in two blank lines", "actor: a\nother: x\n\n\n", "actor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := updateYamlKey(tc.content, tc.key, "b")
			if err != nil {
				t.Fatalf("updateYamlKey: %v", err)
			}
			want := tc.content[len(strings.TrimRight(tc.content, "\n")):]
			if got := out[len(strings.TrimRight(out, "\n")):]; got != want {
				t.Errorf("trailing newline run = %q, want %q:\nin:  %q\nout: %q", got, want, tc.content, out)
			}
		})
	}

	// The in-place branch replaces the one value and leaves every other byte,
	// the terminator included, as it was.
	t.Run("key present: only the value changes", func(t *testing.T) {
		out, err := updateYamlKey("issue_prefix: vp\ndolt.mode: server\n", "issue_prefix", "zz")
		if err != nil {
			t.Fatalf("updateYamlKey: %v", err)
		}
		if want := "issue_prefix: \"zz\"\ndolt.mode: server\n"; out != want {
			t.Errorf("updateYamlKey:\ngot:  %q\nwant: %q", out, want)
		}
	})

	// The appended branch makes an absent key the new last line, which is where
	// the old join left it unterminated: "x\n" came back "x\n\nnewkey: \"v\"".
	// The set adds that one line and nothing else: a blank line before it only
	// when the file does not already end in one, and a terminator after it when
	// the file had one.
	t.Run("appended key: separator kept, terminator restored", func(t *testing.T) {
		for _, tc := range []struct{ content, want string }{
			{"x\n", "x\n\nnewkey: \"v\"\n"},
			// A trailing blank line already separates, so no second one is added,
			// and the file's blank lines stay ahead of the new key instead of
			// being repeated after it: the file comes back whole, followed by one
			// terminated line. Re-attaching the file's own run here would end it
			// "x\n\nnewkey: \"v\"\n\n", a blank line nobody asked for.
			{"x\n\n", "x\n\nnewkey: \"v\"\n"},
			{"x\n\n\n", "x\n\n\nnewkey: \"v\"\n"},
			// The same append on a CRLF file, which comes back LF throughout.
			{"x\r\n\r\n", "x\n\nnewkey: \"v\"\n"},
		} {
			out, err := updateYamlKey(tc.content, "newkey", "v")
			if err != nil {
				t.Fatalf("updateYamlKey(%q): %v", tc.content, err)
			}
			if out != tc.want {
				t.Errorf("updateYamlKey(%q):\ngot:  %q\nwant: %q", tc.content, out, tc.want)
			}
		}
	})

	// A document that genuinely has no trailing newline keeps not having one:
	// the rule is "preserve", not "always append". One input per branch.
	t.Run("absent trailing newline is not invented", func(t *testing.T) {
		for _, content := range []string{"actor: a\nother: x", "other: x"} {
			out, err := updateYamlKey(content, "actor", "b")
			if err != nil {
				t.Fatalf("updateYamlKey(%q): %v", content, err)
			}
			if strings.HasSuffix(out, "\n") {
				t.Errorf("a trailing newline was invented for a document that had none: %q -> %q", content, out)
			}
		}
	})

	// A set of a single-segment key rewrites a CRLF document to LF throughout,
	// as an unset does. That predates the fix and is pinned here, like unset's
	// subtest of the same name, so that changing it has to be deliberate. What
	// the fix changes is the end: the old join dropped the terminator, so
	// "…x\r\n" came back "…x". A set now ends the file the way an unset of the
	// same document does, blank lines included: both measure the trailing run
	// after normalizing, so each trailing "\r\n" comes back as one "\n".
	t.Run("CRLF is normalized to LF, trailing run included", func(t *testing.T) {
		for _, tc := range []struct{ content, want string }{
			{"actor: a\r\nother: x\r\n", "actor: \"b\"\nother: x\n"},
			// Measured on the raw bytes, the run stops at the last "\r", so
			// re-attaching it ends both of these in a single "\n". For the
			// second that loses more than the base did, which ended it "…x\n\n".
			{"actor: a\r\nother: x\r\n\r\n", "actor: \"b\"\nother: x\n\n"},
			{"actor: a\r\nother: x\r\n\r\n\r\n", "actor: \"b\"\nother: x\n\n\n"},
		} {
			out, err := updateYamlKey(tc.content, "actor", "b")
			if err != nil {
				t.Fatalf("updateYamlKey(%q): %v", tc.content, err)
			}
			if out != tc.want {
				t.Errorf("updateYamlKey(%q):\ngot:  %q\nwant: %q", tc.content, out, tc.want)
			}
			unset, err := commentOutYamlKey(tc.content, "actor")
			if err != nil {
				t.Fatalf("commentOutYamlKey(%q): %v", tc.content, err)
			}
			if got, want := out[len(strings.TrimRight(out, "\n")):], unset[len(strings.TrimRight(unset, "\n")):]; got != want {
				t.Errorf("set and unset end %q differently: set %q, unset %q", tc.content, got, want)
			}
		}
	})
}

// The same property through the public writer, which is what `bd config set`
// actually calls: the twin of TestUnsetThroughTheFileKeepsTheTrailingNewline.
func TestSetThroughTheFileKeepsTheTrailingNewline(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "config.yaml")
	// The file ends in a blank line, the shape a single appended "\n" does not
	// restore. actor is a single-segment key, so the set takes the flat path.
	const body = "issue_prefix: vp\nactor: alice\ndolt:\n  mode: server\n\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	if err := SetYamlConfig("actor", "bob"); err != nil {
		t.Fatalf("SetYamlConfig: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The value must have landed, or an untouched file would pass below.
	if !strings.Contains(string(after), "actor: \"bob\"\n") {
		t.Fatalf("set did not write the value:\n%q", string(after))
	}
	want := body[len(strings.TrimRight(body, "\n")):]
	if got := string(after)[len(strings.TrimRight(string(after), "\n")):]; got != want {
		t.Errorf("set rewrote the end of the file: trailing run %q, want %q\n%q", got, want, string(after))
	}
}

// The appended branch through the same writer. The file already ends in a
// blank line, which separates the new key, so the set adds exactly one line:
// the change a hand edit would make, and all someone reviewing it should see.
func TestSetOfAnAbsentKeyThroughTheFileAddsOneLine(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "config.yaml")
	const body = "issue_prefix: vp\nactor: alice\n\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	if err := SetYamlConfig("no-push", "true"); err != nil {
		t.Fatalf("SetYamlConfig: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := body + "no-push: true\n"; string(after) != want {
		t.Errorf("set of an absent key:\ngot:  %q\nwant: %q", string(after), want)
	}
}

// updateYamlKey hands every dotted key to updateNestedYamlKey before it
// normalizes CRLF, and has to keep doing so: replaceFlatKeyLine and
// appendNestedYamlKey carry "\r" through on the lines they leave alone (the
// literal dotted key and the comment-only document below), and normalizing
// above the dispatch would silently change what they write. The other two rows
// are re-marshalled to LF either way. This pins the boundary, not those
// outputs: whatever the nested writer makes of a CRLF document, a set of a
// dotted key returns exactly that.
func TestUpdateYamlKeyLeavesTheNestedPathAlone(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"literal dotted key", "issue_prefix: vp\r\ndolt.mode: server\r\n"},
		{"nested block", "issue_prefix: vp\r\ndolt:\r\n  mode: server\r\n"},
		{"key absent", "issue_prefix: vp\r\n"},
		{"comment-only document", "# dolt.mode: server\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, ok, err := updateNestedYamlKey(tc.content, "dolt.mode", "embedded")
			if err != nil || !ok {
				t.Fatalf("updateNestedYamlKey: ok=%v err=%v; nothing to compare against", ok, err)
			}
			got, err := updateYamlKey(tc.content, "dolt.mode", "embedded")
			if err != nil {
				t.Fatalf("updateYamlKey: %v", err)
			}
			if got != want {
				t.Errorf("updateYamlKey and updateNestedYamlKey differ on %q:\nset:    %q\nnested: %q", tc.content, got, want)
			}
		})
	}
}

// The span refusal cuts the document after the key's line as yaml.v3 numbers
// it, and yaml.v3 ends a line at more than LF: at a CR, at a CRLF (once), at a
// NEL, and at the Unicode line and paragraph separators. Counting any of them
// differently moves the cut onto another line, which refuses a value that ends
// on its own line or misses one that wraps. An unset normalizes CRLF before the
// probe runs, so a CRLF reaches the count only as what is left of a CR CR LF;
// the count has to get it right all the same. Each break is also tried as the
// file's first byte, a blank first line, where the look back for a CR before
// an LF has nothing to look at.
func TestYamlLineEndCutsWhereYamlV3EndsALine(t *testing.T) {
	lineBreaks := map[string]string{
		"LF": "\n", "CR": "\r", "CRLF": "\r\n", "CR CRLF": "\r\r\n",
		"NEL": "\u0085", "LS": "\u2028", "PS": "\u2029",
	}
	for name, lineBreak := range lineBreaks {
		for _, lead := range []string{"", lineBreak} {
			t.Run(fmt.Sprintf("%s lead %q", name, lead), func(t *testing.T) {
				testYamlLineEndCuts(t, lead+"a: 1"+lineBreak+"b: 2"+lineBreak+"c: 3")
			})
		}
	}
	// Out of range, before line 1 or past the last line, there is no cut.
	for _, line := range []int{0, 4} {
		if end, ok := yamlLineEnd("a: 1\nb: 2\nc: 3\n", line); ok {
			t.Errorf("yamlLineEnd(line %d of 3) = %d, true; want ok = false", line, end)
		}
	}
}

func testYamlLineEndCuts(t *testing.T, content string) {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		t.Fatalf("parse %q: %v", content, err)
	}
	top := root.Content[0]
	for i := 0; i < len(top.Content); i += 2 {
		key, value := top.Content[i], top.Content[i+1]
		end, ok := yamlLineEnd(content, key.Line)
		if last := i == len(top.Content)-2; ok == last {
			t.Fatalf("yamlLineEnd(%q, %d) ok = %v for key %s", content, key.Line, ok, key.Value)
		}
		if entry := key.Value + ": " + value.Value; ok && !strings.HasSuffix(content[:end], entry) {
			t.Errorf("the cut after line %d (key %s) is %q, want it to end with %q", key.Line, key.Value, content[:end], entry)
		}
	}
}
