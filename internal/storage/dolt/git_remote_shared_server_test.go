//go:build integration

package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestCredentialCLIRoutingE2ESharedServer verifies that shared-server mode can
// route credential-bearing pushes through the shared Dolt root even when the
// per-project dbPath is stale and lacks the remote.
func TestCredentialCLIRoutingE2ESharedServer(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping test")
	}
	skipIfNoGit(t)

	baseDir, err := os.MkdirTemp("", "credential-cli-routing-shared-server-e2e-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(baseDir) })

	remoteDir := filepath.Join(baseDir, "remote.git")
	runCmd(t, baseDir, "git", "init", "--bare", "-b", "main", remoteDir)

	seedDir := filepath.Join(baseDir, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatalf("failed to create seed dir: %v", err)
	}
	runCmd(t, seedDir, "git", "init", "-b", "main")
	runCmd(t, seedDir, "git", "commit", "--allow-empty", "-m", "init")
	runCmd(t, seedDir, "git", "remote", "add", "origin", remoteDir)
	runCmd(t, seedDir, "git", "push", "-u", "origin", "main")

	remoteURL := "file://" + remoteDir

	sharedServerDir := filepath.Join(baseDir, "shared-server")
	sharedDoltDir := filepath.Join(sharedServerDir, "dolt")
	if err := os.MkdirAll(sharedDoltDir, 0o755); err != nil {
		t.Fatalf("failed to create shared dolt dir: %v", err)
	}
	runCmd(t, sharedDoltDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	sharedTestdbDir := filepath.Join(sharedDoltDir, "testdb")
	if err := os.MkdirAll(sharedTestdbDir, 0o755); err != nil {
		t.Fatalf("failed to create shared testdb dir: %v", err)
	}
	runCmd(t, sharedTestdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")
	runCmd(t, sharedTestdbDir, "dolt", "remote", "add", "origin", remoteURL)

	initSchemaSQL := schema.AllMigrationsSQL() + "\nCALL DOLT_ADD('.');\nCALL DOLT_COMMIT('-Am', 'Genesis: schema and config');"
	runDoltSQL(t, sharedTestdbDir, initSchemaSQL)

	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = sharedDoltDir
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	if !testutil.WaitForServer(port, 15*time.Second) {
		t.Fatal("dolt sql-server did not become ready within timeout")
	}

	projectBeadsDir := filepath.Join(baseDir, "project", ".beads")
	clientDataDir := filepath.Join(projectBeadsDir, "dolt")
	clientTestdbDir := filepath.Join(clientDataDir, "testdb")
	if err := os.MkdirAll(clientTestdbDir, 0o755); err != nil {
		t.Fatalf("failed to create client testdb dir: %v", err)
	}
	runCmd(t, clientTestdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	cmd := exec.Command("dolt", "remote", "-v")
	cmd.Dir = clientTestdbDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt remote -v failed in stale client dir: %v\n%s", err, output)
	}
	require.NotContains(t, string(output), "origin", "stale per-project CLI dir should not contain the shared remote")

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedServerDir)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_TEST_MODE", "")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := New(ctx, &Config{
		Path:            clientDataDir,
		BeadsDir:        projectBeadsDir,
		Database:        "testdb",
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		ServerUser:      "root",
		CommitterName:   "test",
		CommitterEmail:  "test@test.com",
		AutoStart:       false,
		CreateIfMissing: false,
		Remote:          "origin",
		RemoteUser:      "testuser",
		RemotePassword:  "testpassword",
	})
	if err != nil {
		t.Fatalf("failed to create DoltStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix) failed: %v", err)
	}

	require.Equal(t, filepath.Join(sharedDoltDir, "testdb"), store.CLIDir(), "shared-server CLIDir should resolve to shared Dolt root")
	require.True(t, store.shouldUseCLIForCredentials(ctx), "shared-server mode should route credentials via the shared CLI remote")

	issue := &types.Issue{
		ID:        "shared-route-001",
		Title:     "Shared server routed push",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.Commit(ctx, "Add shared-route-001"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	cloneDir := filepath.Join(baseDir, "clone-shared-routing")
	doltClone(t, remoteURL, cloneDir)

	rows := queryCSV(t, cloneDir, "SELECT id, title FROM issues WHERE id = 'shared-route-001'")
	if len(rows) == 0 {
		t.Fatal("clone: expected shared-route-001 to exist after shared-server push")
	}
	if rows[0]["title"] != "Shared server routed push" {
		t.Errorf("clone: title = %q, want %q", rows[0]["title"], "Shared server routed push")
	}
}
