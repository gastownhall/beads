//go:build cgo

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// bd-206: Test updating open issue to closed preserves closed_at
func TestImportOpenToClosedTransition(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := context.Background()

	// Step 1: Create an open issue in the database
	openIssue := &types.Issue{
		ID:          "bd-transition-1",
		Title:       "Test transition",
		Description: "This will be closed",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		ClosedAt:    nil,
	}

	if err := testStore.CreateIssue(ctx, openIssue, "test"); err != nil {
		t.Fatalf("Failed to create open issue: %v", err)
	}

	// Step 2: Update via UpdateIssue with closed status (closed_at managed automatically)
	updates := map[string]interface{}{
		"status": types.StatusClosed,
	}

	if err := testStore.UpdateIssue(ctx, "bd-transition-1", updates, "test"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Step 3: Verify the issue is now closed with correct closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-1")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusClosed {
		t.Errorf("Expected status to be closed, got %s", updated.Status)
	}

	if updated.ClosedAt == nil {
		t.Fatal("Expected closed_at to be set after transition to closed")
	}
}

// bd-206: Test updating closed issue to open clears closed_at
func TestImportClosedToOpenTransition(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := context.Background()

	// Step 1: Create a closed issue in the database
	closedTime := time.Now()
	closedIssue := &types.Issue{
		ID:          "bd-transition-2",
		Title:       "Test reopening",
		Description: "This will be reopened",
		Status:      types.StatusClosed,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   closedTime,
		ClosedAt:    &closedTime,
	}

	if err := testStore.CreateIssue(ctx, closedIssue, "test"); err != nil {
		t.Fatalf("Failed to create closed issue: %v", err)
	}

	// Step 2: Update via UpdateIssue with open status (closed_at managed automatically)
	updates := map[string]interface{}{
		"status": types.StatusOpen,
	}

	if err := testStore.UpdateIssue(ctx, "bd-transition-2", updates, "test"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Step 3: Verify the issue is now open with null closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-2")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusOpen {
		t.Errorf("Expected status to be open, got %s", updated.Status)
	}

	if updated.ClosedAt != nil {
		t.Errorf("Expected closed_at to be nil after reopening, got %v", updated.ClosedAt)
	}
}

// TestBlockedEnvVars tests that BD_BACKEND and BD_DATABASE_BACKEND are blocked (bd-hevyw).
func TestBlockedEnvVars(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		value  string
	}{
		{"BD_BACKEND blocked", "BD_BACKEND", "sqlite"},
		{"BD_DATABASE_BACKEND blocked", "BD_DATABASE_BACKEND", "sqlite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)
			err := checkBlockedEnvVars()
			if err == nil {
				t.Errorf("expected error when %s is set, got nil", tt.envVar)
			}
			if err != nil && !strings.Contains(err.Error(), tt.envVar) {
				t.Errorf("expected error to mention %s, got: %v", tt.envVar, err)
			}
		})
	}

	// Verify no error when env vars are unset
	t.Run("no env vars set", func(t *testing.T) {
		t.Setenv("BD_BACKEND", "")
		t.Setenv("BD_DATABASE_BACKEND", "")
		// Unset them (t.Setenv("", "") sets to empty which Getenv returns as "")
		os.Unsetenv("BD_BACKEND")
		os.Unsetenv("BD_DATABASE_BACKEND")
		err := checkBlockedEnvVars()
		if err != nil {
			t.Errorf("expected no error when env vars are unset, got: %v", err)
		}
	})
}

func TestListUsesRepoBeadsDirWhenDoltDataDirEscapesDotBeads(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	initConfigForTest(t)
	ensureCleanGlobalState(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	relativeDoltDir := "../external-dolt"
	externalDoltDir := filepath.Join(beadsDir, relativeDoltDir)
	if err := os.MkdirAll(filepath.Dir(externalDoltDir), 0o755); err != nil {
		t.Fatalf("mkdir external dolt parent: %v", err)
	}

	database := uniqueTestDBName(t)
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
		DoltDataDir:    relativeDoltDir,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(testDoltServerPort)), 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	ctx := context.Background()
	testStore, err := dolt.New(ctx, &dolt.Config{
		Path:            externalDoltDir,
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	defer func() {
		_ = testStore.Close()
		dropTestDatabase(database, testDoltServerPort)
	}()

	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := testStore.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		t.Fatalf("set types.custom: %v", err)
	}

	now := time.Now()
	issue := &types.Issue{
		ID:          "test-port-proof-1",
		Title:       "Port-proof issue",
		Description: "Verifies bd list uses the repo's .beads config even with external dolt data",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	binPath := filepath.Join(t.TempDir(), "bd-under-test")
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	buildCmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", binPath, ".")
	buildCmd.Dir = packageDir
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, buildOut)
	}

	listCmd := exec.Command(binPath, "list", "--json")
	listCmd.Dir = repoDir
	listCmd.Env = append(os.Environ(),
		"BEADS_TEST_MODE=1",
		"BEADS_DIR="+beadsDir,
		"BEADS_DB=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
	)
	output, err := listCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Port-proof issue") {
		t.Fatalf("expected list output to include created issue\n%s", output)
	}
}

// bd-6dnrw.5: shared-server mode overriding a pinned dolt_mode=embedded must
// warn and win for the session, but must never rewrite the committed
// metadata.json (per-machine env must not leak into shared config).
func TestSharedServerEmbeddedMismatchDoesNotRewriteMetadata(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeEmbedded
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	before, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")

	oldServerMode, oldWarned := serverMode, sharedServerEmbeddedMismatchWarned
	defer func() {
		serverMode = oldServerMode
		sharedServerEmbeddedMismatchWarned = oldWarned
	}()
	sharedServerEmbeddedMismatchWarned = false

	captureStderr := func(fn func()) string {
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("pipe: %v", pipeErr)
		}
		oldStderr := os.Stderr
		os.Stderr = w
		defer func() { os.Stderr = oldStderr }()
		fn()
		_ = w.Close()
		out, readErr := io.ReadAll(r)
		if readErr != nil {
			t.Fatalf("read stderr: %v", readErr)
		}
		return string(out)
	}

	stderr := captureStderr(func() { loadServerModeFromBeadsDir(beadsDir) })

	if !serverMode {
		t.Error("expected shared-server env to win for the session (serverMode=true)")
	}
	if !strings.Contains(stderr, "dolt_mode=\"embedded\"") {
		t.Errorf("expected mismatch notice on stderr, got: %q", stderr)
	}
	after, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	if err != nil {
		t.Fatalf("re-read metadata.json: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("metadata.json was rewritten on disk:\nbefore: %s\nafter: %s", before, after)
	}

	// The notice is once-per-process: a second load must stay quiet.
	stderr = captureStderr(func() { loadServerModeFromBeadsDir(beadsDir) })
	if strings.Contains(stderr, "dolt_mode") {
		t.Errorf("expected no repeat notice, got: %q", stderr)
	}
}
