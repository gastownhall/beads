//go:build cgo

package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// setupStaleClosedTestDB creates a dolt store with n closed issues and returns tmpDir.
// closedAt sets the closed_at timestamp. pinnedIndices marks specific issues as pinned.
// For small counts, uses the store API. For large counts (>100), uses raw SQL bulk insert.
func setupStaleClosedTestDB(t *testing.T, numClosed int, closedAt time.Time, pinnedIndices map[int]bool, thresholdDays int) string {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping test")
	}
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Generate unique database name for test isolation
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	dbName := "doctest_" + hex.EncodeToString(h[:6])
	port := doctorTestServerPort()

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.StaleClosedIssuesDays = thresholdDays
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	cfg.DoltDatabase = dbName
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "dolt")
	ctx := context.Background()

	store, err := dolt.New(ctx, &dolt.Config{
		Path:       dbPath,
		ServerHost: "127.0.0.1",
		ServerPort: port,
		Database:   dbName,
	})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer store.Close()
	t.Cleanup(func() { dropDoctorTestDatabase(dbName, port) })

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	db := store.UnderlyingDB()
	if db == nil {
		t.Fatal("UnderlyingDB returned nil")
	}

	if numClosed <= 100 {
		// Small count: use store API for realistic data
		for i := 0; i < numClosed; i++ {
			issue := &types.Issue{
				Title:     "Closed issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("Failed to create issue %d: %v", i, err)
			}
			if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
				t.Fatalf("Failed to close issue %s: %v", issue.ID, err)
			}
		}
	} else {
		// Large count: raw SQL bulk insert for speed.
		// Uses explicit transaction so writes persist when @@autocommit is OFF.
		now := time.Now().UTC()
		tx, txErr := db.Begin()
		if txErr != nil {
			t.Fatalf("Failed to begin transaction: %v", txErr)
		}
		for i := 0; i < numClosed; i++ {
			id := fmt.Sprintf("test-%06d", i)
			_, err := tx.Exec(
				`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at, pinned)
				 VALUES (?, 'Closed issue', '', '', '', '', 'closed', 2, 'task', ?, ?, ?, 0)`,
				id, now, now, closedAt,
			)
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("Failed to insert issue %d: %v", i, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Failed to commit bulk insert: %v", err)
		}
	}

	// Set closed_at for store-API-created issues (explicit tx for autocommit-OFF safety)
	if numClosed <= 100 {
		tx, txErr := db.Begin()
		if txErr != nil {
			t.Fatalf("Failed to begin transaction: %v", txErr)
		}
		_, err = tx.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("Failed to update closed_at: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Failed to commit closed_at update: %v", err)
		}
	}

	// Set pinned flag for specified indices
	if len(pinnedIndices) > 0 {
		rows, err := db.Query("SELECT id FROM issues WHERE status = 'closed' ORDER BY id")
		if err != nil {
			t.Fatalf("Failed to query IDs: %v", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("Failed to scan ID: %v", err)
			}
			ids = append(ids, id)
		}
		rows.Close()

		tx, txErr := db.Begin()
		if txErr != nil {
			t.Fatalf("Failed to begin transaction: %v", txErr)
		}
		for idx := range pinnedIndices {
			if idx < len(ids) {
				if _, err := tx.Exec("UPDATE issues SET pinned = 1 WHERE id = ?", ids[idx]); err != nil {
					_ = tx.Rollback()
					t.Fatalf("Failed to set pinned for %s: %v", ids[idx], err)
				}
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Failed to commit pinned updates: %v", err)
		}
	}

	return tmpDir
}

// Test #2: Disabled (threshold=0), small closed count → OK
func TestCheckStaleClosedIssues_DisabledSmallCount(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, 50, time.Now().AddDate(0, 0, -60), nil, 0)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (disabled with small count should be OK)", check.Status, StatusOK)
	}
	if check.Message != "Disabled (set stale_closed_issues_days to enable)" {
		t.Errorf("Message = %q, want disabled message", check.Message)
	}
}

