//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
