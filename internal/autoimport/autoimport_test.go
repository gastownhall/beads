package autoimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

// testNotifier captures notifications for assertions
type testNotifier struct {
	debugs []string
	infos  []string
	warns  []string
	errors []string
}

func (n *testNotifier) Debugf(format string, args ...interface{}) {
	n.debugs = append(n.debugs, format)
}

func (n *testNotifier) Infof(format string, args ...interface{}) {
	n.infos = append(n.infos, format)
}

func (n *testNotifier) Warnf(format string, args ...interface{}) {
	n.warns = append(n.warns, format)
}

func (n *testNotifier) Errorf(format string, args ...interface{}) {
	n.errors = append(n.errors, format)
}

func TestAutoImportIfNewer_NoJSONL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-autoimport-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")
	store := memory.New("")
	notify := &testNotifier{}

	importCalled := false
	importFunc := func(ctx context.Context, issues []*types.Issue) (int, int, map[string]string, error) {
		importCalled = true
		return 0, 0, nil, nil
	}

	err = AutoImportIfNewer(context.Background(), store, dbPath, notify, importFunc, nil)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if importCalled {
		t.Error("Import should not be called when JSONL doesn't exist")
	}
}

func TestAutoImportIfNewer_UnchangedHash(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-autoimport-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create test JSONL
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(f).Encode(issue)
	f.Close()

	// Compute hash
	data, _ := os.ReadFile(jsonlPath)
	hasher := sha256.New()
	hasher.Write(data)
	hash := hex.EncodeToString(hasher.Sum(nil))

	// Store hash in metadata
	store := memory.New("")
	ctx := context.Background()
	store.SetMetadata(ctx, "last_import_hash", hash)

	notify := &testNotifier{}
	importCalled := false
	importFunc := func(ctx context.Context, issues []*types.Issue) (int, int, map[string]string, error) {
		importCalled = true
		return 0, 0, nil, nil
	}

	err = AutoImportIfNewer(ctx, store, dbPath, notify, importFunc, nil)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if importCalled {
		t.Error("Import should not be called when hash is unchanged")
	}
}

func TestAutoImportIfNewer_ChangedHash(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-autoimport-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create test JSONL
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(f).Encode(issue)
	f.Close()

	// Store different hash in metadata
	store := memory.New("")
	ctx := context.Background()
	store.SetMetadata(ctx, "last_import_hash", "different-hash")

	notify := &testNotifier{}
	importCalled := false
	var receivedIssues []*types.Issue
	importFunc := func(ctx context.Context, issues []*types.Issue) (int, int, map[string]string, error) {
		importCalled = true
		receivedIssues = issues
		return 1, 0, nil, nil
	}

	err = AutoImportIfNewer(ctx, store, dbPath, notify, importFunc, nil)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !importCalled {
		t.Error("Import should be called when hash changed")
	}

	if len(receivedIssues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(receivedIssues))
	}

	if receivedIssues[0].ID != "test-1" {
		t.Errorf("Expected issue ID 'test-1', got '%s'", receivedIssues[0].ID)
	}
}

func TestAutoImportIfNewer_MergeConflict(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-autoimport-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create JSONL with merge conflict markers
	conflictData := `{"id":"test-1","title":"Issue 1"}
<<<<<<< HEAD
{"id":"test-2","title":"Local version"}
=======
{"id":"test-2","title":"Remote version"}
>>>>>>> main
{"id":"test-3","title":"Issue 3"}
`
	os.WriteFile(jsonlPath, []byte(conflictData), 0644)

	store := memory.New("")
	ctx := context.Background()
	notify := &testNotifier{}

	importFunc := func(ctx context.Context, issues []*types.Issue) (int, int, map[string]string, error) {
		t.Error("Import should not be called with merge conflict")
		return 0, 0, nil, nil
	}

	err = AutoImportIfNewer(ctx, store, dbPath, notify, importFunc, nil)
	if err == nil {
		t.Error("Expected error for merge conflict")
	}

	if len(notify.errors) == 0 {
		t.Error("Expected error notification")
	}
}

