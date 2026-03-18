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
}
