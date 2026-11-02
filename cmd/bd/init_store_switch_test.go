//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
