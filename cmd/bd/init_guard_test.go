package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitGuardServerMessage(t *testing.T) {
	tests := map[string]struct {
		dbName         string
		host           string
		port           int
		prefix         string
		syncGitRemote  string
		wantContains   []string
		wantNotContain []string
	}{
		"DB missing, no sync.git-remote configured (FR-010, FR-011)": {
			dbName:        "acf_beads",
			host:          "127.0.0.1",
			port:          3309,
			prefix:        "acf",
			syncGitRemote: "",
			wantContains: []string{
				`"acf_beads"`,
				"127.0.0.1:3309",
				"not found on server",
				"bd doctor",
				"bd dolt status",
				"bd bootstrap",
				"set sync.git-remote",
				".beads/config.yaml",
				"Aborting",
				"--force destroys ALL existing issues",
			},
			wantNotContain: []string{
				"sync.git-remote is configured",
				// GH#2363: must NOT suggest --force as the primary action
				"bd init --force --prefix",
			},
		},
		"DB missing, sync.git-remote IS configured (FR-010, FR-011)": {
			dbName:        "beads_kc",
			host:          "192.168.1.50",
			port:          3307,
			prefix:        "kc",
			syncGitRemote: "https://doltremoteapi.dolthub.com/myorg/beads",
			wantContains: []string{
				`"beads_kc"`,
				"192.168.1.50:3307",
				"not found on server",
				"bd doctor",
				"bd dolt status",
				"bd bootstrap",
				"sync.git-remote is configured",
				"https://doltremoteapi.dolthub.com/myorg/beads",
				"--force destroys ALL existing issues",
			},
			wantNotContain: []string{
				"set sync.git-remote",
				// GH#2363: must NOT suggest --force as the primary action
				"bd init --force --prefix",
				"bd init --force to bootstrap",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := initGuardServerMessage(tt.dbName, tt.host, tt.port, tt.prefix, tt.syncGitRemote)
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
