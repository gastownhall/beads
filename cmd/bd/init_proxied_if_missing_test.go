//go:build cgo

package main

import (
	"strings"
	"testing"
)

// TestProxiedServerInitIfMissing covers --init-if-missing on the proxied-server
// route, where it used to be inert: runInitProxiedServer runs its own
// already-initialized check and returns before init.go's --init-if-missing
// block, so a scaffold script re-running init got exit 1 and the "Aborting."
// banner, and the mismatch guards that protect the flag had never run at all.
func TestProxiedServerInitIfMissing(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "pim")

	initArgs := func(extra ...string) []string {
		return append([]string{
			"init", "--proxied-server", "--non-interactive", "--skip-agents", "--skip-hooks",
		}, extra...)
	}

	t.Run("re-init skips benignly", func(t *testing.T) {
		_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, initArgs("--init-if-missing")...)
		if err != nil {
			t.Fatalf("init --init-if-missing should exit 0 on an initialized workspace: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "Skipping init: workspace already initialized") {
			t.Errorf("expected the benign skip message, got stderr:\n%s", stderr)
		}
	})

	t.Run("matching prefix skips benignly", func(t *testing.T) {
		_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, initArgs("--init-if-missing", "--prefix", p.prefix)...)
		if err != nil {
			t.Fatalf("matching --prefix should still skip: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "Skipping init: workspace already initialized") {
			t.Errorf("expected the benign skip message, got stderr:\n%s", stderr)
		}
	})

	// The multi-project ask. Skipping here would silently ignore --database and
	// keep using the existing one, so it must refuse — and say which database
	// the workspace is actually on.
	t.Run("different database is refused, not skipped", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, initArgs("--init-if-missing", "--database", "some_other_db")...)
		if err == nil {
			t.Fatalf("mismatched --database must abort; got success\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		out := stdout + stderr
		for _, want := range []string{
			"workspace already initialized as database " + `"` + p.database + `"`,
			`--database "some_other_db" was requested`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("abort message missing %q, got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Skipping init") {
			t.Errorf("a mismatched --database must not be skipped, got:\n%s", out)
		}
	})

	t.Run("different prefix is refused, not skipped", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, initArgs("--init-if-missing", "--prefix", "somethingelse")...)
		if err == nil {
			t.Fatalf("mismatched --prefix must abort; got success\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		out := stdout + stderr
		for _, want := range []string{
			"workspace already initialized as database " + `"` + p.database + `"`,
			`--prefix "somethingelse" was requested`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("abort message missing %q, got:\n%s", want, out)
			}
		}
	})

	// Without the flag, the existing refusal is unchanged: --init-if-missing is
	// what makes re-init benign, not this change.
	t.Run("without the flag re-init still aborts", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, initArgs()...)
		if err == nil {
			t.Fatalf("re-init without --init-if-missing must abort; got success\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if out := stdout + stderr; !strings.Contains(out, "already initialized") {
			t.Errorf("expected the existing already-initialized refusal, got:\n%s", out)
		}
	})
}
