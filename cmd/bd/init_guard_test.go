package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestInitGuardServerMessage(t *testing.T) {
	tests := map[string]struct {
		dbName         string
		host           string
		port           int
		prefix         string
		syncRemote     string
		wantContains   []string
		wantNotContain []string
	}{
		"DB missing, no sync.remote configured (FR-010, FR-011)": {
			dbName:     "acf_beads",
			host:       "127.0.0.1",
			port:       3309,
			prefix:     "acf",
			syncRemote: "",
			wantContains: []string{
				`"acf_beads"`,
				"127.0.0.1:3309",
				"not found on server",
				"bd doctor",
				"bd dolt status",
				"bd bootstrap",
				"set sync.remote",
				".beads/config.yaml",
				"Aborting",
				"--force destroys ALL existing issues",
			},
			wantNotContain: []string{
				"sync.remote is configured",
				// GH#2363: must NOT suggest --force as the primary action
				"bd init --force --prefix",
			},
		},
		"DB missing, sync.remote IS configured (FR-010, FR-011)": {
			dbName:     "beads_kc",
			host:       "192.168.1.50",
			port:       3307,
			prefix:     "kc",
			syncRemote: "https://doltremoteapi.dolthub.com/myorg/beads",
			wantContains: []string{
				`"beads_kc"`,
				"192.168.1.50:3307",
				"not found on server",
				"bd doctor",
				"bd dolt status",
				"bd bootstrap",
				"sync.remote is configured",
				"https://doltremoteapi.dolthub.com/myorg/beads",
				"--force destroys ALL existing issues",
			},
			wantNotContain: []string{
				"set sync.remote",
				// GH#2363: must NOT suggest --force as the primary action
				"bd init --force --prefix",
				"bd init --force to bootstrap",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := initGuardServerMessage(tt.dbName, tt.host, tt.port, tt.prefix, tt.syncRemote)
			if err == nil {
				t.Fatal("expected non-nil error")
			}

			msg := err.Error()

			for _, want := range tt.wantContains {
				if !strings.Contains(msg, want) {
					t.Errorf("expected message to contain %q, got:\n%s", want, msg)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(msg, notWant) {
					t.Errorf("expected message NOT to contain %q, got:\n%s", notWant, msg)
				}
			}
		})
	}
}

func TestInitGuardDBCheck_ExistsPath(t *testing.T) {
	// FR-012: When checkDatabaseOnServer returns Exists=true, the init guard
	// should fall through to existing "already initialized" message.
	// We verify the guard's branching logic: only Reachable=true AND Exists=false
	// triggers the new message; Exists=true must NOT trigger it.

	t.Run("exists=true skips refined message", func(t *testing.T) {
		// Simulate the guard's decision logic directly.
		// When DB exists, the guard should NOT call initGuardServerMessage.
		result := initGuardDBCheck{Exists: true, Reachable: true}
		if result.Reachable && !result.Exists && result.Err == nil {
			t.Fatal("guard would incorrectly show refined message for existing DB")
		}
		// Pass: the condition is false, so the guard falls through to "already initialized".
	})

	t.Run("exists=false triggers refined message", func(t *testing.T) {
		result := initGuardDBCheck{Exists: false, Reachable: true}
		if !(result.Reachable && !result.Exists && result.Err == nil) {
			t.Fatal("guard would NOT show refined message for missing DB")
		}
		// Verify the message content matches FR-010.
		err := initGuardServerMessage("test_db", "127.0.0.1", 3309, "test", "")
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !strings.Contains(err.Error(), "not found on server") {
			t.Errorf("expected 'not found on server' in message, got:\n%s", err.Error())
		}
		if !strings.Contains(err.Error(), "bd bootstrap") {
			t.Errorf("expected bootstrap-first recovery guidance, got:\n%s", err.Error())
		}
	})
}

func TestInitGuardDBCheck_ServerUnreachable(t *testing.T) {
	// FR-030: When server is unreachable, should return Reachable=false
	// so caller falls through to existing error path without panic.

	result := checkDatabaseOnServer("127.0.0.1", 1, "root", "", "nonexistent_db", false)
	if result.Reachable {
		t.Fatal("expected Reachable=false for connection refused")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil error for connection refused")
	}
	// Key assertion: no panic occurred — FR-030 satisfied.
}

func TestInitGuard_FreshCloneWithMetadataJSON(t *testing.T) {
	// GH#2433: On a fresh clone, metadata.json is committed (tracked by git)
	// but dolt/ directory is gitignored. The init guard should recognize this
	// as a fresh clone and allow init to proceed.

	t.Run("server_mode_metadata_no_dolt_dir_allows_init", func(t *testing.T) {
		// Switch to server mode for this subtest
		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write metadata.json as it would be on a fresh clone:
		// DoltMode=server, DoltDatabase set, but no dolt/ directory.
		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
		}
		data, _ := json.Marshal(metadata)
		metadataPath := filepath.Join(beadsDir, "metadata.json")
		if err := os.WriteFile(metadataPath, data, 0644); err != nil {
			t.Fatal(err)
		}

		// No dolt/ directory — simulates fresh clone with gitignored dolt/.
		// No server running — simulates machine B with no local server.
		err := checkExistingBeadsDataAt(beadsDir, "myproject")
		if err != nil {
			t.Errorf("fresh clone with metadata.json should allow init, got: %v", err)
		}
	})

	t.Run("server_mode_with_dolt_dir_blocks_init", func(t *testing.T) {
		// Switch to server mode for this subtest
		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write metadata.json with server mode
		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		// Create dolt/ directory — this is NOT a fresh clone
		doltDir := filepath.Join(beadsDir, "dolt")
		if err := os.MkdirAll(doltDir, 0755); err != nil {
			t.Fatal(err)
		}

		err := checkExistingBeadsDataAt(beadsDir, "myproject")
		if err == nil {
			t.Error("existing dolt directory should block init")
		}
		if err != nil && !strings.Contains(err.Error(), "already initialized") {
			t.Errorf("expected 'already initialized' message, got: %v", err)
		}
		// GH#3684: must suggest --reinit-local, not deprecated --force
		if err != nil && strings.Contains(err.Error(), "init --force") {
			t.Errorf("message must NOT suggest deprecated --force, got:\n%s", err)
		}
	})

	t.Run("embedded_mode_no_embeddeddolt_dir_allows_init", func(t *testing.T) {
		// Embedded mode is the default — no need to set serverMode
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write metadata.json with embedded mode
		metadata := map[string]interface{}{
			"database":  "dolt",
			"backend":   "dolt",
			"dolt_mode": "embedded",
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		// No embeddeddolt/ directory — simulates fresh clone
		err := checkExistingBeadsDataAt(beadsDir, "test")
		if err != nil {
			t.Errorf("fresh clone with embedded metadata should allow init, got: %v", err)
		}
	})

	t.Run("embedded_mode_with_existing_db_blocks_init", func(t *testing.T) {
		// Embedded mode is the default — no need to set serverMode
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write metadata.json with embedded mode
		metadata := map[string]interface{}{
			"database":  "dolt",
			"backend":   "dolt",
			"dolt_mode": "embedded",
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		// Create embeddeddolt/<db>/.dolt/ to simulate an existing embedded database
		dbDir := filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt")
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			t.Fatal(err)
		}

		err := checkExistingBeadsDataAt(beadsDir, "test")
		if err == nil {
			t.Error("existing embedded database should block init")
		}
		if err != nil && !strings.Contains(err.Error(), "already initialized") {
			t.Errorf("expected 'already initialized' message, got: %v", err)
		}
		// GH#3684: must suggest --reinit-local, not deprecated --force
		if err != nil && strings.Contains(err.Error(), "init --force") {
			t.Errorf("message must NOT suggest deprecated --force, got:\n%s", err)
		}
	})

	t.Run("embedded_metadata_ignores_ambient_shared_server_mode", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		metadata := map[string]interface{}{
			"database":  "dolt",
			"backend":   "dolt",
			"dolt_mode": "embedded",
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		dbDir := filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt")
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			t.Fatal(err)
		}

		err := checkExistingBeadsDataAt(beadsDir, "test")
		if err == nil {
			t.Error("existing embedded database should still block init when shared server mode is enabled elsewhere")
		}
		if err != nil && !strings.Contains(err.Error(), "already initialized") {
			t.Errorf("expected 'already initialized' message, got: %v", err)
		}
		// GH#3684: must suggest --reinit-local, not deprecated --force
		if err != nil && strings.Contains(err.Error(), "init --force") {
			t.Errorf("message must NOT suggest deprecated --force, got:\n%s", err)
		}
	})

	t.Run("no_metadata_json_allows_init", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// No metadata.json, no dolt/ — fresh project, never initialized
		err := checkExistingBeadsDataAt(beadsDir, "test")
		if err != nil {
			t.Errorf("empty beads dir should allow init, got: %v", err)
		}
	})
}

