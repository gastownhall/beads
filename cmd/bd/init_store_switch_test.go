//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// writeInitStoreSwitchMetadata writes metadata.json into a fresh .beads
// directory and returns that directory.
func writeInitStoreSwitchMetadata(t *testing.T, metadata string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// TestRunInitReinitPreflightStoreSwitch pins the switch decision without a
// store: a switch is refused without the token before anything is counted,
// and a request that keeps the recorded store falls through to the count.
func TestRunInitReinitPreflightStoreSwitch(t *testing.T) {
	const (
		embeddedFi = `{"database":"dolt","backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`
		serverLa   = `{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"la"}`
		proxiedLa  = `{"database":"dolt","backend":"dolt","dolt_mode":"proxied-server","dolt_database":"la"}`
		noModeLa   = `{"database":"dolt","backend":"dolt","dolt_database":"la"}`
	)
	tests := []struct {
		name            string
		metadata        string
		database        string
		targetSQLServer bool
		wantSwitch      bool
	}{
		{name: "server recorded, embedded requested", metadata: serverLa, wantSwitch: true},
		{name: "proxied recorded, embedded requested", metadata: proxiedLa, wantSwitch: true},
		{name: "embedded recorded, server requested", metadata: embeddedFi, targetSQLServer: true, wantSwitch: true},
		{name: "server recorded, server requested", metadata: serverLa, targetSQLServer: true},
		{name: "proxied recorded, server requested", metadata: proxiedLa, targetSQLServer: true},
		{name: "embedded recorded, embedded requested", metadata: embeddedFi},
		{name: "no recorded mode skips the family axis", metadata: noModeLa},
		{name: "no recorded mode, other database", metadata: noModeLa, database: "other", wantSwitch: true},
		{name: "same family, other database", metadata: embeddedFi, database: "other", wantSwitch: true},
		{name: "same family, case-only database difference", metadata: embeddedFi, database: "FI", wantSwitch: true},
		{name: "same family, same database", metadata: embeddedFi, database: "fi"},
		{name: "non-dolt backend", metadata: `{"backend":"sqlite","dolt_mode":"server"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			counted := false
			p := initReinitPreflight{
				reinitLocal:     true,
				beadsDir:        writeInitStoreSwitchMetadata(t, tt.metadata),
				prefix:          "la",
				database:        tt.database,
				nonInteractive:  true,
				targetSQLServer: tt.targetSQLServer,
				countIssues: func() (int, error) {
					counted = true
					return 0, nil
				},
				stderr: &stderr,
			}
			err := p.run()
			if !tt.wantSwitch {
				if err != nil {
					t.Fatalf("no switch: want nil, got %v\n%s", err, stderr.String())
				}
				if !counted {
					t.Fatal("no switch: the existing issue-count confirmation was skipped")
				}
				return
			}
			var exitErr *exitError
			if !errors.As(err, &exitErr) || exitErr.Code != ExitDestroyTokenMissing {
				t.Fatalf("switch without a token: want exit %d, got %v\n%s", ExitDestroyTokenMissing, err, stderr.String())
			}
			if counted {
				t.Fatal("switch: the old store was counted before refusing")
			}
		})
	}
}

// TestInitReinitLocalRefusesEngineFamilySwitch: a workspace recorded as
// server mode, re-initialized with an explicit embedded mode, would have been
// moved onto a new embedded database without confirmation whenever the
// server store held no issues or could not be counted. No server runs here:
// the refusal must not depend on reaching the recorded store.
func TestInitReinitLocalRefusesEngineFamilySwitch(t *testing.T) {
	bd := buildBDUnderTest(t)
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	// The recorded database's own directory satisfies the earlier
	// missing-database guard (tier 1), and the current-era witness the
	// legacy-upgrade guard, so the switch preflight is what decides.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "la", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"la","project_id":"existing-project-id"}`)
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bd, "init", "--reinit-local", "--prefix", "la", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = projectDir
	cmd.Env = append(initGuardE2EEnv(t, freeLoopbackPort(t)), "BEADS_DOLT_SERVER_MODE=0")
	stdout, stderr, err := runCommandBuffers(t, cmd)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitDestroyTokenMissing {
		t.Fatalf("reinit of a server workspace in embedded mode without a destroy token: want exit %d, got %v\nstdout:\n%s\nstderr:\n%s",
			ExitDestroyTokenMissing, err, stdout.String(), stderr.String())
	}

	got, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, metadata) {
		t.Errorf("metadata.json changed by a refused store switch:\nbefore:\n%s\nafter:\n%s", metadata, got)
	}
	requireNoFile(t, filepath.Join(beadsDir, "embeddeddolt"))
}

// TestRunInitReinitPreflightStoreSwitchPhrase: on an interactive terminal a
// switch is confirmed by typing a phrase naming the store left behind, and a
// mismatch or end of input refuses it with exit 11. Neither path counts.
func TestRunInitReinitPreflightStoreSwitchPhrase(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		input    string
		wantCode int // 0 accepts
	}{
		{name: "phrase names the recorded database", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, input: "leave fi behind\n"},
		{name: "surrounding space is ignored", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, input: "  leave fi behind  \n"},
		{name: "no recorded database: phrase names the recorded mode", metadata: `{"backend":"dolt","dolt_mode":"embedded"}`, input: "leave embedded behind\n"},
		{name: "mismatched phrase", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, input: "leave other behind\n", wantCode: ExitLocalExistsRefused},
		{name: "a bare yes is not the phrase", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, input: "yes\n", wantCode: ExitLocalExistsRefused},
		{name: "end of input", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, input: "", wantCode: ExitLocalExistsRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			p := initReinitPreflight{
				reinitLocal:     true,
				beadsDir:        writeInitStoreSwitchMetadata(t, tt.metadata),
				prefix:          "fi",
				targetSQLServer: true,
				countIssues: func() (int, error) {
					t.Fatal("a store switch counted the old store")
					return 0, nil
				},
				isTerminal: func() bool { return true },
				stdin:      bytes.NewBufferString(tt.input),
				stderr:     &stderr,
			}
			err := p.run()
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("matching phrase: want nil, got %v\n%s", err, stderr.String())
				}
				return
			}
			var exitErr *exitError
			if !errors.As(err, &exitErr) || exitErr.Code != tt.wantCode {
				t.Fatalf("want exit %d, got %v\n%s", tt.wantCode, err, stderr.String())
			}
		})
	}
}

