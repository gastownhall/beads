//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// TestRunInitReinitPreflightRecordedMetadata pins how the preflight reads
// metadata.json: an absent workspace is not a switch, a present but malformed
// file is an error before anything is counted (never an empty workspace that
// fails open), and an unknown recorded mode skips only the family axis.
func TestRunInitReinitPreflightRecordedMetadata(t *testing.T) {
	run := func(t *testing.T, beadsDir, database string, targetSQLServer bool) (error, bool) {
		t.Helper()
		var stderr bytes.Buffer
		counted := false
		p := initReinitPreflight{
			reinitLocal:     true,
			beadsDir:        beadsDir,
			prefix:          "la",
			database:        database,
			nonInteractive:  true,
			targetSQLServer: targetSQLServer,
			countIssues: func() (int, error) {
				counted = true
				return 0, nil
			},
			stderr: &stderr,
		}
		return p.run(), counted
	}

	t.Run("absent workspace is not a switch", func(t *testing.T) {
		err, counted := run(t, filepath.Join(t.TempDir(), "nope", ".beads"), "other", true)
		if err != nil {
			t.Fatalf("missing workspace: %v", err)
		}
		if !counted {
			t.Fatal("missing workspace: the existing issue-count confirmation was skipped")
		}
	})

	t.Run("malformed metadata is an error before anything is counted", func(t *testing.T) {
		err, counted := run(t, writeInitStoreSwitchMetadata(t, "{not json"), "", false)
		if err == nil {
			t.Fatal("malformed metadata.json must be an error, not an empty workspace")
		}
		if counted {
			t.Fatal("malformed metadata reached the recorded store")
		}
	})

	unknownMode := `{"database":"dolt","backend":"dolt","dolt_mode":"experimental","dolt_database":"la"}`
	for _, target := range []bool{false, true} {
		err, counted := run(t, writeInitStoreSwitchMetadata(t, unknownMode), "", target)
		if err != nil || !counted {
			t.Errorf("unknown recorded mode, targetSQLServer=%v: want no switch, got err=%v counted=%v", target, err, counted)
		}
	}
	err, counted := run(t, writeInitStoreSwitchMetadata(t, unknownMode), "other", false)
	var exitErr *exitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDestroyTokenMissing || counted {
		t.Errorf("unknown recorded mode, other database: want exit %d without counting, got err=%v counted=%v", ExitDestroyTokenMissing, err, counted)
	}

	spacedServer := `{"database":"dolt","backend":"dolt","dolt_mode":"  SERVER  ","dolt_database":"la"}`
	if err, counted := run(t, writeInitStoreSwitchMetadata(t, spacedServer), "", true); err != nil || !counted {
		t.Errorf("recorded mode is normalized before comparing: server target got err=%v counted=%v", err, counted)
	}
	if err, _ := run(t, writeInitStoreSwitchMetadata(t, spacedServer), "", false); !errors.As(err, &exitErr) {
		t.Errorf("recorded mode is normalized before comparing: embedded target got %v, want a refused switch", err)
	}
}

// TestDetectInitStoreSwitchKeepsUnchangedAxes: the switch carries the
// recorded family and database even on the axis that did not change, so the
// diagnostic can name both stores.
func TestDetectInitStoreSwitchKeepsUnchangedAxes(t *testing.T) {
	server := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: "server", DoltDatabase: "la"}
	sw := detectInitStoreSwitch(server, "", false)
	if !sw.familyChanged || sw.databaseChanged {
		t.Fatalf("family-only switch = %+v", sw)
	}
	if sw.recordedDatabase != "la" || sw.recordedMode != "server" {
		t.Errorf("family-only switch dropped the recorded store: %+v", sw)
	}

	embedded := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: "embedded", DoltDatabase: "fi"}
	sw = detectInitStoreSwitch(embedded, "other", false)
	if sw.familyChanged || !sw.databaseChanged {
		t.Fatalf("database-only switch = %+v", sw)
	}
	if sw.recordedMode != "embedded" || sw.recordedDatabase != "fi" || sw.requestedDatabase != "other" {
		t.Errorf("database-only switch dropped the recorded family: %+v", sw)
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

// TestRunInitReinitPreflightStoreSwitchTokenNeverCounts: an accepted destroy
// token authorizes the switch once, without counting the old store or
// following it with the count-based confirmation.
func TestRunInitReinitPreflightStoreSwitchTokenNeverCounts(t *testing.T) {
	var stderr bytes.Buffer
	p := initReinitPreflight{
		reinitLocal:    true,
		beadsDir:       writeInitStoreSwitchMetadata(t, `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`),
		prefix:         "fi",
		destroyToken:   FormatDestroyToken("fi"),
		database:       "other",
		nonInteractive: true,
		countIssues: func() (int, error) {
			t.Fatal("an authorized store switch counted the old store")
			return 7, nil
		},
		stderr: &stderr,
	}
	if err := p.run(); err != nil {
		t.Fatalf("authorized switch: %v\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "Existing issues") {
		t.Errorf("an authorized switch was followed by the count-based prompt:\n%s", stderr.String())
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
// passing silently, escapes the cause, and does not call the store empty.
// The preflight takes no --quiet input, so the warning prints under --quiet
// too.
func TestRunInitReinitPreflightCountErrorWarns(t *testing.T) {
	var stderr bytes.Buffer
	p := initReinitPreflight{
		reinitLocal:    true,
		beadsDir:       writeInitStoreSwitchMetadata(t, `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"fi"}`),
		prefix:         "fi",
		nonInteractive: true,
		countIssues:    func() (int, error) { return 0, errors.New("dial tcp 127.0.0.1:1: connection refused\x1b[2J") },
		stderr:         &stderr,
	}
	if err := p.run(); err != nil {
		t.Fatalf("a count error keeps the existing policy: want nil, got %v", err)
	}
	out := stderr.String()
	if !strings.Contains(out, "could not count") || !strings.Contains(out, "connection refused") {
		t.Errorf("count-error warning missing or without its cause:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("count-error warning printed a raw escape byte:\n%q", out)
	}
	if strings.Contains(out, "empty") {
		t.Errorf("count-error warning calls a store it could not read empty:\n%s", out)
	}
}
