//go:build cgo

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedCreateReportsInvalidTypeBeforeTitleAndInDryRun pins GH#5787
// issue 1: `-t` is the short flag for --type, so `--type=bug -t "<title>"`
// rebinds the type to the title text. bd must name the invalid type rather
// than claim the title is missing, and --dry-run must refuse the same type
// the real create refuses.
func TestEmbeddedCreateReportsInvalidTypeBeforeTitleAndInDryRun(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tyv")

	run := func(args ...string) (string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String() + stderr.String(), err
	}

	t.Run("short -t rebinding type", func(t *testing.T) {
		out, err := run("create", "--type=bug", "-p", "2", "-t", "a long human title with spaces", "-d", "desc")
		if err == nil {
			t.Fatalf("create succeeded; want an invalid-type error\n%s", out)
		}
		if strings.Contains(out, "title required") || !strings.Contains(out, "invalid issue type") {
			t.Fatalf("error should name the invalid type, not a missing title; got:\n%s", out)
		}
	})

	t.Run("dry-run validates type", func(t *testing.T) {
		out, err := run("create", "--type=a long human title with spaces", "--title=PROBE", "--dry-run")
		if err == nil {
			t.Fatalf("create --dry-run accepted a type the real create rejects; got:\n%s", out)
		}
		if !strings.Contains(out, "invalid issue type") {
			t.Fatalf("dry-run error should name the invalid type; got:\n%s", out)
		}
	})

	t.Run("dry-run validates type in json mode", func(t *testing.T) {
		out, err := run("create", "--type=a long human title with spaces", "--title=PROBE", "--dry-run", "--json")
		if err == nil {
			t.Fatalf("create --dry-run --json accepted a type the real create rejects; got:\n%s", out)
		}
		if !strings.Contains(out, "invalid issue type") {
			t.Fatalf("json dry-run error should name the invalid type; got:\n%s", out)
		}
		if strings.Contains(out, "PROBE") {
			t.Fatalf("json dry-run must emit no preview; got:\n%s", out)
		}
	})
}