func TestAutoImportIfNewer_WithRemapping(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-autoimport-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create test JSONL
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(f).Encode(issue)
	f.Close()

	store := memory.New("")
	ctx := context.Background()
	notify := &testNotifier{}

	idMapping := map[string]string{"test-1": "test-2"}
	importFunc := func(ctx context.Context, issues []*types.Issue) (int, int, map[string]string, error) {
		return 1, 0, idMapping, nil
	}

	onChangedCalled := false
	var needsFullExport bool
	onChanged := func(fullExport bool) {
		onChangedCalled = true
		needsFullExport = fullExport
	}

	err = AutoImportIfNewer(ctx, store, dbPath, notify, importFunc, onChanged)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !onChangedCalled {
		t.Error("onChanged should be called when issues are remapped")
	}

	if !needsFullExport {
		t.Error("needsFullExport should be true when issues are remapped")
	}

	// Verify remapping was logged
	foundRemapping := false
	for _, info := range notify.infos {
		if strings.Contains(info, "remapped") {
			foundRemapping = true
			break
		}
	}
	if !foundRemapping {
		t.Error("Expected remapping notification")
	}
}

func TestCheckStaleness_NoMetadata(t *testing.T) {
	store := memory.New("")
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "bd-stale-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")

	stale, err := CheckStaleness(ctx, store, dbPath)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if stale {
		t.Error("Should not be stale with no metadata")
	}
}

func TestCheckStaleness_NewerJSONL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-stale-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create old import time
	oldTime := time.Now().Add(-1 * time.Hour)
	store := memory.New("")
	ctx := context.Background()
	store.SetMetadata(ctx, "last_import_time", oldTime.Format(time.RFC3339))

	// Create newer JSONL file
	os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`), 0644)

	stale, err := CheckStaleness(ctx, store, dbPath)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !stale {
		t.Error("Should be stale when JSONL is newer")
	}
}

func TestCheckStaleness_CorruptedMetadata(t *testing.T) {
	store := memory.New("")
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "bd-stale-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bd.db")

	// Set invalid timestamp format
	store.SetMetadata(ctx, "last_import_time", "not-a-valid-timestamp")

	_, err = CheckStaleness(ctx, store, dbPath)
	if err == nil {
		t.Error("Expected error for corrupted metadata, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "corrupted last_import_time") {
		t.Errorf("Expected 'corrupted last_import_time' error, got: %v", err)
	}
}

func TestCheckForMergeConflicts(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantError bool
	}{
		{
			name:      "no conflict",
			data:      `{"id":"test-1"}`,
			wantError: false,
		},
		{
			name: "conflict with HEAD marker",
			data: `<<<<<<< HEAD
{"id":"test-1"}`,
			wantError: true,
		},
		{
			name: "conflict with separator",
			data: `{"id":"test-1"}
=======
{"id":"test-2"}`,
			wantError: true,
		},
		{
			name: "conflict with end marker",
			data: `{"id":"test-1"}
