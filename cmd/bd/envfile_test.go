package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/testutil/credentialcmd"
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
		// A key whose name marks it as executable is refused by suffix, so a
		// command setting added later needs no entry in CommandEnvKeys.
		{"BEADS_DOLT_PASSWORD_COMMAND", "command settings must come from your shell environment or user config"},
		{"BD_EXAMPLE_HELPER_COMMAND", "command settings must come from your shell environment or user config"},
		{"beads_example_command", "command settings must come from your shell environment or user config"},
		{"BEADS_EXAMPLE_BIN", "command settings must come from your shell environment or user config"},
		{"BD_EXAMPLE_BINARY", "command settings must come from your shell environment or user config"},
		// The http backend's bearer outranks every credential source the
		// caller configured, for the server the workspace names.
		{"BEADS_HTTP_TOKEN", "credential settings must come from your shell environment or user config"},
		{"beads_http_token", "credential settings must come from your shell environment or user config"},
		// The plaintext grant would send a bearer over http in the clear, and
		// the serve token file decides which bearers bd serve accepts.
		{"BEADS_HTTP_ALLOW_INSECURE", "credential target settings must come from your shell environment or user config"},
		{"BEADS_SERVE_TOKEN_FILE", "credential target settings must come from your shell environment or user config"},
		{"BEADS_DOLT_SERVER_HOST", "credential target settings must come from your shell environment or user config"},
		{"BEADS_CREDENTIALS_FILE", "credential target settings must come from your shell environment or user config"},
		{"BEADS_CENTRAL_CONFIG", "credential target settings must come from your shell environment or user config"},
		{"BEADS_HTTP_CA_FILE", "credential target settings must come from your shell environment or user config"},
		// A path bd creates, truncates or appends to: a committed value would
		// choose a file on the caller's machine for bd to overwrite.
		{"BEADS_MEM_PROFILE", "output file settings must come from your shell environment"},
		{"beads_mem_profile", "output file settings must come from your shell environment"},
		{"BEADS_MEM_STATS", "output file settings must come from your shell environment"},
		{"BEADS_PROXIED_SERVER_LOG", "output file settings must come from your shell environment"},
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

// TestLoadBeadsEnvFileRefusesVariableExpansion pins that .beads/.env values are
// taken as written. A value that a $VAR or ${VAR} reference fills in -- from
// the caller's environment or from an earlier line -- is refused with a
// notice, so a committed file cannot copy a secret out of the caller's
// environment into a setting the workspace routes elsewhere: an actor name
// written into issues, or DOLT_REMOTE_PASSWORD sent to the workspace's own
// sync.remote, which no endpoint pin covers. A dollar sign the file writes
// literally (single-quoted, escaped, or not followed by a variable name)
// still imports unchanged.
func TestLoadBeadsEnvFileRefusesVariableExpansion(t *testing.T) {
	const secret = "s3cr3t-from-the-caller-environment"
	const reason = "$VAR references are not expanded in .beads/.env; write the value literally or set it in your shell environment"
	t.Setenv("BEADS_TEST_CALLER_SECRET", secret)

	refused := []string{
		"BEADS_TEST_EXPAND_BRACED",
		"BEADS_TEST_EXPAND_BARE",
		"BEADS_TEST_EXPAND_QUOTED",
		"BEADS_TEST_EXPAND_EMBEDDED",
		"BEADS_TEST_EXPAND_UNSET",
		"BEADS_TEST_EXPAND_SIBLING",
		"DOLT_REMOTE_PASSWORD",
	}
	literal := map[string]string{
		"BEADS_TEST_SIBLING":         "sibling",
		"BEADS_TEST_LITERAL_SINGLE":  "pa$$word ${HOME}",
		"BEADS_TEST_LITERAL_ESCAPED": "a${HOME}b",
		"BEADS_TEST_LITERAL_DOLLARS": "pa$$word",
		"BEADS_TEST_LITERAL_LOWER":   "cost$lower",
	}
	for _, key := range refused {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	for key := range literal {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	dir := t.TempDir()
	content := strings.Join([]string{
		"BEADS_TEST_EXPAND_BRACED=${BEADS_TEST_CALLER_SECRET}",
		"BEADS_TEST_EXPAND_BARE=$BEADS_TEST_CALLER_SECRET",
		`BEADS_TEST_EXPAND_QUOTED="prefix-${BEADS_TEST_CALLER_SECRET}"`,
		"BEADS_TEST_EXPAND_EMBEDDED=a${BEADS_TEST_CALLER_SECRET}b",
		"BEADS_TEST_EXPAND_UNSET=${BEADS_TEST_NOT_SET_ANYWHERE}",
		"BEADS_TEST_SIBLING=sibling",
		"BEADS_TEST_EXPAND_SIBLING=${BEADS_TEST_SIBLING}",
		"DOLT_REMOTE_PASSWORD=${BEADS_TEST_CALLER_SECRET}",
		`BEADS_TEST_LITERAL_SINGLE='pa$$word ${HOME}'`,
		`BEADS_TEST_LITERAL_ESCAPED="a\${HOME}b"`,
		"BEADS_TEST_LITERAL_DOLLARS=pa$$word",
		"BEADS_TEST_LITERAL_LOWER=cost$lower",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })

	for _, kv := range os.Environ() {
		if strings.Contains(kv, secret) && !strings.HasPrefix(kv, "BEADS_TEST_CALLER_SECRET=") {
			t.Errorf(".beads/.env copied the caller's secret into the environment: %q", kv)
		}
	}
	for _, key := range refused {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q was imported; a value filled in by expansion must be refused", key, value)
		}
		want := "beads: ignoring " + key + " from .beads/.env (" + reason + ")\n"
		if !strings.Contains(warning, want) {
			t.Errorf("warning = %q, want it to contain %q", warning, want)
		}
	}
	for key, want := range literal {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want the literal %q", key, got, want)
		}
		if strings.Contains(warning, "ignoring "+key+" ") {
			t.Errorf("%s was refused but its value is literal: %q", key, warning)
		}
	}
}

