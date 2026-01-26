package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestCheckDaemonStatus(t *testing.T) {
	t.Run("no beads directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		check := CheckDaemonStatus(tmpDir, "1.0.0")

		// Should return OK when no .beads directory (daemon not needed)
		if check.Status != StatusOK {
			t.Errorf("Status = %q, want %q", check.Status, StatusOK)
		}
	})

	t.Run("beads directory exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		check := CheckDaemonStatus(tmpDir, "1.0.0")

		// Should check daemon status - may be OK or warning depending on daemon state
		if check.Name != "Daemon Health" {
			t.Errorf("Name = %q, want %q", check.Name, "Daemon Health")
		}
	})
}

func TestCheckGitSyncSetup(t *testing.T) {
	t.Run("not in git repository", func(t *testing.T) {
		tmpDir := t.TempDir()
		oldDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = os.Chdir(oldDir)
			git.ResetCaches()
		}()

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}
		git.ResetCaches()

		check := CheckGitSyncSetup(tmpDir)

		if check.Status != StatusWarning {
			t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
		}
		if check.Name != "Git Sync Setup" {
			t.Errorf("Name = %q, want %q", check.Name, "Git Sync Setup")
		}
		if check.Fix == "" {
			t.Error("Expected Fix to contain instructions")
		}
	})

	t.Run("in git repository without sync-branch", func(t *testing.T) {
		tmpDir := t.TempDir()
		oldDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = os.Chdir(oldDir)
			git.ResetCaches()
		}()

		// Initialize git repo
		cmd := exec.Command("git", "init", "--initial-branch=main")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to init git repo: %v", err)
		}

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}
		git.ResetCaches()

		check := CheckGitSyncSetup(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("Status = %q, want %q", check.Status, StatusOK)
		}
		if check.Name != "Git Sync Setup" {
			t.Errorf("Name = %q, want %q", check.Name, "Git Sync Setup")
		}
		// Should mention sync-branch not configured
		if check.Detail == "" {
			t.Error("Expected Detail to contain sync-branch hint")
		}
	})
}

func TestCheckDaemonAutoSync(t *testing.T) {
	t.Run("no daemon socket", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		check := CheckDaemonAutoSync(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("Status = %q, want %q", check.Status, StatusOK)
		}
		if check.Message != "Daemon not running (will use defaults on next start)" {
			t.Errorf("Message = %q, want 'Daemon not running...'", check.Message)
		}
	})

	t.Run("no sync-branch configured", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create database without sync-branch config
		dbPath := filepath.Join(beadsDir, "beads.db")
		ctx := context.Background()
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()

		// Create a fake socket file to simulate daemon running
		socketPath := filepath.Join(beadsDir, "bd.sock")
		if err := os.WriteFile(socketPath, []byte{}, 0600); err != nil {
			t.Fatal(err)
		}

		check := CheckDaemonAutoSync(tmpDir)

		// Should return OK because no sync-branch means auto-sync not applicable
		if check.Status != StatusOK {
			t.Errorf("Status = %q, want %q", check.Status, StatusOK)
		}
		if check.Message != "No sync-branch configured (auto-sync not applicable)" {
			t.Errorf("Message = %q, want 'No sync-branch...'", check.Message)
		}
	})

	t.Run("sync-branch configured but cannot connect", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create database with sync-branch config
		dbPath := filepath.Join(beadsDir, "beads.db")
		ctx := context.Background()
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetConfig(ctx, "sync.branch", "beads-sync"); err != nil {
			t.Fatal(err)
		}
		_ = store.Close()

		// Create a fake socket file (not a real daemon)
		socketPath := filepath.Join(beadsDir, "bd.sock")
		if err := os.WriteFile(socketPath, []byte{}, 0600); err != nil {
			t.Fatal(err)
		}

		check := CheckDaemonAutoSync(tmpDir)

		// Should return warning because can't connect to fake socket
		if check.Status != StatusWarning {
			t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
		}
	})
}

func TestCheckHydratedRepoDaemons(t *testing.T) {
	t.Run("no additional repos configured", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create database without repos.additional config
		dbPath := filepath.Join(beadsDir, "beads.db")
		ctx := context.Background()
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()

		check := CheckHydratedRepoDaemons(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("Status = %q, want %q", check.Status, StatusOK)
		}
		if check.Name != "Hydrated Repo Daemons" {
			t.Errorf("Name = %q, want %q", check.Name, "Hydrated Repo Daemons")
		}
		// Should say no additional repos configured
		if check.Message != "No additional repos configured (N/A)" {
			t.Errorf("Message = %q, want 'No additional repos configured (N/A)'", check.Message)
		}
	})

	t.Run("additional repos configured but no daemons running", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a fake additional repo directory
		additionalRepo := t.TempDir()
		additionalBeadsDir := filepath.Join(additionalRepo, ".beads")
		if err := os.Mkdir(additionalBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create database with repos.additional config
		dbPath := filepath.Join(beadsDir, "beads.db")
		ctx := context.Background()
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()

		// Set repos.additional config (stored as JSON array)
		reposJSON := `["` + additionalRepo + `"]`
		if err := store.SetConfig(ctx, "repos.additional", reposJSON); err != nil {
			t.Fatalf("failed to set repos.additional: %v", err)
		}

		check := CheckHydratedRepoDaemons(tmpDir)

		// Should return warning because no daemon is running in additional repo
		if check.Status != StatusWarning {
			t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
		}
		if check.Name != "Hydrated Repo Daemons" {
			t.Errorf("Name = %q, want %q", check.Name, "Hydrated Repo Daemons")
		}
		// Should mention missing daemons
		if check.Fix == "" {
			t.Error("Expected Fix to contain remediation steps")
		}
	})

	t.Run("database unavailable", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Don't create database - should handle gracefully

		check := CheckHydratedRepoDaemons(tmpDir)

		// When database is unavailable, GetConfig returns empty string,
		// so the check reports "No additional repos configured" which is OK status
		if check.Status != StatusOK {
			t.Errorf("Status = %q, want %q", check.Status, StatusOK)
		}
		// The function returns early when no config is found, treating it as "no repos"
		if !contains(check.Message, "No additional repos configured") {
			t.Errorf("Message = %q, want message about no additional repos", check.Message)
		}
	})
}