// GH#2363: Regression — AI agent followed "bd init --force" suggestion and wiped DB.
// Ensure the message never suggests --force as an actionable command.
func TestInitGuardServerMessage_NoForceAsAction(t *testing.T) {
	err := initGuardServerMessage("test_beads", "127.0.0.1", 3307, "test", "")
	msg := err.Error()

	// The message should mention --force only in the caution/warning section,
	// never as a suggested command to run.
	if strings.Contains(msg, "bd init --force --prefix") {
		t.Errorf("message must NOT suggest 'bd init --force --prefix' as an action:\n%s", msg)
	}
	if strings.Contains(msg, "bd init --force to") {
		t.Errorf("message must NOT suggest 'bd init --force to ...' as an action:\n%s", msg)
	}
}

// GH#3684: Regression — init "already initialized" messages must suggest
// --reinit-local (not the deprecated --force) for the reinit path.
func TestCheckExistingBeadsData_SuggestsReinitLocal(t *testing.T) {
	t.Run("embedded_dolt", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")

		// Write metadata.json with embedded dolt mode
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		metadata := map[string]interface{}{
			"database": "dolt", "backend": "dolt", "dolt_mode": "embedded",
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		// Create embeddeddolt/<db>/.dolt/ to simulate existing DB
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt"), 0755); err != nil {
			t.Fatal(err)
		}

		err := checkExistingBeadsDataAt(beadsDir, "test")
		if err == nil {
			t.Fatal("expected error for existing database")
		}
		msg := err.Error()
		if !strings.Contains(msg, "--reinit-local") {
			t.Errorf("message must suggest --reinit-local, got:\n%s", msg)
		}
		if strings.Contains(msg, "init --force") {
			t.Errorf("message must NOT suggest deprecated --force, got:\n%s", msg)
		}
	})

	t.Run("sqlite_db_file", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a fake beads.db file (no metadata.json → falls through to SQLite check)
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}

		err := checkExistingBeadsDataAt(beadsDir, "test")
		if err == nil {
			t.Fatal("expected error for existing database file")
		}
		msg := err.Error()
		if !strings.Contains(msg, "--reinit-local") {
			t.Errorf("message must suggest --reinit-local, got:\n%s", msg)
		}
		if strings.Contains(msg, "init --force") {
			t.Errorf("message must NOT suggest deprecated --force, got:\n%s", msg)
		}
	})
}

