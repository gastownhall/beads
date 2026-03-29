package beads_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/testutil"
)

// testServerPort is the port of the shared test Dolt server (0 = not running).
var testServerPort int

func TestMain(m *testing.M) {
	os.Setenv("BEADS_TEST_MODE", "1")
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
		testServerPort = testutil.DoltContainerPortInt()
	}

	code := m.Run()

	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	os.Exit(code)
}

func skipIfNoDolt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping test")
	}
}

func skipIfNoDoltServer(t *testing.T) {
	t.Helper()
	if testServerPort == 0 {
		t.Skip("Test Dolt server not available, skipping test")
	}
	addr := fmt.Sprintf("127.0.0.1:%d", testServerPort)
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		t.Skipf("Dolt server not running on %s, skipping test", addr)
	}
	_ = conn.Close()
}

func TestOpen(t *testing.T) {
	skipIfNoDoltServer(t)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-dolt")

	ctx := context.Background()
	store, err := beads.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestFindDatabasePath(t *testing.T) {
	// This will return empty string in test environment without a database
	path := beads.FindDatabasePath()
	// Just verify it doesn't panic
	_ = path
}

func TestFindBeadsDir(t *testing.T) {
	// This will return empty string or a valid path
	dir := beads.FindBeadsDir()
	// Just verify it doesn't panic
	_ = dir
}

func TestOpenFromConfig_Embedded(t *testing.T) {
	// This test requires a running Dolt server (embedded mode is not yet implemented;
	// New() always connects via MySQL protocol to dolt sql-server).
	skipIfNoDoltServer(t)

	// Create a .beads dir with metadata.json configured for embedded mode
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	metadata := `{"backend":"dolt","database":"dolt","dolt_database":"testdb","dolt_mode":"embedded"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	ctx := context.Background()
	store, err := beads.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig (embedded) failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestOpenFromConfig_DefaultsToEmbedded(t *testing.T) {
	// This test requires a running Dolt server (embedded mode is not yet implemented;
	// New() always connects via MySQL protocol to dolt sql-server).
	skipIfNoDoltServer(t)

	// metadata.json without dolt_mode should default to embedded
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	metadata := `{"backend":"dolt","database":"dolt"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	ctx := context.Background()
	store, err := beads.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig (default) failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestOpenFromConfig_ServerModeFailsWithoutServer(t *testing.T) {
	// Server mode should fail-fast when no server is listening.
	// Temporarily unset BEADS_DOLT_PORT/BEADS_TEST_MODE so the config port
	// isn't overridden by applyConfigDefaults to the test server.
	if prev := os.Getenv("BEADS_DOLT_PORT"); prev != "" {
		os.Unsetenv("BEADS_DOLT_PORT")
		t.Cleanup(func() { os.Setenv("BEADS_DOLT_PORT", prev) })
	}
	if prev := os.Getenv("BEADS_TEST_MODE"); prev != "" {
		os.Unsetenv("BEADS_TEST_MODE")
		t.Cleanup(func() { os.Setenv("BEADS_TEST_MODE", prev) })
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Dynamically find an unused port by binding to :0 then closing
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	metadata := fmt.Sprintf(`{"backend":"dolt","database":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":%d}`, freePort)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	ctx := context.Background()
	_, openErr := beads.OpenFromConfig(ctx, beadsDir)
	if openErr == nil {
		t.Fatal("OpenFromConfig (server mode) should fail when no server is running")
	}
	// Should contain "unreachable" from the fail-fast TCP check
	if !strings.Contains(openErr.Error(), "unreachable") {
		t.Errorf("expected 'unreachable' in error, got: %v", openErr)
	}
}

func TestOpenFromConfig_NoMetadata(t *testing.T) {
	skipIfNoDoltServer(t)
	// Missing metadata.json should use defaults (server mode)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	ctx := context.Background()
	store, err := beads.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig (no metadata) failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestFindAllDatabases(t *testing.T) {
	// This scans the file system, just verify it doesn't panic
	dbs := beads.FindAllDatabases()
	// Should return a slice (possibly empty)
	if dbs == nil {
		t.Error("expected non-nil slice")
	}
}

// Test that exported constants have correct values
func TestConstants(t *testing.T) {
	// Status constants
	if beads.StatusOpen != "open" {
		t.Errorf("StatusOpen = %q, want %q", beads.StatusOpen, "open")
	}
	if beads.StatusInProgress != "in_progress" {
		t.Errorf("StatusInProgress = %q, want %q", beads.StatusInProgress, "in_progress")
	}
	if beads.StatusBlocked != "blocked" {
		t.Errorf("StatusBlocked = %q, want %q", beads.StatusBlocked, "blocked")
	}
	if beads.StatusClosed != "closed" {
		t.Errorf("StatusClosed = %q, want %q", beads.StatusClosed, "closed")
	}

	// IssueType constants
	if beads.TypeBug != "bug" {
		t.Errorf("TypeBug = %q, want %q", beads.TypeBug, "bug")
	}
	if beads.TypeFeature != "feature" {
		t.Errorf("TypeFeature = %q, want %q", beads.TypeFeature, "feature")
	}
	if beads.TypeTask != "task" {
		t.Errorf("TypeTask = %q, want %q", beads.TypeTask, "task")
	}
	if beads.TypeEpic != "epic" {
		t.Errorf("TypeEpic = %q, want %q", beads.TypeEpic, "epic")
	}

	// DependencyType constants
	if beads.DepBlocks != "blocks" {
		t.Errorf("DepBlocks = %q, want %q", beads.DepBlocks, "blocks")
	}
	if beads.DepRelated != "related" {
		t.Errorf("DepRelated = %q, want %q", beads.DepRelated, "related")
	}
}

func TestPublicAPITypeAssertions(t *testing.T) {
	skipIfNoDoltServer(t)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-dolt")

	ctx := context.Background()
	store, err := beads.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	t.Run("RemoteStore", func(t *testing.T) {
		rs, ok := store.(beads.RemoteStore)
		if !ok {
			t.Fatal("store does not satisfy beads.RemoteStore")
		}
		// Verify a method is callable (no remotes configured, so empty list)
		remotes, err := rs.ListRemotes(ctx)
		if err != nil {
			t.Fatalf("ListRemotes failed: %v", err)
		}
		_ = remotes
	})

	t.Run("SyncStore", func(t *testing.T) {
		_, ok := store.(beads.SyncStore)
		if !ok {
			t.Fatal("store does not satisfy beads.SyncStore")
		}
	})

	t.Run("VersionControlReader", func(t *testing.T) {
		vcr, ok := store.(beads.VersionControlReader)
		if !ok {
			t.Fatal("store does not satisfy beads.VersionControlReader")
		}

		branch, err := vcr.CurrentBranch(ctx)
		if err != nil {
			t.Fatalf("CurrentBranch failed: %v", err)
		}
		if branch == "" {
			t.Error("expected non-empty branch name")
		}

		commit, err := vcr.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("GetCurrentCommit failed: %v", err)
		}
		if commit == "" {
			t.Error("expected non-empty commit hash")
		}

		exists, err := vcr.CommitExists(ctx, commit)
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if !exists {
			t.Errorf("CommitExists(%s) = false, want true", commit)
		}

		status, err := vcr.Status(ctx)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}
		_ = status

		logs, err := vcr.Log(ctx, 5)
		if err != nil {
			t.Fatalf("Log failed: %v", err)
		}
		_ = logs
	})
}
