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
		syncGitRemote  string
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
		"DB missing, no sync.git-remote returns Warning (FR-020)": {
			dbExists:      false,
			dbName:        "acf_beads",
			host:          "127.0.0.1",
			port:          3309,
			syncGitRemote: "",
			wantStatus:    StatusWarning,
			wantContains: []string{
				`"acf_beads"`,
				"not found on server",
				"127.0.0.1:3309",
				"sync.git-remote",
				".beads/config.yaml",
			},
			wantNotContain: []string{
				"sync.git-remote is configured",
			},
			wantFix: "bd bootstrap",
		},
		"DB missing, sync.git-remote IS configured returns Warning with remote hint": {
			dbExists:      false,
			dbName:        "beads_kc",
			host:          "192.168.1.50",
			port:          3307,
			syncGitRemote: "https://doltremoteapi.dolthub.com/myorg/beads",
			wantStatus:    StatusWarning,
			wantContains: []string{
				`"beads_kc"`,
				"not found on server",
				"sync.git-remote is configured",
				"https://doltremoteapi.dolthub.com/myorg/beads",
				"bd bootstrap",
			},
			wantNotContain: []string{
				"Set sync.git-remote in .beads/config.yaml",
			},
			wantFix: "bd bootstrap",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			check := freshCloneServerResult(tt.dbExists, tt.dbName, tt.host, tt.port, tt.syncGitRemote)

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
	result := checkFreshCloneDB("127.0.0.1", 1, "root", "", "nonexistent_db")
	if result.Reachable {
		t.Fatal("expected Reachable=false for connection refused")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil error for connection refused")
	}
}

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
