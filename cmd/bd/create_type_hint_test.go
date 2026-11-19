package main

import (
	"strings"
	"testing"
)

// TestCreateTypeHint pins the GH#5787 `-t` confusion: `-t` is the short flag
// for --type, so `bd create --type=bug -t "<a title>"` rebinds the type to
// the intended title. validateCreateArgs must name the invalid type (and
// where the value went) instead of reporting only a missing title, while
// built-in and alias types keep the plain missing-title error and every
// other title diagnostic is unchanged.
func TestCreateTypeHint(t *testing.T) {
	run := func(args []string, positional ...string) (string, error) {
		cmd := newCreateFlagsCommand(t, args...)
		var err error
		stderr := captureStderr(t, func() {
			err = validateCreateArgs(cmd, positional)
		})
		return stderr, err
	}

	splat := func(args ...string) []string { return args }

	t.Run("invalid -t type names the type, not the missing title", func(t *testing.T) {
		stderr, err := run(splat("--type=bug", "--type", "a long human title with spaces"))
		if err == nil {
			t.Fatal("expected an invalid-type error")
		}
		if strings.Contains(stderr, "title required") {
			t.Fatalf("error should not lead with the missing-title message; got:\n%s", stderr)
		}
		for _, want := range []string{
			"invalid issue type",
			`"a long human title with spaces"`,
			"-t is the short flag for --type, not --title",
			"a title is missing",
		} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("error should contain %q; got:\n%s", want, stderr)
			}
		}
	})

	t.Run("built-in type keeps the plain missing-title error", func(t *testing.T) {
		stderr, err := run(splat("--type", "task"))
		if err == nil {
			t.Fatal("expected the missing-title error")
		}
		if !strings.Contains(stderr, "title required") {
			t.Fatalf("built-in --type should retain the missing-title error; got:\n%s", stderr)
		}
	})

	t.Run("alias type normalizes before the hint", func(t *testing.T) {
		stderr, err := run(splat("--type", "feat"))
		if err == nil {
			t.Fatal("expected the missing-title error")
		}
		if !strings.Contains(stderr, "title required") {
			t.Fatalf("alias --type feat should normalize to feature and retain the missing-title error; got:\n%s", stderr)
		}
	})

	t.Run("title present does not trigger the hint", func(t *testing.T) {
		_, err := run(splat("--type", "a long human title with spaces", "--title", "Real title"))
		if err != nil {
			t.Fatalf("a present title with a bad type is refused at store time, not at arg validation: %v", err)
		}
	})

	t.Run("whitespace title keeps its own diagnostic", func(t *testing.T) {
		stderr, err := run(splat("--type", "a long human title with spaces", "--title", "   "))
		if err == nil {
			t.Fatal("expected the whitespace-title error")
		}
		if !strings.Contains(stderr, "whitespace-only") {
			t.Fatalf("expected the whitespace-title diagnostic; got:\n%s", stderr)
		}
	})

	t.Run("--file bypasses title validation", func(t *testing.T) {
		if _, err := run(splat("--type", "a long human title with spaces", "--file", "issues.md")); err != nil {
			t.Fatalf("--file defers title validation to file processing: %v", err)
		}
	})
}
