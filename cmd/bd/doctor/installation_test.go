package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckInstallation(t *testing.T) {
	t.Run("missing beads directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		check := CheckInstallation(tmpDir)

		if check.Status != StatusError {
			t.Errorf("expected StatusError, got %s", check.Status)
		}
		if check.Name != "Installation" {
			t.Errorf("expected name 'Installation', got %s", check.Name)
		}
	})
}

func TestCheckMultipleDatabases(t *testing.T) {
	t.Run("no beads directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		check := CheckMultipleDatabases(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("expected StatusOK for missing dir, got %s", check.Status)
		}
	})

	t.Run("single database", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create single db file
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}

		check := CheckMultipleDatabases(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("expected StatusOK for single db, got %s", check.Status)
		}
	})

	t.Run("multiple databases", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create multiple db files
		for _, name := range []string{"beads.db", "issues.db", "another.db"} {
			if err := os.WriteFile(filepath.Join(beadsDir, name), []byte{}, 0644); err != nil {
				t.Fatal(err)
			}
		}

		check := CheckMultipleDatabases(tmpDir)

		if check.Status != StatusWarning {
			t.Errorf("expected StatusWarning for multiple dbs, got %s", check.Status)
		}
	})

	t.Run("backup files ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create one real db and one backup
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.backup.db"), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}

		check := CheckMultipleDatabases(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("expected StatusOK (backup ignored), got %s", check.Status)
		}
	})
}

func TestCheckPermissions(t *testing.T) {
	t.Run("no beads directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		check := CheckPermissions(tmpDir)

		// Should return error when .beads dir doesn't exist (can't write to it)
		if check.Status != StatusError {
			t.Errorf("expected StatusError for missing dir, got %s", check.Status)
		}
	})

	t.Run("writable directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.Mkdir(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		check := CheckPermissions(tmpDir)

		if check.Status != StatusOK {
			t.Errorf("expected StatusOK for writable dir, got %s", check.Status)
		}
	})
}