>>>>>>> main`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkForMergeConflicts([]byte(tt.data), "test.jsonl")
			if tt.wantError && err == nil {
				t.Error("Expected error for merge conflict")
			}
			if !tt.wantError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestParseJSONL(t *testing.T) {
	notify := &testNotifier{}

	t.Run("valid jsonl", func(t *testing.T) {
		data := `{"id":"test-1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}
{"id":"test-2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`

		issues, err := parseJSONL([]byte(data), notify)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if len(issues) != 2 {
			t.Errorf("Expected 2 issues, got %d", len(issues))
		}
	})

	t.Run("empty lines ignored", func(t *testing.T) {
		data := `{"id":"test-1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}

{"id":"test-2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`

		issues, err := parseJSONL([]byte(data), notify)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if len(issues) != 2 {
			t.Errorf("Expected 2 issues, got %d", len(issues))
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		data := `{"id":"test-1","title":"Issue 1"}
not valid json`

		_, err := parseJSONL([]byte(data), notify)
		if err == nil {
			t.Error("Expected error for invalid JSON")
		}
	})

	t.Run("closed without closedAt", func(t *testing.T) {
		data := `{"id":"test-1","title":"Closed Issue","status":"closed","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`

		issues, err := parseJSONL([]byte(data), notify)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].ClosedAt == nil {
			t.Error("Expected ClosedAt to be set for closed issue")
		}
	})

	t.Run("migrate deleted status to tombstone (GH#1223)", func(t *testing.T) {
		now := time.Now()
		deletedAt := now.Format(time.RFC3339)
		data := `{"id":"test-1","title":"Deleted Issue","status":"deleted","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","deleted_at":"` + deletedAt + `"}`

		notify := &testNotifier{}
		issues, err := parseJSONL([]byte(data), notify)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if len(issues) != 1 {
			t.Errorf("Expected 1 issue, got %d", len(issues))
		}

		if issues[0].Status != types.StatusTombstone {
			t.Errorf("Expected status 'tombstone', got %s", issues[0].Status)
		}

		if issues[0].DeletedAt == nil {
			t.Error("Expected DeletedAt to be set for migrated tombstone")
		}

		// Check that debug message was logged
		if len(notify.debugs) == 0 {
			t.Error("Expected debug notification for status migration")
		}
	})

	t.Run("ensure tombstone has deleted_at", func(t *testing.T) {
		data := `{"id":"test-1","title":"Tombstone Without DeletedAt","status":"tombstone","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`

		notify := &testNotifier{}
		issues, err := parseJSONL([]byte(data), notify)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].DeletedAt == nil {
			t.Error("Expected DeletedAt to be auto-set for tombstone without deleted_at")
		}

		// Check that debug message was logged
		if len(notify.debugs) == 0 {
			t.Error("Expected debug notification for auto-added deleted_at")
		}
	})

	t.Run("fix corrupted closed issue with deleted_at (GH#1223)", func(t *testing.T) {
		now := time.Now()
		deletedAt := now.Format(time.RFC3339)
		data := `{"id":"bd-6s61","title":"Version Bump","status":"closed","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","closed_at":"2024-01-02T00:00:00Z","deleted_at":"` + deletedAt + `"}`

		notify := &testNotifier{}
		issues, err := parseJSONL([]byte(data), notify)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if len(issues) != 1 {
			t.Errorf("Expected 1 issue, got %d", len(issues))
		}

		// Issue should be converted to tombstone
		if issues[0].Status != types.StatusTombstone {
			t.Errorf("Expected status 'tombstone' for closed issue with deleted_at, got %s", issues[0].Status)
		}

		// Check that debug message was logged
		if len(notify.debugs) == 0 {
			t.Error("Expected debug notification for status correction")
		}
	})
}

func TestShowRemapping(t *testing.T) {
	notify := &testNotifier{}

	allIssues := []*types.Issue{
		{ID: "test-1", Title: "Issue 1"},
		{ID: "test-2", Title: "Issue 2"},
	}

	idMapping := map[string]string{
		"test-1": "test-3",
		"test-2": "test-4",
	}

	showRemapping(allIssues, idMapping, notify)

	if len(notify.infos) == 0 {
		t.Error("Expected info messages for remapping")
	}

	foundRemappingHeader := false
	for _, info := range notify.infos {
		if strings.Contains(info, "remapped") && strings.Contains(info, "colliding") {
			foundRemappingHeader = true
			break
		}
	}

	if !foundRemappingHeader {
		t.Errorf("Expected remapping summary message, got infos: %v", notify.infos)
	}
}

func TestStderrNotifier(t *testing.T) {
	t.Run("debug enabled", func(t *testing.T) {
		notify := NewStderrNotifier(true)
		// Just verify it doesn't panic
		notify.Debugf("test debug")
		notify.Infof("test info")
		notify.Warnf("test warn")
		notify.Errorf("test error")
	})

	t.Run("debug disabled", func(t *testing.T) {
		notify := NewStderrNotifier(false)
		// Just verify it doesn't panic
		notify.Debugf("test debug")
		notify.Infof("test info")
	})
}

