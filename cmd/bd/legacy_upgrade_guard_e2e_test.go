//go:build cgo

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestLegacyUpgradeGuardAdmitsAffirmativelyEmptyServerDatabase(t *testing.T) {
	testutil.RequireDoltCLIOnly(t)
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("start native Dolt server: %v", err)
	}
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })
	database := uniqueTestDBName(t)
	// Deliberately retain a stale socket in metadata. Gas City names TCP with
	// --server-host/--server-port, so the qualification and init must use those
	// exact flags rather than silently routing through this stale endpoint.
	cfg := &configfile.Config{Database: "dolt", Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: database, DoltServerHost: "127.0.0.1", DoltServerPort: state.Port, DoltServerSocket: filepath.Join(t.TempDir(), "stale.sock")}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt.mode: server\ndolt.auto-start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE `%s`", database)); err != nil {
		t.Fatalf("create empty database: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, state.Port) })
	t.Setenv("BEADS_DIR", beadsDir)
	if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("fixture guard = %v, want legacy refusal before fix", err)
	}
	empty, err := selectedServerDatabaseIsEmpty(doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root", Database: database})
	if err != nil || !empty {
		t.Fatalf("fixture empty-database proof = (%v, %v), want (true, nil)", empty, err)
	}
	if _, qualified := qualifyEmptyServerInitWitness(beadsDir, emptyServerInitWitnessInput{
		ExplicitServer: true, ExplicitDatabase: true, Database: database, ReinitLocal: true,
		ExplicitHost: true, ServerHost: "127.0.0.1", ExplicitPort: true, ServerPort: state.Port,
	}); !qualified {
		t.Fatal("fixture did not qualify for the narrow empty-server init recovery")
	}
	// Explicit host/port flags must suppress an ambient socket all the way
	// through dolt.New's defaulting, or schema creation would be redirected
	// after the TCP database was proved empty.
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", filepath.Join(t.TempDir(), "stale.sock"))
	bd := buildBDUnderTest(t)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cmdCancel()
	cmd := exec.CommandContext(cmdCtx, bd, "init", "--force", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents", "--server", "--server-host", "127.0.0.1", "--server-port", fmt.Sprint(state.Port), "--database", database)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir, "BD_DISABLE_METRICS=1", "BEADS_DOLT_AUTO_START=0")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		witness, witnessErr := os.ReadFile(filepath.Join(beadsDir, localVersionFile))
		t.Fatalf("bd init --force empty server DB = %v, want success (witness=%q, witnessErr=%v):\n%s", err, witness, witnessErr, output.String())
	}
	got, err := os.ReadFile(filepath.Join(beadsDir, localVersionFile))
	if err != nil {
		t.Fatalf("read version witness: %v", err)
	}
	if strings.TrimSpace(string(got)) != Version {
		t.Fatalf("version witness = %q, want %q", strings.TrimSpace(string(got)), Version)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer inspectCancel()
	var tableCount int
	if err := db.QueryRowContext(inspectCtx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", database).Scan(&tableCount); err != nil {
		t.Fatalf("inspect proved database schema: %v", err)
	}
	if tableCount == 0 {
		t.Fatalf("proved database %q has no schema tables after init", database)
	}
}

func TestLegacyUpgradeGuardRefusesNonEmptyServerDatabaseWithoutWitness(t *testing.T) {
	testutil.RequireDoltCLIOnly(t)
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("start native Dolt server: %v", err)
	}
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })
	database := uniqueTestDBName(t)
	cfg := &configfile.Config{Database: "dolt", Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: database, DoltServerHost: "127.0.0.1", DoltServerPort: state.Port}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt.mode: server\ndolt.auto-start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE `%s`", database)); err != nil {
		t.Fatalf("create server database: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, state.Port) })
	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`; CREATE TABLE preserved (id INT PRIMARY KEY)", database)); err != nil {
		t.Fatalf("create preserved table: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	bd := buildBDUnderTest(t)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cmdCancel()
	cmd := exec.CommandContext(cmdCtx, bd, "init", "--force", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents", "--server", "--server-host", "127.0.0.1", "--server-port", fmt.Sprint(state.Port), "--database", database)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir, "BD_DISABLE_METRICS=1", "BEADS_DOLT_AUTO_START=0")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err == nil {
		t.Fatalf("bd init --force non-empty server DB succeeded, want legacy refusal:\n%s", output.String())
	} else if !strings.Contains(output.String(), "legacy Dolt server workspace detected") {
		t.Fatalf("bd init --force non-empty server DB output = %q, want legacy refusal", output.String())
	}
	if _, err := os.Lstat(filepath.Join(beadsDir, localVersionFile)); !os.IsNotExist(err) {
		t.Fatalf("version witness was changed for non-empty server DB: %v", err)
	}
}

func TestLegacyUpgradeGuardRefusesBeforeMutatingHistoricalWorkspace(t *testing.T) {
	bd := buildBDUnderTest(t)

	layouts := []struct {
		name       string
		wantReason string
		setup      func(t *testing.T, beadsDir string)
	}{
		{
			name:       "legacy server Dolt",
			wantReason: "legacy Dolt server workspace",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				writeFile(t, filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`))
				writeFile(t, filepath.Join(beadsDir, localVersionFile), []byte("0.62.0\n"))
				if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "metadata-less v0.9.1 SQLite",
			wantReason: "historical SQLite workspace",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				writeFile(t, filepath.Join(beadsDir, "vc.db"), []byte("SQLite format 3\x00"))
			},
		},
		{
			name:       "blank-mode old Dolt root without version witness",
			wantReason: "legacy Dolt workspace",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				writeFile(t, filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`))
				if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "metadata-less old Dolt root",
			wantReason: "legacy Dolt workspace",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	commands := []struct {
		name     string
		args     []string
		wantOK   bool
		wantJSON bool
	}{
		{name: "list", args: []string{"list", "--json", "--limit", "0", "--all"}},
		{name: "context", args: []string{"context", "--json"}},
		{name: "dolt start", args: []string{"dolt", "start"}},
		{name: "force init", args: []string{"init", "--force", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"}},
		{name: "doctor", args: []string{"doctor", "--json"}, wantOK: true, wantJSON: true},
	}

	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			repoDir := t.TempDir()
			initGitRepo(t, repoDir)
			beadsDir := filepath.Join(repoDir, ".beads")
			layout.setup(t, beadsDir)
			if err := os.Chmod(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}

			for _, tc := range commands {
				t.Run(tc.name, func(t *testing.T) {
					before := legacyUpgradeTreeDigest(t, beadsDir)
					commandCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					cmd := exec.CommandContext(commandCtx, bd, tc.args...)
					cmd.Dir = repoDir
					cmd.Env = append(os.Environ(), "BD_DISABLE_METRICS=1", "BEADS_DOLT_AUTO_START=0")
					var stdout, stderr bytes.Buffer
					cmd.Stdout = &stdout
					cmd.Stderr = &stderr
					err := cmd.Run()
					commandContextErr := commandCtx.Err()
					cancel()
					output := stdout.String() + stderr.String()
					if commandContextErr != nil {
						t.Fatalf("bd %s did not refuse before the command deadline: %v\n%s", strings.Join(tc.args, " "), commandContextErr, output)
					}
					if (err == nil) != tc.wantOK {
						t.Fatalf("bd %s error = %v, want success=%v\n%s", strings.Join(tc.args, " "), err, tc.wantOK, output)
					}
					if !strings.Contains(output, layout.wantReason) ||
						!strings.Contains(output, "explicit migration is required") {
						t.Fatalf("bd %s did not report the expected migration refusal:\n%s", strings.Join(tc.args, " "), output)
					}
					if tc.wantJSON {
						var payload map[string]any
						if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
							t.Fatalf("doctor stdout is not valid JSON: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
						}
						if payload["code"] != "legacy_upgrade_required" {
							t.Fatalf("doctor code = %v, want legacy_upgrade_required", payload["code"])
						}
						if payload["guide"] != "docs/getting-started/upgrading.md#cross-era-upgrades" {
							t.Fatalf("doctor guide = %v, want layout-specific cross-era guide", payload["guide"])
						}
						if _, misleading := payload["bridge"]; misleading {
							t.Fatalf("doctor advertised the SQLite bridge for %s", layout.name)
						}
					}
					if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
						t.Fatalf("bd %s mutated legacy source: before=%s after=%s", strings.Join(tc.args, " "), before, after)
					}
				})
			}
		})
	}
}

func TestLegacyNoStoreCommandExemptions(t *testing.T) {
	syntheticRoot := &cobra.Command{Use: "bd"}
	helpCmd := &cobra.Command{Use: "help", Run: func(*cobra.Command, []string) {}}
	completeCmd := &cobra.Command{Use: "__complete", Run: func(*cobra.Command, []string) {}}
	syntheticRoot.AddCommand(helpCmd, completeCmd)
	tests := []struct {
		name string
		cmd  *cobra.Command
	}{
		{name: "root", cmd: rootCmd},
		{name: "version", cmd: versionCmd},
		{name: "schema", cmd: schemaCmd},
		{name: "metrics", cmd: metricsCmd},
		{name: "metrics subcommand", cmd: metricsOffCmd},
		{name: "doctor", cmd: doctorCmd},
		{name: "init", cmd: initCmd},
		{name: "bootstrap", cmd: bootstrapCmd},
		{name: "legacy SQLite reader", cmd: legacySQLiteCmd},
		{name: "help", cmd: helpCmd},
		{name: "shell completion", cmd: completeCmd},
		{name: "non-runnable", cmd: &cobra.Command{Use: "group"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := guardLegacyNoStoreCommand(tt.cmd, t.TempDir()); err != nil {
				t.Fatalf("guardLegacyNoStoreCommand() = %v, want exemption", err)
			}
		})
	}

	beadsDir := t.TempDir()
	writeFile(t, filepath.Join(beadsDir, "vc.db"), []byte("SQLite format 3\x00"))
	if err := guardLegacyNoStoreCommand(contextCmd, beadsDir); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("context admission = %v, want legacy migration refusal", err)
	}
}

func TestVersionLeavesHistoricalWorkspaceUnchanged(t *testing.T) {
	bd := buildBDUnderTest(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(beadsDir, "vc.db"), []byte("SQLite format 3\x00"))
	before := legacyUpgradeTreeDigest(t, beadsDir)

	cmd := exec.Command(bd, "version")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "BD_DISABLE_METRICS=1", "BEADS_DOLT_AUTO_START=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd version failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "bd version") {
		t.Fatalf("bd version output missing version: %s", output)
	}
	if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
		t.Fatalf("bd version mutated historical source: before=%s after=%s", before, after)
	}
}

func TestLegacyBackendConfigRefusesBeforeMetadataMigration(t *testing.T) {
	bd := buildBDUnderTest(t)
	commands := [][]string{
		{"list", "--json", "--limit", "0", "--all"},
		{"init", "--force", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"},
	}

	for _, backend := range []string{"postgres", "mysql", "mystery"} {
		t.Run(backend, func(t *testing.T) {
			repoDir := t.TempDir()
			initGitRepo(t, repoDir)
			beadsDir := filepath.Join(repoDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			legacyPath := filepath.Join(beadsDir, "config.json")
			legacy := []byte("{\n  \"backend\": \"" + backend + "\"\n}\n")
			writeFile(t, legacyPath, legacy)

			for _, args := range commands {
				before := legacyUpgradeTreeDigest(t, beadsDir)
				cmd := exec.Command(bd, args...)
				cmd.Dir = repoDir
				cmd.Env = append(os.Environ(), "BD_DISABLE_METRICS=1", "BEADS_DOLT_AUTO_START=0")
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("bd %s unexpectedly accepted backend %q:\n%s", strings.Join(args, " "), backend, output)
				}
				if !strings.Contains(string(output), backend) {
					t.Fatalf("bd %s error did not identify backend %q:\n%s", strings.Join(args, " "), backend, output)
				}
				if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
					t.Fatalf("bd %s mutated legacy config: before=%s after=%s", strings.Join(args, " "), before, after)
				}
				after, readErr := os.ReadFile(legacyPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(after, legacy) {
					t.Fatalf("bd %s rewrote config.json: got %q, want %q", strings.Join(args, " "), after, legacy)
				}
				if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
					t.Fatalf("bd %s created metadata.json: %v", strings.Join(args, " "), statErr)
				}
			}
		})
	}
}

func TestBootstrapRefusesLegacyServerBeforeMigratingLegacyConfig(t *testing.T) {
	bd := buildBDUnderTest(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	legacyPath := filepath.Join(beadsDir, "config.json")
	legacy := []byte("{\n  \"backend\": \"dolt\",\n  \"dolt_mode\": \"server\"\n}\n")
	writeFile(t, legacyPath, legacy)
	writeFile(t, filepath.Join(beadsDir, localVersionFile), []byte("0.62.0\n"))
	if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}

	before := legacyUpgradeTreeDigest(t, beadsDir)
	cmd := exec.Command(bd, "bootstrap", "--dry-run", "--yes")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "BD_DISABLE_METRICS=1", "BEADS_DOLT_AUTO_START=0")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd bootstrap unexpectedly accepted historical server workspace:\n%s", output)
	}
	if !strings.Contains(string(output), "legacy Dolt server workspace") ||
		!strings.Contains(string(output), "explicit migration is required") {
		t.Fatalf("bd bootstrap did not report the expected migration refusal:\n%s", output)
	}
	if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
		t.Fatalf("bd bootstrap mutated legacy source: before=%s after=%s", before, after)
	}
	after, readErr := os.ReadFile(legacyPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, legacy) {
		t.Fatalf("bd bootstrap rewrote config.json: got %q, want %q", after, legacy)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
		t.Fatalf("bd bootstrap created metadata.json: %v", statErr)
	}
}

func TestBootstrapRefusesLegacyAncestorConfigWithoutMigratingIt(t *testing.T) {
	bd := buildBDUnderTest(t)
	root := t.TempDir()
	ancestorBeadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(ancestorBeadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(ancestorBeadsDir, "config.json")
	legacy := []byte("{\n  \"backend\": \"dolt\",\n  \"dolt_mode\": \"server\"\n}\n")
	writeFile(t, legacyPath, legacy)
	writeFile(t, filepath.Join(ancestorBeadsDir, localVersionFile), []byte("0.62.0\n"))
	if err := os.Mkdir(filepath.Join(ancestorBeadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}

	selectedBeadsDir := filepath.Join(root, "rig", ".beads")
	if err := os.MkdirAll(selectedBeadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(selectedBeadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := legacyUpgradeTreeDigest(t, ancestorBeadsDir)
	cmd := exec.Command(bd, "bootstrap", "--dry-run", "--yes")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"BD_DISABLE_METRICS=1",
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DIR="+selectedBeadsDir,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd bootstrap unexpectedly accepted historical ancestor workspace:\n%s", output)
	}
	if !strings.Contains(string(output), "legacy Dolt server workspace") ||
		!strings.Contains(string(output), "explicit migration is required") {
		t.Fatalf("bd bootstrap did not report the expected ancestor migration refusal:\n%s", output)
	}
	if after := legacyUpgradeTreeDigest(t, ancestorBeadsDir); after != before {
		t.Fatalf("bd bootstrap mutated legacy ancestor source: before=%s after=%s", before, after)
	}
	after, readErr := os.ReadFile(legacyPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, legacy) {
		t.Fatalf("bd bootstrap rewrote ancestor config.json: got %q, want %q", after, legacy)
	}
	if _, statErr := os.Stat(filepath.Join(ancestorBeadsDir, "metadata.json")); !os.IsNotExist(statErr) {
		t.Fatalf("bd bootstrap created ancestor metadata.json: %v", statErr)
	}
}

func TestInitProxiedServerRefusesHistoricalExternalWorkspaceBeforeMutation(t *testing.T) {
	bd := buildBDUnderTest(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyConfig := []byte("{\n  \"backend\": \"dolt\",\n  \"dolt_mode\": \"server\",\n  \"dolt_database\": \"historical\"\n}\n")
	legacyConfigPath := filepath.Join(beadsDir, "config.json")
	writeFile(t, legacyConfigPath, legacyConfig)
	writeFile(t, filepath.Join(beadsDir, localVersionFile), []byte("0.62.0\n"))
	before := legacyUpgradeTreeDigest(t, beadsDir)
	home := t.TempDir()

	cmd := exec.Command(bd, "init", "--proxied-server", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_DOLT_AUTO_START=0",
		"HOME="+home,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd init --proxied-server unexpectedly accepted historical external workspace:\n%s", output)
	}
	if !strings.Contains(string(output), "legacy Dolt server workspace") ||
		!strings.Contains(string(output), "explicit migration is required") {
		t.Fatalf("bd init --proxied-server did not report the expected migration refusal:\n%s", output)
	}
	if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
		t.Fatalf("bd init --proxied-server mutated historical source: before=%s after=%s", before, after)
	}
	afterConfig, err := os.ReadFile(legacyConfigPath)
	if err != nil {
		t.Fatalf("bd init --proxied-server removed historical config.json: %v", err)
	}
	if !bytes.Equal(afterConfig, legacyConfig) {
		t.Fatalf("bd init --proxied-server rewrote config.json: got %q, want %q", afterConfig, legacyConfig)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(err) {
		t.Fatalf("bd init --proxied-server created metadata.json before refusing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
		t.Fatalf("bd init --proxied-server created local Dolt state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("bd init --proxied-server created client state before refusing: %v", err)
	}
}

func TestLegacyGuardUsesSelectedTargetSharedServerConfig(t *testing.T) {
	bd := buildBDUnderTest(t)
	selectors := []struct {
		name string
		args func(target, targetBeadsDir string) []string
	}{
		{
			name: "explicit db",
			args: func(_, targetBeadsDir string) []string {
				return []string{"--db", filepath.Join(targetBeadsDir, "dolt"), "list", "--json", "--limit", "0", "--all"}
			},
		},
		{
			name: "change directory",
			args: func(target, _ string) []string {
				return []string{"-C", target, "list", "--json", "--limit", "0", "--all"}
			},
		},
	}
	tests := []struct {
		name              string
		callerShared      bool
		targetShared      bool
		wantLegacyRefusal bool
	}{
		{name: "target shared caller not", targetShared: true},
		{name: "caller shared target not", callerShared: true, wantLegacyRefusal: true},
	}

	for _, selector := range selectors {
		for _, tt := range tests {
			t.Run(selector.name+"/"+tt.name, func(t *testing.T) {
				caller := t.TempDir()
				callerBeadsDir := filepath.Join(caller, ".beads")
				if err := os.MkdirAll(callerBeadsDir, 0o700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(callerBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"embedded"}`))
				writeFile(t, filepath.Join(callerBeadsDir, "config.yaml"), []byte("dolt:\n  shared-server: "+strconv.FormatBool(tt.callerShared)+"\n"))

				target := t.TempDir()
				targetBeadsDir := filepath.Join(target, ".beads")
				if err := os.MkdirAll(filepath.Join(targetBeadsDir, "dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(targetBeadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`))
				writeFile(t, filepath.Join(targetBeadsDir, "config.yaml"), []byte("dolt:\n  shared-server: "+strconv.FormatBool(tt.targetShared)+"\n"))

				cmd := exec.Command(bd, selector.args(target, targetBeadsDir)...)
				cmd.Dir = caller
				cmd.Env = append(os.Environ(),
					"BD_DISABLE_METRICS=1",
					"BD_DISABLE_EVENT_FLUSH=1",
					"BEADS_DOLT_AUTO_START=0",
					"BEADS_DOLT_SERVER_PORT=59999",
					"BEADS_DOLT_SHARED_SERVER=0",
					"HOME="+t.TempDir(),
				)
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("bd %s target list unexpectedly succeeded:\n%s", selector.name, output)
				}
				hasLegacyRefusal := strings.Contains(string(output), "explicit migration is required")
				if hasLegacyRefusal != tt.wantLegacyRefusal {
					t.Fatalf("bd %s target list legacy refusal=%v, want %v:\n%s", selector.name, hasLegacyRefusal, tt.wantLegacyRefusal, output)
				}
				if !tt.wantLegacyRefusal && !strings.Contains(string(output), "Dolt server") {
					t.Fatalf("bd %s target list did not use the target shared-server config:\n%s", selector.name, output)
				}
			})
		}
	}
}

func legacyUpgradeTreeDigest(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		entries = append(entries, rel+"\x00"+info.Mode().String())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entries = append(entries, hex.EncodeToString(sum[:]))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}