// GH#2338, GH#2327: Regression — error messages must always include enough
// context to identify the active target (host, port, DB name).
func TestInitGuardServerMessage_IncludesTargetIdentity(t *testing.T) {
	err := initGuardServerMessage("custom_db", "10.0.0.5", 3309, "custom", "")
	msg := err.Error()

	for _, want := range []string{"custom_db", "10.0.0.5", "3309"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must include target identity %q, got:\n%s", want, msg)
		}
	}
}

// GH#1111: Regression — safe recovery paths must be suggested before destructive ones.
// Verify that diagnostic commands appear before any mention of --force.
func TestInitGuardServerMessage_DiagnosticsBeforeForce(t *testing.T) {
	err := initGuardServerMessage("test_beads", "127.0.0.1", 3307, "test", "")
	msg := err.Error()

	doctorIdx := strings.Index(msg, "bd doctor")
	forceIdx := strings.Index(msg, "--force")

	if doctorIdx == -1 {
		t.Fatal("message must contain 'bd doctor'")
	}
	if forceIdx == -1 {
		t.Fatal("message must contain '--force' (in caution section)")
	}
	if doctorIdx > forceIdx {
		t.Errorf("'bd doctor' (at %d) must appear before '--force' (at %d) in message:\n%s",
			doctorIdx, forceIdx, msg)
	}
}

