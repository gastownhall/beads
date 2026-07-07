package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshCloneServerResult(t *testing.T) {
	tests := map[string]struct {
		dbExists       bool
		dbName         string
		host           string
		port           int
		syncRemote     string
		wantStatus     string
		wantContains   []string
		wantNotContain []string
		wantFix        string
	}{
		"DB exists on server returns OK (FR-021)": {
			dbExists:   true,
			dbName:     "acf_beads",
			host:       "127.0.0.1",
			port:       3309,
			wantStatus: StatusOK,
			wantContains: []string{
				"Database exists on server",
			},
		},
		"DB missing, no sync.remote returns Warning (FR-020)": {
			dbExists:   false,
			dbName:     "acf_beads",
			host:       "127.0.0.1",
			port:       3309,
			syncRemote: "",
			wantStatus: StatusWarning,
			wantContains: []string{
				`"acf_beads"`,
				"not found on server",
				"127.0.0.1:3309",
				"sync.remote",
				".beads/config.yaml",
			},
			wantNotContain: []string{
				"sync.remote is configured",
			},
			wantFix: "bd bootstrap",
		},
		"DB missing, sync.remote IS configured returns Warning with remote hint": {
			dbExists:   false,
			dbName:     "beads_kc",
			host:       "192.168.1.50",
			port:       3307,
			syncRemote: "https://doltremoteapi.dolthub.com/myorg/beads",
			wantStatus: StatusWarning,
			wantContains: []string{
				`"beads_kc"`,
				"not found on server",
				"sync.remote is configured",
				"https://doltremoteapi.dolthub.com/myorg/beads",
				"bd bootstrap",
			},
			wantNotContain: []string{
				"Set sync.remote in .beads/config.yaml",
			},
			wantFix: "bd bootstrap",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			check := freshCloneServerResult(tt.dbExists, tt.dbName, tt.host, tt.port, tt.syncRemote)

			if check.Name != "Fresh Clone" {
				t.Errorf("expected Name %q, got %q", "Fresh Clone", check.Name)
			}

			if check.Status != tt.wantStatus {
				t.Errorf("expected Status %q, got %q (message: %s)", tt.wantStatus, check.Status, check.Message)
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(check.Message, want) {
					t.Errorf("expected message to contain %q, got:\n%s", want, check.Message)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(check.Message, notWant) {
					t.Errorf("expected message NOT to contain %q, got:\n%s", notWant, check.Message)
				}
			}

			if tt.wantFix != "" && check.Fix != tt.wantFix {
				t.Errorf("expected Fix %q, got %q", tt.wantFix, check.Fix)
			}
		})
	}
}

func TestCheckFreshCloneDB_ServerUnreachable(t *testing.T) {
	// FR-030: When server is unreachable, should return Reachable=false
	// so caller skips the server-mode check without panic.
	result := checkFreshCloneDB("127.0.0.1", 1, "root", "", "nonexistent_db", false)
	if result.Reachable {
		t.Fatal("expected Reachable=false for connection refused")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil error for connection refused")
	}
}

