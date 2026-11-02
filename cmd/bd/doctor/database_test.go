package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestCheckDatabaseIntegrity(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database directory created
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
		{
			name: "empty beads dir",
			setup: func(t *testing.T, dir string) {
				// .beads exists but no dolt/ directory
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckDatabaseIntegrity(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q", tt.expectedStatus, check.Status)
			}
			if tt.expectMessage != "" && check.Message != tt.expectMessage {
				t.Errorf("expected message %q, got %q", tt.expectMessage, check.Message)
			}
		})
	}
}

func TestServerModeIntegrityManualRecoveryDetail(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 1,
		DoltDatabase:   "doctest_missing_server_db",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	detail := serverModeIntegrityManualRecoveryDetail(beadsDir)
	if detail == "" {
		t.Fatal("expected server-mode manual recovery detail")
	}
	if !strings.Contains(detail, "Automatic integrity recovery is disabled for server-mode repos") {
		t.Fatalf("detail missing manual recovery guidance:\n%s", detail)
	}
	if !strings.Contains(detail, "doctest_missing_server_db") {
		t.Fatalf("detail missing configured database name:\n%s", detail)
	}
	if !strings.Contains(detail, doltDir) {
		t.Fatalf("detail missing dolt root path:\n%s", detail)
	}
}

func TestCheckDatabaseVersion(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No dolt/ directory - error (need to run bd init)
			},
			expectedStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckDatabaseVersion(tmpDir, "0.1.0")

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func TestCheckDatabaseVersion_BareParentWorktreeFallback(t *testing.T) {
	bareDir, worktreeDir := setupDoctorBareParentWorktree(t)
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	bareDoltDir := filepath.Join(bareBeadsDir, "dolt")
	if err := os.MkdirAll(bareDoltDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareBeadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckDatabaseVersion(worktreeDir, "0.1.0")
	if check.Message == "No dolt database found" {
		t.Fatalf("CheckDatabaseVersion() should resolve bare-parent fallback, got message %q", check.Message)
	}
}

func TestCheckSchemaCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database created
			},
			expectedStatus: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckSchemaCompatibility(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func runDoctorGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := gitCommand(args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v\n%s", args, dir, err, output)
	}

	return strings.TrimSpace(string(output))
}

func setupDoctorBareParentWorktree(t *testing.T) (string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "repo.git")
	mainWorktreeDir := filepath.Join(tmpDir, "main")
	featureWorktreeDir := filepath.Join(tmpDir, "feature")

	runDoctorGitInDir(t, tmpDir, "init", "--bare", bareDir)
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "config", "user.email", "test@example.com")
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "config", "user.name", "Test User")
	emptyTree := runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "hash-object", "-t", "tree", "/dev/null")
	initCommit := runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "commit-tree", "-m", "Initial commit", emptyTree)
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "update-ref", "HEAD", initCommit)
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "worktree", "add", mainWorktreeDir, "main")
	runDoctorGitInDir(t, mainWorktreeDir, "branch", "feature")
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "worktree", "add", featureWorktreeDir, "feature")

	return bareDir, featureWorktreeDir
}

// TestLargeDatabaseWarningRecommendsRealCommand pins the Large Database
// remedy to a command that exists: the check used to recommend 'bd cleanup',
// which is not a root-level command in this build (cleanup lives under
// 'bd admin cleanup'); the operator-facing lifecycle command is 'bd prune'.
func TestLargeDatabaseWarningRecommendsRealCommand(t *testing.T) {
	dc := largeDatabaseWarning(6000, 5000)

	if dc.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v", dc.Status)
	}
	if strings.Contains(dc.Fix, "bd cleanup") {
		t.Fatalf("Large Database fix recommends nonexistent 'bd cleanup': %q", dc.Fix)
	}
	if !strings.Contains(dc.Fix, "bd prune") {
		t.Fatalf("Large Database fix should recommend 'bd prune': %q", dc.Fix)
	}
}

// TestDatabaseOpenFailureRecoveryGuidance pins the GH#4521 contract for the
// local-store "Unable to open database" diagnostics: keep the server
// configuration diagnosis, link the engine-wont-open recovery runbook, and do
// not present doctor repair as an unopenable-journal rebuild. The
// missing-database and remote-server branches stay distinct cases.
func TestDatabaseOpenFailureRecoveryGuidance(t *testing.T) {
	const recoveryURL = "https://github.com/gastownhall/beads/blob/main/docs/recovery/database-corruption.md#engine-wont-open"

	t.Run("local store present but unopenable", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		// No metadata.json: doctor expects a local dolt directory, whose
		// default location is .beads/dolt. It exists, so the check reaches
		// the open-failure branch without opening a real engine.
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		ss := &SharedStore{beadsDir: beadsDir} // nil store: engine never opened

		check := CheckDatabaseVersionWithStore(ss, "v0.0.0-doctest")

		if check.Message != "Unable to open database" {
			t.Fatalf("expected message %q, got %q", "Unable to open database", check.Message)
		}
		if !strings.Contains(check.Fix, "bd dolt status") {
			t.Errorf("Fix must keep the server-configuration diagnosis, got %q", check.Fix)
		}
		if !strings.Contains(check.Fix, recoveryURL) {
			t.Errorf("Fix must link the engine-wont-open recovery runbook, got %q", check.Fix)
		}
		if strings.Contains(check.Fix, "bd doctor --fix") {
			t.Errorf("Fix must not present doctor repair as an unopenable-journal rebuild, got %q", check.Fix)
		}
	})

	t.Run("missing database stays a bootstrap case", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		ss := &SharedStore{beadsDir: beadsDir}

		check := CheckDatabaseVersionWithStore(ss, "v0.0.0-doctest")

		if check.Message != "No dolt database found" {
			t.Fatalf("expected the missing-database case, got %q", check.Message)
		}
		if !strings.Contains(check.Fix, "bd bootstrap") {
			t.Errorf("missing database is a bootstrap case, got %q", check.Fix)
		}
		if strings.Contains(check.Fix, recoveryURL) {
			t.Errorf("a missing database is not corruption recovery, got %q", check.Fix)
		}
	})

	t.Run("remote server stays an availability case", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := &configfile.Config{
			Backend:  configfile.BackendDolt,
			DoltMode: configfile.DoltModeServer,
		}
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("failed to save config: %v", err)
		}
		ss := &SharedStore{beadsDir: beadsDir}

		check := CheckDatabaseVersionWithStore(ss, "v0.0.0-doctest")

		if check.Message != "Unable to open database" {
			t.Fatalf("expected message %q, got %q", "Unable to open database", check.Message)
		}
		if !strings.Contains(check.Fix, "server availability") {
			t.Errorf("Fix must stay focused on server availability, got %q", check.Fix)
		}
		if strings.Contains(check.Fix, recoveryURL) {
			t.Errorf("an unreachable remote server is not local-store corruption recovery, got %q", check.Fix)
		}
	})
}