// be-5up5: 2026-08-11 fleet-wide data loss. In server mode there is NEVER a
// local dolt/ directory, whether this is a genuine fresh clone or an existing
// project whose server-side database was lost — doltDirExists==false alone
// (GH#2433's signal) cannot tell them apart. metadata.json's project_id is
// only written by a real prior init, so a non-empty project_id here is proof
// this is an existing project recovering from a missing database, not
// GH#2433's fresh-clone case (whose fixture carries no project_id — see
// TestInitGuard_FreshCloneWithMetadataJSON above). init must refuse and
// point at bd bootstrap, not silently recreate an empty database on the
// server (the missing-database state that reproduced the 2026-08-11 loss).
func TestInitGuard_ExistingProjectMissingServerDB_Refuses(t *testing.T) {
	testutil.RequireDoltBinary(t)

	oldServerMode := serverMode
	serverMode = true
	defer func() { serverMode = oldServerMode }()

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
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// The server above has no "myproject" database — simulating server-side
	// data loss on an existing project, not a never-initialized clone.
	metadata := map[string]interface{}{
		"database":      "dolt",
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": "myproject",
		"project_id":    "existing-0000-1111-2222-333344445555",
	}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	err = checkExistingBeadsDataAt(beadsDir, "myproject")
	if err == nil {
		t.Fatal("existing project (non-empty project_id) with missing server-side database must refuse init, got nil error")
	}
	if !strings.Contains(err.Error(), "not found on server") {
		t.Errorf("expected refusal message about missing database, got: %v", err)
	}
	if strings.Contains(err.Error(), "init --force") {
		t.Errorf("message must NOT suggest deprecated --force, got:\n%s", err)
	}
	if strings.Contains(err.Error(), "bd bootstrap") {
		t.Errorf("message must NOT recommend bd bootstrap for a server-mode workspace (bootstrap has its own related mode-blind create bug — be-cy41), got:\n%s", err)
	}
	if !strings.Contains(err.Error(), "bd backup restore") {
		t.Errorf("message must name a recovery path, got:\n%s", err)
	}
}

// be-ab3b: TestInitGuard_ExistingProjectMissingServerDB_Refuses above proves
// initAllowRecreateMissing is REQUIRED (refuses without it); this proves it is
// SUFFICIENT (permits init to proceed with it), covering both guard sites the
// flag gates — init.go:2566 (server reachable, database confirmed missing) and
// init.go:2578 (server unreachable, or errored, while checking). Without this,
// a wiring bug could silently strand the documented --recreate-missing
// recovery path (asymmetric risk: fails safe by blocking a legitimate
// recreate, but untested is untested).
func TestInitGuard_ExistingProjectMissingServerDB_RecreateMissingAllows(t *testing.T) {
	oldAllow := initAllowRecreateMissing
	initAllowRecreateMissing = true
	defer func() { initAllowRecreateMissing = oldAllow }()

	newExistingProjectMetadata := func() []byte {
		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			"project_id":    "existing-0000-1111-2222-333344445555",
		}
		data, _ := json.Marshal(metadata)
		return data
	}

	t.Run("server_reachable_db_missing (init.go:2566)", func(t *testing.T) {
		testutil.RequireDoltBinary(t)

		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()

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
		t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Same fixture as TestInitGuard_ExistingProjectMissingServerDB_Refuses
		// (existing project, server up, "myproject" DB absent) — only
		// initAllowRecreateMissing differs.
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), newExistingProjectMetadata(), 0644); err != nil {
			t.Fatal(err)
		}

		if err := checkExistingBeadsDataAt(beadsDir, "myproject"); err != nil {
			t.Fatalf("existing project with --recreate-missing must permit init to proceed when the server-side database is missing, got refusal: %v", err)
		}
	})

	t.Run("server_unreachable (init.go:2578)", func(t *testing.T) {
		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()

		// Port 1 is a privileged port nothing listens on in test environments —
		// same deterministic-unreachable technique as
		// TestInitGuardDBCheck_ServerUnreachable. No real dolt server needed:
		// this exercises the "server unreachable or error during check" branch,
		// not the "confirmed missing" branch above.
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), newExistingProjectMetadata(), 0644); err != nil {
			t.Fatal(err)
		}

		if err := checkExistingBeadsDataAt(beadsDir, "myproject"); err != nil {
			t.Fatalf("existing project with --recreate-missing must permit init to proceed even when the server is unreachable, got refusal: %v", err)
		}
	})
}

