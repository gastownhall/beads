package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// TestRunDoltHealthChecks_NonDoltBackend was removed: SQLite backend no longer
// exists. GetBackend() always returns "dolt" after the dolt-native cleanup.
// (bd-yqpwy)

func TestRunDoltHealthChecks_DoltBackendNoServer(t *testing.T) {
	// GH#2722: In owned/embedded mode (non-external), when no server is
	// running, server-dependent checks should be skipped gracefully (StatusOK)
	// instead of reporting false errors. The embedded SharedStore checks
	// already cover data integrity.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Write metadata.json marking this as dolt backend (no explicit server port → owned mode)
	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// No BEADS_DOLT_SERVER_PORT set → port 0 → no server running
	// No BEADS_DOLT_SHARED_SERVER → owned mode (not external)
	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 7 {
		t.Fatalf("expected exactly 7 checks (consistent shape), got %d", len(checks))
	}

	// Verify check names are consistent
	expectedNames := []string{"Dolt Connection", "Dolt Schema", "Dolt Issue Count", "Dolt Status", "Dolt Lock Health", "Phantom Databases", "Shared Server"}
	for i, name := range expectedNames {
		if checks[i].Name != name {
			t.Errorf("checks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}

	// Server-dependent checks should be OK (gracefully skipped), not errors
	for _, idx := range []int{0, 1, 2, 3, 5} {
		if checks[idx].Status != StatusOK {
			t.Errorf("checks[%d] (%s): expected StatusOK (graceful skip), got %s: %s",
				idx, checks[idx].Name, checks[idx].Status, checks[idx].Message)
		}
		if !strings.Contains(checks[idx].Message, "no server running") {
			t.Errorf("checks[%d] (%s): expected skip message about no server, got %q",
				idx, checks[idx].Name, checks[idx].Message)
		}
	}
}

func TestRunDoltHealthChecks_ExternalModeNoServer(t *testing.T) {
	// In external/shared server mode, a server IS expected to be running,
	// so connection failure should report real errors.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Write metadata.json marking this as dolt backend
	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Point at a port nothing listens on AND set server mode to external
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59998")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 7 {
		t.Fatalf("expected exactly 7 checks (consistent shape), got %d", len(checks))
	}

	if checks[0].Name != "Dolt Connection" {
		t.Errorf("expected first check to be 'Dolt Connection', got %q", checks[0].Name)
	}
	if checks[0].Status != StatusError {
		t.Errorf("expected StatusError (external server unreachable), got %s: %s", checks[0].Status, checks[0].Message)
	}

	// Schema, Issue Count, Status, and Phantom Databases should be StatusError with skip message
	for _, idx := range []int{1, 2, 3, 5} {
		if checks[idx].Status != StatusError {
			t.Errorf("checks[%d] (%s): expected StatusError, got %s", idx, checks[idx].Name, checks[idx].Status)
		}
		if !strings.Contains(checks[idx].Message, "Skipped (no connection)") {
			t.Errorf("checks[%d] (%s): expected skip message, got %q", idx, checks[idx].Name, checks[idx].Message)
		}
	}
}

func TestRunDoltHealthChecks_CheckNameAndCategory(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) == 0 {
		t.Fatal("expected at least 1 check")
	}

	check := checks[0]
	if check.Category != CategoryCore {
		t.Errorf("expected CategoryCore, got %q", check.Category)
	}
}

// TestLockContention was removed: server-only mode does not acquire advisory
// locks — the server handles its own locking. Lock contention is no longer
// a doctor concern for connection establishment.

func TestServerMode_NoLockAcquired(t *testing.T) {
	// Server-only mode never acquires advisory locks.
	// We force a non-listening port in external mode so the connection always fails.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1") // External mode: server expected

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 7 {
		t.Fatalf("expected exactly 7 checks, got %d", len(checks))
	}

	check := checks[0]

	// Should fail with a connection error, NOT a lock error
	if check.Status != StatusError {
		t.Errorf("expected StatusError (server unreachable), got %s", check.Status)
	}

}

func TestIsIgnoredTable(t *testing.T) {
	tests := []struct {
		name     string
		table    string
		expected bool
	}{
		{"wisps table", "wisps", true},
		{"wisp_events", "wisp_events", true},
		{"wisp_labels", "wisp_labels", true},
		{"wisp_dependencies", "wisp_dependencies", true},
		{"wisp_comments", "wisp_comments", true},
		{"leases", "leases", true},
		{"local_metadata", "local_metadata", true},
		{"repo_mtimes", "repo_mtimes", true},
		{"events", "events", true},
		{"issues table", "issues", false},
		{"labels table", "labels", false},
		{"dependencies table", "dependencies", false},
		{"config table", "config", false},
		{"dolt_ignore", "dolt_ignore", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIgnoredTable(tt.table); got != tt.expected {
				t.Errorf("isIgnoredTable(%q) = %v, want %v", tt.table, got, tt.expected)
			}
		})
	}
}