// TestLoadBeadsSelectionEnvFileRefusesVariableExpansion pins the same rule on
// the early selector loader: a BEADS_DIR whose value expansion fills in is not
// imported (and records no selection provenance), while a literal sibling
// selector still is. The broad loader reports the refusal, so this one stays
// quiet.
func TestLoadBeadsSelectionEnvFileRefusesVariableExpansion(t *testing.T) {
	t.Setenv("BEADS_TEST_SELECTION_TARGET", filepath.Join(t.TempDir(), ".beads"))
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	setBeadsDirStartupProvenanceForTest(t, false)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("BEADS_DIR=${BEADS_TEST_SELECTION_TARGET}\nBEADS_DB=/literal/.beads/dolt\n"), 0600); err != nil {
		t.Fatal(err)
	}

	warning := captureStderr(t, func() { loadBeadsSelectionEnvFile(dir) })

	if value, ok := os.LookupEnv("BEADS_DIR"); ok {
		t.Errorf("BEADS_DIR = %q was imported from an expanded value", value)
	}
	if beadsDirProvidedAtStartup {
		t.Error("a refused BEADS_DIR must not record selection provenance")
	}
	if got := os.Getenv("BEADS_DB"); got != "/literal/.beads/dolt" {
		t.Errorf("BEADS_DB = %q, want the literal selector imported", got)
	}
	if warning != "" {
		t.Errorf("selector loader printed %q; the broad loader owns the notice", warning)
	}
}

// TestPlantedEnvFileCannotRunCredentialCommand reproduces, end to end, the
// attack the .beads/.env filter exists for: a cloned repository commits a
// .beads/.env whose bearer-token command is scoped to the server its own
// http_target.json would name. Loading the file and resolving the http
// backend's bearer for that server must not run the command.
func TestPlantedEnvFileCannotRunCredentialCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "credential-command-ran")
	command := credentialcmd.Marker(t, marker)
	for _, key := range []string{httpclient.TokenEnv, httpclient.TokenCommandEnv} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	dir := t.TempDir()
	planted := httpclient.TokenCommandEnv + "='127.0.0.1=" + command + "'\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(planted), 0600); err != nil {
		t.Fatal(err)
	}

	warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })

	if value, ok := os.LookupEnv(httpclient.TokenCommandEnv); ok {
		t.Errorf("%s = %q was imported from a planted .beads/.env", httpclient.TokenCommandEnv, value)
	}
	if !strings.Contains(warning, "beads: ignoring "+httpclient.TokenCommandEnv+" from .beads/.env (") {
		t.Errorf("warning = %q, want the refusal notice", warning)
	}
	server, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := httpclient.NewBearerProvider(server).Authorize(context.Background(), req); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	credentialcmd.AssertMarkerAbsent(t, marker)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want no credential from a planted .beads/.env", got)
	}
}

// TestPlantedEnvFileCannotChooseDiagnosticsOutputFiles reproduces a committed
// .beads/.env that names a file of the caller's as bd's heap profile and
// MemStats destinations. bd creates both files when a command exits, which
// truncates whatever was there, so importing either line would let a cloned
// repository overwrite any file the caller can write. Loading the file and
// writing the diagnostics must leave the caller's files untouched.
func TestPlantedEnvFileCannotChooseDiagnosticsOutputFiles(t *testing.T) {
	const notes = "the caller's own notes\n"
	victims := t.TempDir()
	profileVictim := filepath.Join(victims, "notes.txt")
	statsVictim := filepath.Join(victims, "todo.txt")
	for _, victim := range []string{profileVictim, statsVictim} {
		if err := os.WriteFile(victim, []byte(notes), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"BEADS_MEM_PROFILE", "BEADS_MEM_STATS", "BD_IS_FLUSHER"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	dir := t.TempDir()
	planted := "BEADS_MEM_PROFILE=" + profileVictim + "\nBEADS_MEM_STATS=" + statsVictim + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(planted), 0600); err != nil {
		t.Fatal(err)
	}

	warning := captureStderr(t, func() { loadBeadsEnvFile(dir) })
	writeMemDiagnostics("")

	for _, key := range []string{"BEADS_MEM_PROFILE", "BEADS_MEM_STATS"} {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q was imported from a planted .beads/.env", key, value)
		}
		if want := "beads: ignoring " + key + " from .beads/.env (output file settings must come from your shell environment)\n"; !strings.Contains(warning, want) {
			t.Errorf("warning = %q, want it to contain %q", warning, want)
		}
	}
	for _, victim := range []string{profileVictim, statsVictim} {
		got, err := os.ReadFile(victim)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != notes {
			t.Errorf("%s was overwritten through a planted .beads/.env: %q", victim, got)
		}
	}
}