// TestInitGuard_UnreachableServerRefuses covers the second guard site — server
// unreachable, or errored while checking — on the REFUSE side.
//
// Review of PR #5791, item 2: the PR claimed
// TestInitGuard_ExistingProjectMissingServerDB_Refuses covered both the
// reachable-but-missing and the server-unreachable cases, but that test starts
// a live server, so it only ever exercises the first. The unreachable site had
// coverage only on the permit side (the BEADS_DOLT_SERVER_PORT=1 subtest of
// _RecreateMissingAllows), which would not notice if the refusal there were
// dropped. Needs no Dolt binary: port 1 is unreachable by construction.
func TestInitGuard_UnreachableServerRefuses(t *testing.T) {
	oldAllow := initAllowRecreateMissing
	initAllowRecreateMissing = false
	defer func() { initAllowRecreateMissing = oldAllow }()

	oldServerMode := serverMode
	serverMode = true
	defer func() { serverMode = oldServerMode }()

	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	metadata := map[string]interface{}{
		"database":      "dolt",
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": "myproject",
		"project_id":    "existing-0000-1111-2222-333344445555",
	}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	err := checkExistingBeadsDataAt(beadsDir, "myproject")
	if err == nil {
		t.Fatal("existing project whose server could not be reached must refuse init, got nil error")
	}
	if !strings.Contains(err.Error(), "not found on server") {
		t.Errorf("expected the missing-database refusal, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bd backup restore") {
		t.Errorf("message must name a recovery path, got:\n%s", err)
	}
}

// TestInitGuard_ReinitLocalDoesNotBypassMissingServerDB is the review's item 1.
//
// --force is a deprecated alias for --reinit-local, and --reinit-local skips
// checkExistingBeadsData entirely. The reinit path's own typed confirmation
// keys on countExistingIssues, which returns 0 or an error in exactly the case
// where the server-side database is missing — so before this fix,
// `bd init --force` against a lost database created a fresh empty one with no
// prompt and no destroy token. That is the 2026-08-11 fleet-wide data-loss
// reflex, reached through the flag an operator in a panic is most likely to
// reach for, and it is what makes --recreate-missing's "never implied by
// --force" help text an honest guarantee rather than an aspiration.
//
// guardMissingServerDatabaseAt is the narrow guard the reinit path now runs.
// The subtests pin both halves: it must refuse without --recreate-missing, and
// must still permit with it (otherwise the documented recovery path is dead).
func TestInitGuard_ReinitLocalDoesNotBypassMissingServerDB(t *testing.T) {
	newExistingProjectWorkspace := func(t *testing.T) string {
		t.Helper()
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			"project_id":    "existing-0000-1111-2222-333344445555",
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		return beadsDir
	}

	t.Run("refuses without --recreate-missing", func(t *testing.T) {
		oldAllow := initAllowRecreateMissing
		initAllowRecreateMissing = false
		defer func() { initAllowRecreateMissing = oldAllow }()
		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

		err := guardMissingServerDatabaseAt(newExistingProjectWorkspace(t), "myproject")
		if err == nil {
			t.Fatal("bd init --force/--reinit-local against a missing server-side database must refuse, got nil error")
		}
		if !strings.Contains(err.Error(), "not found on server") {
			t.Errorf("expected the missing-database refusal, got: %v", err)
		}
	})

	t.Run("permits with --recreate-missing", func(t *testing.T) {
		oldAllow := initAllowRecreateMissing
		initAllowRecreateMissing = true
		defer func() { initAllowRecreateMissing = oldAllow }()
		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

		if err := guardMissingServerDatabaseAt(newExistingProjectWorkspace(t), "myproject"); err != nil {
			t.Fatalf("--recreate-missing must still authorize the reinit path, got: %v", err)
		}
	})

	t.Run("stays silent on a fresh clone with no project_id", func(t *testing.T) {
		oldAllow := initAllowRecreateMissing
		initAllowRecreateMissing = false
		defer func() { initAllowRecreateMissing = oldAllow }()
		oldServerMode := serverMode
		serverMode = true
		defer func() { serverMode = oldServerMode }()
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			// no project_id: a genuine fresh clone, which --reinit-local must
			// still be able to initialize.
		}
		data, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		if err := guardMissingServerDatabaseAt(beadsDir, "myproject"); err != nil {
			t.Fatalf("a fresh clone (no project_id) must not be blocked by this guard, got: %v", err)
		}
	})
}