// TestDescribeUncommittedTables_FiltersIgnored is the regression test for the
// skip-list drift (#5260). Both uncommitted-changes checks — "Dolt Status" and
// "Dolt Locks" — now share this one filter, so a table that is benign for one
// cannot be a permanent warning on the other.
func TestDescribeUncommittedTables_FiltersIgnored(t *testing.T) {
	rows := []doltStatusRow{
		{table: "wisps", status: "modified"},
		{table: "wisp_events", status: "modified"},
		{table: "leases", status: "modified"},
		{table: "local_metadata", status: "modified"},
		{table: "repo_mtimes", status: "modified"},
		{table: "events", status: "modified"},
		{table: "issues", status: "modified", staged: true},
		{table: "labels", status: "modified"},
	}

	got := describeUncommittedTables(rows)

	want := []string{"issues: modified (staged)", "labels: modified"}
	if len(got) != len(want) {
		t.Fatalf("describeUncommittedTables() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("describeUncommittedTables()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDescribeUncommittedTables_AllIgnoredIsClean pins the property the bug
// report turned on: a store whose only dirty tables are dolt_ignore'd must read
// as clean, not as a warning that can never be cleared.
func TestDescribeUncommittedTables_AllIgnoredIsClean(t *testing.T) {
	rows := []doltStatusRow{
		{table: "wisps", status: "modified"},
		{table: "wisp_dependencies", status: "new table"},
		{table: "leases", status: "modified"},
		{table: "events", status: "modified"},
	}

	if got := describeUncommittedTables(rows); len(got) != 0 {
		t.Errorf("describeUncommittedTables() = %v, want empty (all tables are dolt_ignore'd)", got)
	}
}

// TestResolveGlobalDoltDatabase pins each arm of the GH#6599 resolver directly.
// The end-to-end coverage in dolt_phantom_test.go is //go:build cgo and skips
// whenever the shared Dolt test container is unreachable, so these are the arms
// that run everywhere, including the CGO_ENABLED=0 lane: no server, no skip.
func TestResolveGlobalDoltDatabase(t *testing.T) {
	// Shared-server mode is ON here and the stamp is deliberately not
	// doltserver.GlobalDatabaseName, so this is the one configuration that
	// discriminates "the stamp is read first" from "the mode gate is read
	// first" — reordering the two arms returns the constant and reddens this.
	t.Run("stamp wins ahead of the mode gate", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		cfg := &configfile.Config{GlobalDoltDatabase: "beads_global_renamed"}
		if got := resolveGlobalDoltDatabase(cfg); got != "beads_global_renamed" {
			t.Errorf("resolveGlobalDoltDatabase(stamped) = %q, want the stamp %q", got, "beads_global_renamed")
		}
	})

	// BEADS_DOLT_SHARED_SERVER="1" forces IsSharedServerMode() true before it
	// consults config.yaml, so this arm cannot be quieted by the host's config.
	t.Run("unstamped config falls back to the routed constant", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		if got := resolveGlobalDoltDatabase(&configfile.Config{}); got != doltserver.GlobalDatabaseName {
			t.Errorf("resolveGlobalDoltDatabase(unstamped) = %q, want %q", got, doltserver.GlobalDatabaseName)
		}
	})

	t.Run("nil config falls back to the routed constant", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		if got := resolveGlobalDoltDatabase(nil); got != doltserver.GlobalDatabaseName {
			t.Errorf("resolveGlobalDoltDatabase(nil) = %q, want %q", got, doltserver.GlobalDatabaseName)
		}
	})

	t.Run("unstamped per-project workspace resolves to nothing", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
		if doltserver.IsSharedServerMode() {
			t.Skip("shared-server mode enabled via config.yaml; cannot exercise the per-project arm")
		}
		if got := resolveGlobalDoltDatabase(&configfile.Config{}); got != "" {
			t.Errorf("resolveGlobalDoltDatabase(unstamped, per-project) = %q, want \"\" so the skip arm never matches", got)
		}
	})
}

// TestDoltLocksAndDoltStatusShareOneFilter is the anti-drift guard. The two
// checks previously kept independent skip-lists that diverged; if a future
// change reintroduces a second private filter, the shared helper stops being
// the only path and this test is the place that should start failing.
func TestDoltLocksAndDoltStatusShareOneFilter(t *testing.T) {
	// Tables that were reported dirty by "Dolt Locks" but not by "Dolt Status"
	// before #5260, because checkDoltLocks skipped only wisp tables.
	previouslyDivergent := []string{"leases", "local_metadata", "repo_mtimes", "events"}

	for _, table := range previouslyDivergent {
		if !isIgnoredTable(table) {
			t.Errorf("isIgnoredTable(%q) = false; the shared filter must cover every table both checks ignore", table)
		}
		if got := describeUncommittedTables([]doltStatusRow{{table: table, status: "modified"}}); len(got) != 0 {
			t.Errorf("describeUncommittedTables(%q) = %v, want empty", table, got)
		}
	}
}