// requireStoreSwitchDiagnostic checks what a store-switch confirmation tells
// the user, whichever way it ends: both stores, what stops being read, how to
// keep the recorded store, and the safe commands, and never the destroy
// token's value or a destructive command to paste.
func requireStoreSwitchDiagnostic(t *testing.T, out string, want ...string) {
	t.Helper()
	want = append(want,
		"left in place",
		"issues",
		"history",
		"types.custom",
		"status.custom",
		"bd backup",
		"bd help init-safety",
	)
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("store-switch diagnostic does not mention %q:\n%s", w, out)
		}
	}
	for _, banned := range []string{"DESTROY-", "bd init --", "--reinit-local", "--force"} {
		if strings.Contains(out, banned) {
			t.Errorf("store-switch diagnostic contains %q:\n%s", banned, out)
		}
	}
}

// TestInitReinitLocalStoreSwitchDiagnosticUnderQuiet: --quiet does not
// silence the switch diagnostic, which names the recorded and requested
// family and database and what to remove to keep the recorded store.
func TestInitReinitLocalStoreSwitchDiagnosticUnderQuiet(t *testing.T) {
	bd := buildBDUnderTest(t)
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "la", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := `{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"la","project_id":"existing-project-id"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bd, "init", "--quiet", "--reinit-local", "--prefix", "la", "--database", "other",
		"--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = projectDir
	cmd.Env = append(initGuardE2EEnv(t, freeLoopbackPort(t)), "BEADS_DOLT_SERVER_MODE=0")
	_, stderr, err := runCommandBuffers(t, cmd)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitDestroyTokenMissing {
		t.Fatalf("want exit %d, got %v\n%s", ExitDestroyTokenMissing, err, stderr.String())
	}
	requireStoreSwitchDiagnostic(t, stderr.String(),
		"Recorded engine family:  server",
		"Requested engine family: embedded",
		"Recorded database:       la",
		"Requested database:      other",
		"BEADS_DOLT_SERVER_MODE",
		"--database",
	)
}

// TestRunInitReinitPreflightStoreSwitchAcceptedDiagnostic: an accepted
// switch still names the store it leaves behind, without echoing the token.
func TestRunInitReinitPreflightStoreSwitchAcceptedDiagnostic(t *testing.T) {
	var stderr bytes.Buffer
	p := initReinitPreflight{
		reinitLocal:     true,
		beadsDir:        writeInitStoreSwitchMetadata(t, `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`),
		prefix:          "fi",
		destroyToken:    FormatDestroyToken("fi"),
		nonInteractive:  true,
		targetSQLServer: true,
		countIssues: func() (int, error) {
			t.Fatal("a store switch counted the old store")
			return 0, nil
		},
		stderr: &stderr,
	}
	if err := p.run(); err != nil {
		t.Fatalf("matching token: want nil, got %v\n%s", err, stderr.String())
	}
	requireStoreSwitchDiagnostic(t, stderr.String(),
		"Recorded engine family:  embedded",
		"Requested engine family: server",
		"Recorded database:       fi",
		"Requested database:      fi (unchanged)",
		"--server",
	)
}

// TestInitReinitLocalKeepsManagedServerStore locks in the Gas City shape of
// #3495 at the store level: a workspace provisioned against an external
// server, recorded as server mode with database-stored custom types, and
// re-initialized by the reporter's command with no mode of its own. The
// reinit must reuse the recorded server database, keep the custom types, and
// create no embedded database beside it.
func TestInitReinitLocalKeepsManagedServerStore(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env,
		"init", "--quiet", "--server", "--external",
		"--server-host", "127.0.0.1", "--server-port", strconv.Itoa(testDoltServerPort),
		"--database", database, "--prefix", "la", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Fatalf("provisioning bd init --server --external failed: %v\n%s", err, out)
	}
	if out, err := runExternalServerBD(t, repoDir, env, "config", "set", "types.custom", "molecule,convoy"); err != nil {
		t.Fatalf("bd config set types.custom: %v\n%s", err, out)
	}

	out, err = runExternalServerBD(t, repoDir, env,
		"init", "--reinit-local", "--prefix", "la", "--database", database, "--non-interactive", "--role", "maintainer")
	if err != nil {
		t.Fatalf("same-store reinit of a server workspace must succeed: %v\n%s", err, out)
	}
	if strings.Contains(out, "switch this workspace to another store") {
		t.Errorf("same-store reinit was treated as a store switch:\n%s", out)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json after reinit: cfg=%v err=%v", cfg, err)
	}
	if cfg.DoltMode != configfile.DoltModeServer || cfg.DoltDatabase != database {
		t.Errorf("recorded store after reinit: dolt_mode=%q dolt_database=%q, want %q and %q",
			cfg.DoltMode, cfg.DoltDatabase, configfile.DoltModeServer, database)
	}
	requireNoFile(t, filepath.Join(beadsDir, "embeddeddolt"))
	out, err = runExternalServerBD(t, repoDir, env, "config", "get", "types.custom")
	if err != nil || !strings.Contains(out, "molecule,convoy") {
		t.Errorf("types.custom after a same-store reinit: err=%v, want molecule,convoy in:\n%s", err, out)
	}
}

// unreadableStdin fails the test if a confirmation reads an answer.
type unreadableStdin struct{ t *testing.T }

func (r unreadableStdin) Read([]byte) (int, error) {
	r.t.Error("a non-interactive confirmation read an interactive answer")
	return 0, errors.New("stdin must not be read")
}

// TestRunInitReinitPreflightNonInteractiveOnTTY: resolved non-interactive
// mode (--non-interactive, BD_NON_INTERACTIVE, CI) wins over a terminal on
// stdin, for the store-switch confirmation and the existing populated-store
// one alike: no answer is read, and a missing token exits 12.
func TestRunInitReinitPreflightNonInteractiveOnTTY(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		database string
		count    int
	}{
		{name: "store switch", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, database: "other"},
		{name: "populated store", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`, count: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			p := initReinitPreflight{
				reinitLocal:    true,
				beadsDir:       writeInitStoreSwitchMetadata(t, tt.metadata),
				prefix:         "fi",
				database:       tt.database,
				nonInteractive: true,
				isTerminal:     func() bool { return true },
				countIssues:    func() (int, error) { return tt.count, nil },
				stdin:          unreadableStdin{t},
				stderr:         &stderr,
			}
			err := p.run()
			var exitErr *exitError
			if !errors.As(err, &exitErr) || exitErr.Code != ExitDestroyTokenMissing {
				t.Fatalf("non-interactive on a TTY without a token: want exit %d, got %v\n%s", ExitDestroyTokenMissing, err, stderr.String())
			}
		})
	}
}