// TestInitGuard_SharedServerMode_MissingServerDB_Refuses pins the shared-server
// case, which the per-project tests above cannot reach.
//
// guardMissingServerDatabaseAt used to stat doltserver.ResolveDoltDir(beadsDir)
// and return early when it was a directory, treating that as "there is local
// data to protect". In shared-server mode ResolveDoltDir returns the
// machine-global ~/.beads/shared-server/dolt, and SharedDoltDir() MkdirAlls it
// — so the stat always succeeded and checkDatabaseOnServer was never reached.
// The guard was a no-op in exactly the topology the 2026-08-11 loss happened
// in, while passing every per-project test.
//
// Here the server is REACHABLE and simply does not have this project's
// database — the actual loss shape, not an unreachable-host stand-in.
func TestInitGuard_SharedServerMode_MissingServerDB_Refuses(t *testing.T) {
	testutil.RequireDoltBinary(t)

	oldServerMode := serverMode
	serverMode = true
	defer func() { serverMode = oldServerMode }()

	// Shared-server mode, with HOME redirected so SharedDoltDir() resolves
	// (and MkdirAlls) inside the test's own tree rather than the developer's.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	if !doltserver.IsSharedServerMode() {
		t.Fatal("precondition: BEADS_DOLT_SHARED_SERVER=1 did not enable shared-server mode")
	}

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
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"database":      "dolt",
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": "myproject",
		"project_id":    "shared-0000-1111-2222-333344445555",
	}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// The regression this pins: the shared dolt dir exists (MkdirAll'd on
	// resolve) but holds no database for this project.
	sharedDolt, err := doltserver.SharedDoltDir()
	if err != nil {
		t.Fatalf("SharedDoltDir: %v", err)
	}
	if _, statErr := os.Stat(sharedDolt); statErr != nil {
		t.Fatalf("precondition: shared dolt dir %q should exist after resolve: %v", sharedDolt, statErr)
	}
	if _, statErr := os.Stat(filepath.Join(sharedDolt, "myproject")); statErr == nil {
		t.Fatalf("precondition: shared dolt dir must NOT hold a myproject database")
	}

	err = guardMissingServerDatabaseAt(beadsDir, "myproject")
	if err == nil {
		t.Fatal("shared-server mode with a reachable server and no myproject database must refuse init, got nil error " +
			"(the guard returned early on the machine-global shared dolt dir instead of checking the server)")
	}
	if !strings.Contains(err.Error(), "not found on server") {
		t.Errorf("expected refusal message about missing database, got: %v", err)
	}
}

// TestInitGuard_SharedServerMode_PresentServerDB_Allows is the positive control
// for the test above: with this project's database actually present on the
// shared server, the guard must stay out of the way so --reinit-local and
// ordinary reinit keep working. Without this, tightening the guard could
// silently start refusing healthy shared-server workspaces.
func TestInitGuard_SharedServerMode_PresentServerDB_Allows(t *testing.T) {
	testutil.RequireDoltBinary(t)

	oldServerMode := serverMode
	serverMode = true
	defer func() { serverMode = oldServerMode }()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

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
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))

	sharedDB, err := testutil.SetupSharedTestDB(port, "myproject")
	if err != nil {
		t.Fatalf("create myproject database on server: %v", err)
	}
	_ = sharedDB.Close()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"database":      "dolt",
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": "myproject",
		"project_id":    "shared-0000-1111-2222-333344445555",
	}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	if err := guardMissingServerDatabaseAt(beadsDir, "myproject"); err != nil {
		t.Fatalf("database present on the shared server must NOT be refused, got: %v", err)
	}
}