// Test #3: Disabled (threshold=0), large closed count (≥threshold) → warning
func TestCheckStaleClosedIssues_DisabledLargeCount(t *testing.T) {
	// Override threshold to avoid inserting 10k rows (saves ~50s).
	orig := largeClosedIssuesThreshold
	largeClosedIssuesThreshold = 100
	t.Cleanup(func() { largeClosedIssuesThreshold = orig })

	tmpDir := setupStaleClosedTestDB(t, largeClosedIssuesThreshold, time.Now().AddDate(0, 0, -60), nil, 0)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (disabled with ≥10k closed should warn)", check.Status, StatusWarning)
	}
	if check.Fix == "" {
		t.Error("Expected fix suggestion for large closed count")
	}
}

// Test #4: Enabled (threshold=30d), old closed issues → correct count
func TestCheckStaleClosedIssues_EnabledWithCleanable(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, 5, time.Now().AddDate(0, 0, -60), nil, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	expected := "5 closed issue(s) older than 30 days"
	if check.Message != expected {
		t.Errorf("Message = %q, want %q", check.Message, expected)
	}
}

// Test #5: Enabled (threshold=30d), all closed recently → OK
func TestCheckStaleClosedIssues_EnabledNoneCleanable(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, 5, time.Now().AddDate(0, 0, -10), nil, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (all within threshold)", check.Status, StatusOK)
	}
	if check.Message != "No stale closed issues" {
		t.Errorf("Message = %q, want 'No stale closed issues'", check.Message)
	}
}

// Test #6: Pinned closed issues excluded from cleanable count
func TestCheckStaleClosedIssues_PinnedExcluded(t *testing.T) {
	pinned := map[int]bool{0: true, 1: true, 2: true}
	tmpDir := setupStaleClosedTestDB(t, 3, time.Now().AddDate(0, 0, -60), pinned, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (all pinned should be excluded)", check.Status, StatusOK)
	}
}