// TestRunInitReinitPreflightCountErrorWarns: when the recorded store cannot
// be counted, the existing policy still proceeds, but says so instead of
// passing silently, and does not call the store empty. --quiet silences it.
func TestRunInitReinitPreflightCountErrorWarns(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		var stderr bytes.Buffer
		p := initReinitPreflight{
			reinitLocal:    true,
			beadsDir:       writeInitStoreSwitchMetadata(t, `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`),
			prefix:         "fi",
			nonInteractive: true,
			quiet:          quiet,
			countIssues:    func() (int, error) { return 0, errors.New("dial tcp 127.0.0.1:1: connection refused\x1b[2J") },
			stderr:         &stderr,
		}
		if err := p.run(); err != nil {
			t.Fatalf("quiet=%v: a count error keeps the existing policy: want nil, got %v", quiet, err)
		}
		out := stderr.String()
		if quiet {
			if out != "" {
				t.Errorf("quiet=%v: want no output, got:\n%s", quiet, out)
			}
			continue
		}
		if !strings.Contains(out, "could not count") || !strings.Contains(out, "connection refused") {
			t.Errorf("quiet=%v: count-error warning missing or without its cause:\n%s", quiet, out)
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("quiet=%v: count-error warning printed a raw escape byte:\n%q", quiet, out)
		}
		if strings.Contains(out, "empty") {
			t.Errorf("quiet=%v: count-error warning calls a store it could not read empty:\n%s", quiet, out)
		}
	}
}
