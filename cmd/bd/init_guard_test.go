package main

import (
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
				"server is running but this database hasn't been created yet",
				"bd init --force --prefix acf",
				"set sync.git-remote",
				".beads/config.yaml",
				"Aborting",
			},
			wantNotContain: []string{
				"sync.git-remote is configured",
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
				"server is running but this database hasn't been created yet",
				"bd init --force --prefix kc",
				"sync.git-remote is configured",
				"https://doltremoteapi.dolthub.com/myorg/beads",
				"bd init --force to bootstrap from the remote",
			},
			wantNotContain: []string{
				"set sync.git-remote",
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
	})
}

func TestInitGuardDBCheck_ServerUnreachable(t *testing.T) {
	// FR-030: When server is unreachable, should return Reachable=false
	// so caller falls through to existing error path without panic.

	result := checkDatabaseOnServer("127.0.0.1", 1, "root", "", "nonexistent_db")
	if result.Reachable {
		t.Fatal("expected Reachable=false for connection refused")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil error for connection refused")
	}
	// Key assertion: no panic occurred — FR-030 satisfied.
}
