//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// startRepointServer starts a per-test Dolt SQL server on its own port and
// data directory, so every repoint test owns the full database list it asserts
// against. It returns the port the server listens on.
func startRepointServer(t *testing.T) int {
	t.Helper()
	testutil.RequireDoltBinary(t)

	dataDir := t.TempDir()
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("FindFreePort: %v", err)
	}
	// #nosec G204 -- fixed args, no user input
	serverCmd := exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", strconv.Itoa(port), "--data-dir", dataDir)
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
	return port
}

// seedRepointVictim builds the workspace the reinit path must reason about: a
// server-mode .beads whose metadata records database, whose local Dolt root
// and current-era version witness make it an ordinary initialized workspace,
// and whose recorded database holds exactly one live issue on the server at
// port. It returns the project directory the bd child runs in.
func seedRepointVictim(t *testing.T, port int, database, prefix string) string {
	t.Helper()

	// Pin per-project server mode and a local data root, like the guard
	// fixtures: ambient shared-server settings would redirect ResolveDoltDir
	// out of this test's tree.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_DATA_DIR", "")

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltDatabase:   database,
		ProjectID:      "repoint-0000-1111-2222-333344445555",
	}).Save(beadsDir); err != nil {
		t.Fatalf("save workspace metadata: %v", err)
	}

	// A populated local Dolt root plus a current-era version witness, the
	// shape bd itself leaves behind: the legacy-upgrade guard refuses a
	// server workspace with data but no witness.
	doltDir := doltserver.ResolveDoltDir(beadsDir)
	if err := os.MkdirAll(filepath.Join(doltDir, ".dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
		t.Fatalf("write local version: %v", err)
	}

	// One live issue in the recorded database, seeded in-process the same way
	// the backup/restore fixtures seed a server-mode store.
	ctx := context.Background()
	seedStore, err := dolt.New(ctx, &dolt.Config{
		Path:            doltDir,
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create the victim database: %v", err)
	}
	defer func() { _ = seedStore.Close() }()
	if err := seedStore.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	now := time.Now()
	if err := seedStore.CreateIssue(ctx, &types.Issue{
		ID:        prefix + "-1",
		Title:     "issue the reinit must not destroy unconfirmed",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}, "test-user"); err != nil {
		t.Fatalf("seed one issue: %v", err)
	}
	return projectDir
}

// TestInitRepoint_EnvDatabaseCannotMaskConfigured pins the identity the
// reinit confirm counts: the workspace's CONFIGURED database (metadata.json),
// never one a BEADS_DOLT_SERVER_DATABASE value names. The victim records
// database A with one live issue; the environment names a database the server
// lacks, and the flags repoint init at B. The destroy confirmation must count
// A's issue and refuse in non-interactive mode without a token, leaving the
// workspace and the server exactly as they were.
//
// Before the identity split was reconciled, the count opened the env-named
// database, read zero issues out of the read error, and the unconfirmed
// reinit ran to completion.
func TestInitRepoint_EnvDatabaseCannotMaskConfigured(t *testing.T) {
	port := startRepointServer(t)
	const victimDB = "repoint_a"
	const targetDB = "repoint_b"
	projectDir := seedRepointVictim(t, port, victimDB, "rp")
	beadsDir := filepath.Join(projectDir, ".beads")

	// The masking variable: an env-named database the server does not have.
	// The guards must not take it for the workspace's identity.
	env := append(initGuardE2EEnv(t, port), "BEADS_DOLT_SERVER_DATABASE=repoint_mask")

	out, err := runExternalServerBD(t, projectDir, env,
		"init", "--reinit-local", "--recreate-missing",
		"--database", targetDB, "--prefix", "rp",
		"--skip-hooks", "--skip-agents")

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitDestroyTokenMissing {
		t.Fatalf("bd init --reinit-local --database %s must refuse with exit %d while %s holds an issue; got err=%v\n%s",
			targetDB, ExitDestroyTokenMissing, victimDB, err, out)
	}
	if !strings.Contains(out, "Refusing to destroy 1 issues") {
		t.Fatalf("refusal must count the configured database's one issue, got:\n%s", out)
	}
	// The refusal must not have created the target database.
	if check := checkDatabaseOnServer("127.0.0.1", port, "root", "", targetDB, false); check.Exists {
		t.Fatalf("the refused reinit must not create database %s on the server: %+v\n%s", targetDB, check, out)
	}
	// ...nor rewritten the workspace it refused to touch.
	after, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		t.Fatalf("read workspace metadata after the refusal: %v", err)
	}
	if after == nil || after.DoltDatabase != victimDB {
		got := "<nil>"
		if after != nil {
			got = after.DoltDatabase
		}
		t.Fatalf("the refused reinit must leave metadata recording %s, got %q", victimDB, got)
	}
}
