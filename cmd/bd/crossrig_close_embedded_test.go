//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossRigOpenForeignBlockerRefusesExplicitCloseAndClaim pins
// GH#5784 AC-4: a source held by an OPEN foreign issue — a bare cross-prefix
// blocks edge the source rig stores — is withheld from `bd ready`, and the
// same verdict must gate the EXPLICIT lifecycle moves too: `bd close` and
// `bd update --claim` refuse naming the foreign blocker, `--force` keeps its
// override, and closing the foreign target releases the source.
func TestEmbeddedCrossRigOpenForeignBlockerRefusesExplicitCloseAndClaim(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt cross-rig close tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gt")

	targetDir := filepath.Join(dir, "hq-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "hq")

	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"hq-","path":"hq-repo"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	foreign := bdCreate(t, bd, targetDir, "foreign blocker")
	source := bdCreate(t, bd, dir, "source blocked by a foreign issue")

	if out, err := bdRunWithFlockRetry(t, bd, dir, "link", source.ID, foreign.ID, "--type", "blocks"); err != nil {
		t.Fatalf("bd link (cross-prefix): %v\n%s", err, out)
	}

	t.Run("close refuses naming the foreign blocker", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "close", source.ID)
		if err == nil {
			t.Fatalf("bd close must refuse while the foreign blocker %s is open:\nstdout:\n%s\nstderr:\n%s", foreign.ID, stdout, stderr)
		}
		if !strings.Contains(stderr, foreign.ID) {
			t.Errorf("bd close refusal must name the foreign blocker %s:\nstderr:\n%s", foreign.ID, stderr)
		}
		// The refusal must not have closed the source: the short line's
		// status icon is the wordless status (○ open, ✓ closed).
		showOut, _, showErr := bdRunWithFlockRetryBuffers(t, bd, dir, "show", source.ID, "--short")
		if showErr != nil {
			t.Fatalf("bd show after refused close: %v\n%s", showErr, showOut)
		}
		if !strings.Contains(showOut, "○") {
			t.Errorf("a refused close must leave %s open:\n%s", source.ID, showOut)
		}
	})
	t.Run("claim refuses naming the foreign blocker", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "update", source.ID, "--claim")
		if err == nil {
			t.Fatalf("bd update --claim must refuse while the foreign blocker %s is open:\nstdout:\n%s\nstderr:\n%s", foreign.ID, stdout, stderr)
		}
		if !strings.Contains(stderr, foreign.ID) {
			t.Errorf("claim refusal must name the foreign blocker %s:\nstderr:\n%s", foreign.ID, stderr)
		}
	})
	t.Run("close --force keeps its override", func(t *testing.T) {
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "close", source.ID, "--force")
		if err != nil {
			t.Fatalf("bd close --force must override the foreign blocker:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		showOut, _, showErr := bdRunWithFlockRetryBuffers(t, bd, dir, "show", source.ID, "--short")
		if showErr != nil {
			t.Fatalf("bd show after forced close: %v\n%s", showErr, showOut)
		}
		if !strings.Contains(showOut, "✓") {
			t.Errorf("bd close --force must leave %s closed:\n%s", source.ID, showOut)
		}
	})
	t.Run("closing the foreign target releases the source", func(t *testing.T) {
		// A second source on the same foreign blocker: the first was closed
		// by --force above, so this one exercises the ordinary release.
		released := bdCreate(t, bd, dir, "second source on the same foreign blocker")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "link", released.ID, foreign.ID, "--type", "blocks"); err != nil {
			t.Fatalf("bd link (cross-prefix): %v\n%s", err, out)
		}
		if out, _, err := bdRunWithFlockRetryBuffers(t, bd, dir, "close", released.ID); err == nil {
			t.Fatalf("bd close must refuse while the foreign blocker %s is open:\n%s", foreign.ID, out)
		}
		if out, err := bdRunWithFlockRetry(t, bd, targetDir, "close", foreign.ID); err != nil {
			t.Fatalf("bd close (foreign target): %v\n%s", err, out)
		}
		stdout, stderr, err := bdRunWithFlockRetryBuffers(t, bd, dir, "close", released.ID)
		if err != nil {
			t.Fatalf("bd close must succeed once the foreign blocker is closed:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
	})
}
