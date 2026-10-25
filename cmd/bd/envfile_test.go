package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBeadsEnvFile(t *testing.T) {
	t.Run("loads env vars from .env file", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, ".env")
		if err := os.WriteFile(envFile, []byte("BEADS_TEST_LOAD_VAR=hello_from_env\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_TEST_LOAD_VAR", "") // clear
		os.Unsetenv("BEADS_TEST_LOAD_VAR")

		loadBeadsEnvFile(dir)

		if got := os.Getenv("BEADS_TEST_LOAD_VAR"); got != "hello_from_env" {
			t.Errorf("expected BEADS_TEST_LOAD_VAR=hello_from_env, got %q", got)
		}
		os.Unsetenv("BEADS_TEST_LOAD_VAR")
	})

	t.Run("shell env takes precedence over .env", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, ".env")
		if err := os.WriteFile(envFile, []byte("BEADS_TEST_PRECEDENCE=from_file\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_TEST_PRECEDENCE", "from_shell")

		loadBeadsEnvFile(dir)

		if got := os.Getenv("BEADS_TEST_PRECEDENCE"); got != "from_shell" {
			t.Errorf("expected shell env to win, got %q", got)
		}
	})

	t.Run("no-op when .env does not exist", func(t *testing.T) {
		dir := t.TempDir()
		// Should not panic or error
		loadBeadsEnvFile(dir)
	})

	t.Run("no-op when beadsDir is empty", func(t *testing.T) {
		// Should not panic or error
		loadBeadsEnvFile("")
	})

	// A .env-provided BEADS_DIR is user selection on this broad loader too, not
	// only via loadBeadsSelectionEnvFile: that loader early-returns whenever
	// BEADS_DB or BD_DB is already exported (loadSelectionEnvironment), and this
	// one then imports the same .env line. One .env line must mean one thing.
	t.Run("marks selection provenance when .env sets an unset BEADS_DIR", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), ".beads")
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("BEADS_DIR="+target+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", "")
		os.Unsetenv("BEADS_DIR")
		setBeadsDirStartupProvenanceForTest(t, false)

		loadBeadsEnvFile(dir)

		if got := os.Getenv("BEADS_DIR"); got != target {
			t.Fatalf("BEADS_DIR = %q, want %q", got, target)
		}
		if !beadsDirProvidedAtStartup {
			t.Fatal("a .env-provided BEADS_DIR must record selection provenance, or role detection treats the same .env line as mere discovery")
		}
	})

	// The mirror image: prepareSelectedCommandContext rebinds BEADS_DIR for
	// every command and then calls this loader, so an already-set BEADS_DIR must
	// NOT be read as user selection — internal rebinds are discovery.
	t.Run("leaves provenance alone when BEADS_DIR was already set", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("BEADS_DIR=/from/env/file\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", "/already/selected")
		setBeadsDirStartupProvenanceForTest(t, false)

		loadBeadsEnvFile(dir)

		if got := os.Getenv("BEADS_DIR"); got != "/already/selected" {
			t.Fatalf("BEADS_DIR = %q, want the pre-set value to win", got)
		}
		if beadsDirProvidedAtStartup {
			t.Fatal("an internal BEADS_DIR rebind must not be promoted to user selection")
		}
	})
}
