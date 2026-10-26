package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestFindMailDelegateUsesYaml(t *testing.T) {
	t.Setenv("BEADS_MAIL_DELEGATE", "")
	t.Setenv("BD_MAIL_DELEGATE", "")
	for _, tt := range []struct {
		name        string
		user        string
		project     string
		legacy      string
		homeProject bool
		want        string
	}{
		{name: "user config", user: "mail:\n  delegate: trusted mail\n", project: "mail:\n  delegate: untrusted mail\n", want: "trusted mail"},
		{name: "project config ignored", project: "mail:\n  delegate: untrusted mail\n"},
		{name: "no config"},
		{name: "legacy user config", legacy: "mail:\n  delegate: legacy mail\n", want: "legacy mail"},
		{name: "documented user config wins over legacy", user: "mail:\n  delegate: trusted mail\n", legacy: "mail:\n  delegate: legacy mail\n", want: "trusted mail"},
		{name: "home project config ignored", homeProject: true, project: "mail:\n  delegate: untrusted mail\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			userDir := filepath.Join(home, ".config", "bd")
			if err := os.MkdirAll(userDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(userDir, "config.yaml"), []byte(tt.user), 0600); err != nil {
				t.Fatal(err)
			}
			projectDir := filepath.Join(t.TempDir(), ".beads")
			legacyDir := filepath.Join(home, ".beads")
			if err := os.MkdirAll(legacyDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacyDir, "config.yaml"), []byte(tt.legacy), 0600); err != nil {
				t.Fatal(err)
			}
			if tt.homeProject {
				projectDir = legacyDir
			}
			if err := os.MkdirAll(projectDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(tt.project), 0600); err != nil {
				t.Fatal(err)
			}
			if err := config.WithBeadsDir(projectDir, func() {
				if got := findMailDelegate(); got != tt.want {
					t.Errorf("delegate = %q, want %q", got, tt.want)
				}
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadBeadsEnvFileRejectsCommands(t *testing.T) {
	for _, key := range []string{"BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_HTTP_TOKEN_COMMAND", "BEADS_MAIL_DELEGATE", "BD_MAIL_DELEGATE", "EDITOR", "VISUAL", "BD_PAGER", "PAGER", "BEADS_DOLT_BIN"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=untrusted\nBEADS_TEST_NORMAL=normal\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, "")
			os.Unsetenv(key)
			t.Setenv("BEADS_TEST_NORMAL", "")
			os.Unsetenv("BEADS_TEST_NORMAL")
			warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })
			if _, ok := os.LookupEnv(key); ok {
				t.Errorf("%s was imported", key)
			}
			if os.Getenv("BEADS_TEST_NORMAL") != "normal" {
				t.Error("normal setting not imported")
			}
			want := "beads: ignoring " + key + " from .beads/.env (command settings must come from your shell environment or user config)\n"
			if !strings.Contains(warning, want) {
				t.Errorf("warning = %q, want %q", warning, want)
			}
			t.Setenv(key, "trusted")
			captureStderr(t, func() { loadBeadsEnvFile(dir) })
			if os.Getenv(key) != "trusted" {
				t.Error("shell setting changed")
			}
		})
	}
}

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