// Test #7: Mixed pinned and unpinned → only unpinned counted
func TestCheckStaleClosedIssues_MixedPinnedAndStale(t *testing.T) {
	pinned := map[int]bool{0: true, 1: true, 2: true}
	tmpDir := setupStaleClosedTestDB(t, 8, time.Now().AddDate(0, 0, -60), pinned, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	expected := "5 closed issue(s) older than 30 days"
	if check.Message != expected {
		t.Errorf("Message = %q, want %q", check.Message, expected)
	}
}

func setupMaintenanceChecksTestDB(t *testing.T, prefix string) string {
	t.Helper()

	if testServer != nil && testServer.IsCrashed() {
		t.Skipf("Dolt test server crashed: %v", testServer.CrashError())
	}

	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	cfg.DoltDatabase = "doctest_" + hex.EncodeToString(h[:6])
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	t.Cleanup(func() { dropDoctorTestDatabase(cfg.DoltDatabase, cfg.GetDoltServerPort()) })

	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	return tmpDir
}

func withMaintenanceStore(t *testing.T, tmpDir string, fn func(context.Context, *dolt.DoltStore)) {
	t.Helper()

	ctx := context.Background()
	beadsDir := filepath.Join(tmpDir, ".beads")
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer func() { _ = store.Close() }()

	fn(ctx, store)
}

func TestCheckPersistentMolIssues_UsesDoltWithoutJSONL(t *testing.T) {
	tmpDir := setupMaintenanceChecksTestDB(t, "mol")
	withMaintenanceStore(t, tmpDir, func(ctx context.Context, store *dolt.DoltStore) {
		issue := &types.Issue{
			Title:     "mol issue should have been ephemeral",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	})

	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")
	if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
		t.Fatalf("expected no JSONL file, stat err=%v", err)
	}

	check := CheckPersistentMolIssues(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "1 mol- issue(s) should be ephemeral" {
		t.Fatalf("message = %q, want exact count warning", check.Message)
	}
}

func TestCheckMisclassifiedWisps_UsesDoltWithoutJSONL(t *testing.T) {
	tmpDir := setupMaintenanceChecksTestDB(t, "bd")
	withMaintenanceStore(t, tmpDir, func(ctx context.Context, store *dolt.DoltStore) {
		db := store.UnderlyingDB()
		if db == nil {
			t.Fatal("UnderlyingDB returned nil")
		}

		now := time.Now().UTC()
		_, err := db.Exec(
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, pinned, ephemeral)
			 VALUES (?, 'misclassified wisp', '', '', '', '', 'open', 2, 'task', ?, ?, 0, 0)`,
			"bd-wisp-misclassified", now, now,
		)
		if err != nil {
			t.Fatalf("failed to insert misclassified wisp row: %v", err)
		}
		var issuesCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM issues WHERE id = 'bd-wisp-misclassified'").Scan(&issuesCount); err != nil {
			t.Fatalf("failed to verify misclassified insert in issues table: %v", err)
		}
		if issuesCount != 1 {
			t.Fatalf("expected misclassified row in issues table, got count=%d", issuesCount)
		}

		if _, err := db.Exec(
			"CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
			"test: insert misclassified wisp row",
			"beads-test <test@beads.local>",
		); err != nil {
			t.Fatalf("failed to dolt commit misclassified row: %v", err)
		}
	})

	check := checkMisclassifiedWisps(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "1 wisp issue(s) missing ephemeral flag" {
		t.Fatalf("message = %q, want exact count warning", check.Message)
	}
}

func TestCheckPatrolPollution_UsesDoltWithoutJSONL(t *testing.T) {
	tmpDir := setupMaintenanceChecksTestDB(t, "bd")
	withMaintenanceStore(t, tmpDir, func(ctx context.Context, store *dolt.DoltStore) {
		for i := 0; i < PatrolDigestThreshold+1; i++ {
			issue := &types.Issue{
				Title:     fmt.Sprintf("Digest: mol-%02d-patrol", i),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create patrol digest issue %d: %v", i, err)
			}
		}
	})

	check := CheckPatrolPollution(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Message, "11 patrol digest beads (should be 0)") {
		t.Fatalf("message = %q, want patrol digest warning", check.Message)
	}

	ids, err := getPatrolPollutionIDs(tmpDir)
	if err != nil {
		t.Fatalf("getPatrolPollutionIDs returned error: %v", err)
	}
	if len(ids) != PatrolDigestThreshold+1 {
		t.Fatalf("len(ids) = %d, want %d", len(ids), PatrolDigestThreshold+1)
	}
}

func TestCheckPatrolPollution_IgnoresEphemeralWisps(t *testing.T) {
	tmpDir := setupMaintenanceChecksTestDB(t, "bd")
	withMaintenanceStore(t, tmpDir, func(ctx context.Context, store *dolt.DoltStore) {
		for i := 0; i < PatrolDigestThreshold+5; i++ {
			issue := &types.Issue{
				Title:     fmt.Sprintf("Digest: mol-ephemeral-%02d-patrol", i),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Ephemeral: true,
			}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create ephemeral patrol digest issue %d: %v", i, err)
			}
		}
	})

	check := CheckPatrolPollution(tmpDir)
	if check.Status != StatusOK {
		t.Fatalf("status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No patrol pollution detected" {
		t.Fatalf("message = %q, want no-pollution message", check.Message)
	}

	ids, err := getPatrolPollutionIDs(tmpDir)
	if err != nil {
		t.Fatalf("getPatrolPollutionIDs returned error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("len(ids) = %d, want 0", len(ids))
	}
}

func TestClassifyPatrolIssue(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  patrolIssueKind
	}{
		{
			name:  "digest patrol",
			title: "Digest: mol-abc-patrol",
			want:  patrolIssueDigest,
		},
		{
			name:  "session ended",
			title: "Session ended: patrol complete",
			want:  patrolIssueSessionEnded,
		},
		{
			name:  "normal issue",
			title: "Regular task title",
			want:  patrolIssueNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPatrolIssue(tc.title); got != tc.want {
				t.Fatalf("classifyPatrolIssue(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}
