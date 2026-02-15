package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

func TestNew_SQLiteBackendReturnsError(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	_, err := New(ctx, configfile.BackendSQLite, dbPath)
	if err == nil {
		t.Fatal("New(sqlite) should return error since SQLite backend was removed")
	}
	if !strings.Contains(err.Error(), "removed") {
		t.Errorf("error should mention removed, got: %v", err)
	}
}

func TestNew_UnknownBackend(t *testing.T) {
	ctx := context.Background()

	_, err := New(ctx, "unknown-backend", "/tmp/fake")
	if err == nil {
		t.Fatal("New(unknown) should return error")
	}
	if !strings.Contains(err.Error(), "unknown storage backend") {
		t.Errorf("error should mention unknown backend, got: %v", err)
	}
}

func TestRegisterBackend(t *testing.T) {
	called := false
	RegisterBackend("test-backend", func(ctx context.Context, path string, opts Options) (storage.Storage, error) {
		called = true
		return nil, nil
	})

	_, _ = New(context.Background(), "test-backend", "/fake")
	if !called {
		t.Error("registered backend factory was not called")
	}

	// Clean up registry
	delete(backendRegistry, "test-backend")
}

func TestGetBackendFromConfig_NoConfig(t *testing.T) {
	// Non-existent directory should default to Dolt
	backend := GetBackendFromConfig("/nonexistent/path")
	if backend != configfile.BackendDolt {
		t.Errorf("GetBackendFromConfig(missing) = %q, want %q", backend, configfile.BackendDolt)
	}
}

func TestGetBackendFromConfig_WithConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a metadata.json with dolt backend
	metadataPath := filepath.Join(tmpDir, "metadata.json")
	err := os.WriteFile(metadataPath, []byte(`{"backend": "dolt"}`), 0644)
	if err != nil {
		t.Fatalf("writing metadata.json: %v", err)
	}

	backend := GetBackendFromConfig(tmpDir)
	if backend != configfile.BackendDolt {
		t.Errorf("GetBackendFromConfig() = %q, want %q", backend, configfile.BackendDolt)
	}
}

func TestGetBackendFromConfig_LegacySQLite(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a metadata.json with legacy sqlite backend
	metadataPath := filepath.Join(tmpDir, "metadata.json")
	err := os.WriteFile(metadataPath, []byte(`{"backend": "sqlite"}`), 0644)
	if err != nil {
		t.Fatalf("writing metadata.json: %v", err)
	}

	backend := GetBackendFromConfig(tmpDir)
	if backend != configfile.BackendSQLite {
		t.Errorf("GetBackendFromConfig(sqlite) = %q, want %q", backend, configfile.BackendSQLite)
	}
}

func TestOptions_ZeroValue(t *testing.T) {
	opts := Options{}
	if opts.ReadOnly {
		t.Error("zero Options should not be ReadOnly")
	}
	if opts.LockTimeout != 0 {
		t.Error("zero Options should have zero LockTimeout")
	}
	if opts.ServerHost != "" {
		t.Error("zero Options should have empty ServerHost")
	}
	if opts.ServerPort != 0 {
		t.Error("zero Options should have zero ServerPort")
	}
}
