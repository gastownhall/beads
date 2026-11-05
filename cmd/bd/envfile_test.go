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
		name    string
		user    string
		project string
		want    string
	}{
		{name: "user config", user: "mail:\n  delegate: trusted mail\n", project: "mail:\n  delegate: untrusted mail\n", want: "trusted mail"},
		{name: "project config ignored", project: "mail:\n  delegate: untrusted mail\n"},
		{name: "no config"},
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
	for _, tc := range []struct {
		key    string
		reason string
	}{
		{"BEADS_DOLT_CREDENTIAL_COMMAND", "command settings must come from your shell environment or user config"},
		{"beads_dolt_credential_command", "command settings must come from your shell environment or user config"},
		{"BEADS_HTTP_TOKEN_COMMAND", "command settings must come from your shell environment or user config"},
		{"BEADS_MAIL_DELEGATE", "command settings must come from your shell environment or user config"},
		{"Beads_Mail_Delegate", "command settings must come from your shell environment or user config"},
		{"BD_MAIL_DELEGATE", "command settings must come from your shell environment or user config"},
		{"EDITOR", "command settings must come from your shell environment or user config"},
		{"VISUAL", "command settings must come from your shell environment or user config"},
		{"BD_PAGER", "command settings must come from your shell environment or user config"},
		{"PAGER", "command settings must come from your shell environment or user config"},
		{"BEADS_DOLT_BIN", "command settings must come from your shell environment or user config"},
		{"BEADS_DOLT_SERVER_HOST", "credential target settings must come from your shell environment or user config"},
		{"BEADS_CREDENTIALS_FILE", "credential target settings must come from your shell environment or user config"},
		{"BEADS_CENTRAL_CONFIG", "credential target settings must come from your shell environment or user config"},
		{"BEADS_HTTP_CA_FILE", "credential target settings must come from your shell environment or user config"},
		// The DOLT_* namespace is owned by the dolt binary and carries
		// command-/path-valued members bd cannot control, so the prefix is NOT
		// admitted: DOLT_SSH_COMMAND runs a command in dolt's ssh transport,
		// DOLT_ROOT_PATH redirects dolt's global config (which can name an
		// executable credential helper), and a bare DOLT_* key is refused too.
		{"DOLT_SSH_COMMAND", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"DOLT_ROOT_PATH", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"DOLT_PAGER", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"dolt_ssh_command", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"GIT_CONFIG_COUNT", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"GIT_SSH_COMMAND", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"GIT_ASKPASS", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"SSH_ASKPASS", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"LD_PRELOAD", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"DYLD_INSERT_LIBRARIES", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		// Only the exact orchestrator key GT_ROOT is admitted; its near
		// misses and the rest of the GT_* namespace stay refused.
		{"GT_TOWN_ROOT", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
		{"GT_ROOT_X", "only BEADS_*, BD_*, GT_ROOT, and the Dolt remote credential variables (DOLT_REMOTE_USER, DOLT_REMOTE_PASSWORD) are allowed in .beads/.env"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.key+"=untrusted\nBEADS_TEST_NORMAL=normal\n"), 0600); err != nil {
				t.Fatal(err)
			}
			upper := strings.ToUpper(tc.key)
			t.Setenv(tc.key, "")
			os.Unsetenv(tc.key)
			t.Setenv(upper, "")
			os.Unsetenv(upper)
			t.Setenv("BEADS_TEST_NORMAL", "")
			os.Unsetenv("BEADS_TEST_NORMAL")
			warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })
			if _, ok := os.LookupEnv(tc.key); ok {
				t.Errorf("%s was imported", tc.key)
			}
			if _, ok := os.LookupEnv(upper); ok {
				t.Errorf("%s was imported as %s", tc.key, upper)
			}
			if os.Getenv("BEADS_TEST_NORMAL") != "normal" {
				t.Error("normal setting not imported")
			}
			want := "beads: ignoring " + tc.key + " from .beads/.env (" + tc.reason + ")\n"
			if !strings.Contains(warning, want) {
				t.Errorf("warning = %q, want %q", warning, want)
			}
			t.Setenv(upper, "trusted")
			captureStderr(t, func() { loadBeadsEnvFile(dir) })
			if os.Getenv(upper) != "trusted" {
				t.Error("shell setting changed")
			}
		})
	}
}

// TestLoadBeadsEnvFileAdmitsDoltRemoteCredentials pins that narrowing the
// DOLT_* prefix to AllowedDoltEnvKeys did not break the documented
// per-project Dolt remote credential use case (GH#2520): the two explicitly
// allowed DOLT_* keys still import, while a sibling DOLT_* key does not.
func TestLoadBeadsEnvFileAdmitsDoltRemoteCredentials(t *testing.T) {
	for _, key := range []string{"DOLT_REMOTE_USER", "DOLT_REMOTE_PASSWORD"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=from-env\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, "")
			os.Unsetenv(key)
			warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })
			if got := os.Getenv(key); got != "from-env" {
				t.Errorf("%s = %q, want %q (allowed Dolt remote credential key must import)", key, got, "from-env")
			}
			if strings.Contains(warning, "ignoring "+key) {
				t.Errorf("%s was warned about but should be admitted: %q", key, warning)
			}
		})
	}
}

// TestLoadBeadsEnvFileAdmitsGTRoot pins that the orchestrator root Gas City
// writes into a rig's .beads/.env (`gc rig add`) still imports, without the
// "ignoring" warning, so bd run in a rig keeps finding the city's formulas.
// Shell environment still wins over the file.
func TestLoadBeadsEnvFileAdmitsGTRoot(t *testing.T) {
	for _, key := range []string{"GT_ROOT", "gt_root"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=/city/root\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GT_ROOT", "")
			os.Unsetenv("GT_ROOT")
			t.Setenv(config.GTRootFromProjectEnvMarker, "")
			os.Unsetenv(config.GTRootFromProjectEnvMarker)
			warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })
			if got := os.Getenv("GT_ROOT"); got != "/city/root" {
				t.Errorf("GT_ROOT = %q, want %q (orchestrator root must import from .beads/.env)", got, "/city/root")
			}
			// Provenance is recorded so formula writes stay out of it.
			if !config.GTRootFromProjectEnv() {
				t.Error("GTRootFromProjectEnv() = false after importing GT_ROOT from .beads/.env")
			}
			if strings.Contains(strings.ToUpper(warning), "IGNORING GT_ROOT") {
				t.Errorf("GT_ROOT was warned about but should be admitted: %q", warning)
			}
		})
	}
	t.Run("shell env wins", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GT_ROOT=/from/file\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GT_ROOT", "/from/shell")
		t.Setenv(config.GTRootFromProjectEnvMarker, "")
		os.Unsetenv(config.GTRootFromProjectEnvMarker)
		loadBeadsEnvFile(dir)
		if got := os.Getenv("GT_ROOT"); got != "/from/shell" {
			t.Errorf("GT_ROOT = %q, want the shell value %q", got, "/from/shell")
		}
		if config.GTRootFromProjectEnv() {
			t.Error("GTRootFromProjectEnv() = true for a shell-exported GT_ROOT")
		}
	})
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