func TestCheckFreshClone_ServerModeUnreachable(t *testing.T) {
	// GH#35 + bd-tzo9: When metadata.json declares dolt_mode=server but the
	// server is unreachable, CheckFreshClone must:
	//   1. Resolve credentials by the resolved runtime port (bd-tzo9), not
	//      the deprecated metadata port default — exercised by going through
	//      the GetDoltServerPasswordForPort(port) call before the failed ping.
	//   2. Surface a server-mode-aware warning instead of the misleading
	//      legacy "Fresh clone detected (no database)" message (GH#35). In
	//      server mode the local DB absence is expected; suggesting bd
	//      bootstrap is wrong when the actual problem is connectivity/auth.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// JSONL with issues so the check proceeds past the fresh-clone gate.
	jsonl := `{"id":"bd-abc","title":"t"}` + "\n" + `{"id":"bd-def","title":"t"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	// metadata.json declaring server mode pointed at an unreachable host:port.
	// Port 1 is guaranteed-unreachable on loopback.
	meta := `{
  "database": "beads.db",
  "dolt_mode": "server",
  "dolt_server_host": "127.0.0.1",
  "dolt_server_port": 1,
  "dolt_server_user": "root",
  "dolt_database": "beads"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckFreshClone(tmpDir)

	if check.Status != StatusWarning {
		t.Fatalf("expected %q on unreachable server, got %q (message: %s)",
			StatusWarning, check.Status, check.Message)
	}

	// GH#35: must NOT emit the misleading legacy "Fresh clone detected
	// (N issues in issues.jsonl, no database)" message in server mode.
	if strings.Contains(check.Message, "issues in issues.jsonl, no database") {
		t.Errorf("server-mode unreachable should not emit legacy fresh-clone message; got: %s", check.Message)
	}

	// Must call out server-mode connectivity, not "fresh clone".
	wantSubstrings := []string{"server", "127.0.0.1:1"}
	for _, want := range wantSubstrings {
		if !strings.Contains(strings.ToLower(check.Message), strings.ToLower(want)) {
			t.Errorf("expected message to mention %q, got: %s", want, check.Message)
		}
	}

	// Fix should suggest connectivity/credential checks, not bd bootstrap
	// (which won't help when the server itself is unreachable).
	if strings.Contains(check.Fix, "bd bootstrap") {
		t.Errorf("server-mode unreachable fix should not suggest 'bd bootstrap'; got: %s", check.Fix)
	}
	for _, want := range []string{"server", "credentials"} {
		if !strings.Contains(strings.ToLower(check.Fix), want) {
			t.Errorf("expected fix to mention %q, got: %s", want, check.Fix)
		}
	}
}

func TestFreshCloneServerUnreachableResult(t *testing.T) {
	// Pure-function coverage for the server-unreachable branch (GH#35).
	// Verifies the message identifies server mode, includes host:port and
	// the underlying error, and avoids the misleading bd bootstrap fix.
	check := freshCloneServerUnreachableResult(
		"acf_beads",
		"dolt.example.com",
		3306,
		errSentinelForTest{msg: "dial tcp: i/o timeout"},
	)

	if check.Name != "Fresh Clone" {
		t.Errorf("expected Name %q, got %q", "Fresh Clone", check.Name)
	}
	if check.Status != StatusWarning {
		t.Errorf("expected Status %q, got %q", StatusWarning, check.Status)
	}

	wantInMsg := []string{
		"unreachable",
		"dolt.example.com:3306",
		`"acf_beads"`,
		"server mode",
	}
	for _, want := range wantInMsg {
		if !strings.Contains(check.Message, want) {
			t.Errorf("expected message to contain %q, got: %s", want, check.Message)
		}
	}

	if !strings.Contains(check.Detail, "dial tcp: i/o timeout") {
		t.Errorf("expected detail to surface underlying error, got: %s", check.Detail)
	}

	// Must NOT recommend bd bootstrap — the problem is connectivity, not
	// missing local state.
	if strings.Contains(check.Fix, "bd bootstrap") {
		t.Errorf("server-unreachable fix should not suggest 'bd bootstrap'; got: %s", check.Fix)
	}

	// Nil error path should still produce a usable message and not panic.
	checkNil := freshCloneServerUnreachableResult("beads", "127.0.0.1", 3307, nil)
	if checkNil.Status != StatusWarning {
		t.Errorf("nil-err path: expected Status %q, got %q", StatusWarning, checkNil.Status)
	}
	if !strings.Contains(checkNil.Message, "unreachable") {
		t.Errorf("nil-err path: expected message to mention 'unreachable', got: %s", checkNil.Message)
	}
}

// errSentinelForTest is a minimal error type used to drive the server-unreachable
// helper without depending on the network/MySQL driver. Local to this test file.
type errSentinelForTest struct{ msg string }

func (e errSentinelForTest) Error() string { return e.msg }

func TestCheckFreshClone_EmbeddedMode(t *testing.T) {
	// AC-005: Embedded mode (not server mode) uses only filesystem checks.
	// No server connection should be attempted.

	tests := map[string]struct {
		setupFunc      func(t *testing.T, tmpDir string)
		expectedStatus string
		wantContains   string
	}{
		"no beads directory": {
			setupFunc:      func(t *testing.T, tmpDir string) {},
			expectedStatus: StatusOK,
			wantContains:   "N/A",
		},
		"beads dir with JSONL but no database (embedded Dolt)": {
			// AC-005: .beads/ exists with JSONL and dolt backend config, but NO
			// server mode — should detect fresh clone via filesystem only, no server call.
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.MkdirAll(beadsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				// Write a minimal JSONL file so the check proceeds past the JSONL gate.
				if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"test-1"}`+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				// Write a config.yaml with dolt backend but NO server mode.
				// This means embedded Dolt — filesystem check only.
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("backend: dolt\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			wantContains:   "no database",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setupFunc(t, tmpDir)

			check := CheckFreshClone(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}

			if !strings.Contains(check.Message, tt.wantContains) {
				t.Errorf("expected message to contain %q, got: %s", tt.wantContains, check.Message)
			}
		})
	}
}
