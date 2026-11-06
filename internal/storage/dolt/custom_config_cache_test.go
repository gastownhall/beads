package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// errCustomVocabRead is the non-retryable failure injected at the driver: its
// message must not match any isRetryableError pattern, so a failing read ends
// the call at once instead of retrying inside it.
var errCustomVocabRead = errors.New("custom vocabulary read failed")

var customVocabMockSeq atomic.Int64

// countedMockStore returns a DoltStore over a fresh sqlmock connection that
// is reached through sqlcount, after the versioned-history activation-cost
// pattern: the store sees a plain *sql.DB while every driver round trip —
// Begin included — is counted, so a cache-hit proof can later show zero
// traffic rather than merely absent expectations.
func countedMockStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock, *sqlcount.Counts) {
	t.Helper()
	dsn := fmt.Sprintf("dolt_custom_vocab_%d", customVocabMockSeq.Add(1))
	mockDB, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatalf("sqlmock.NewWithDSN: %v", err)
	}
	counts := &sqlcount.Counts{}
	db := sql.OpenDB(sqlcount.WrapConnector(vhMockConnector{dsn: dsn, driver: mockDB.Driver()}, counts))
	t.Cleanup(func() {
		_ = db.Close()
		_ = mockDB.Close()
	})
	return &DoltStore{db: db}, mock, counts
}

// withCustomVocabYAML points the process config at a throwaway
// .beads/config.yaml declaring status.custom and types.custom, with HOME and
// XDG_CONFIG_HOME isolated so no user-level config can leak in. Subtests that
// use it must not parallelize: the global config is swapped for the test.
func withCustomVocabYAML(t *testing.T, statusCSV, typesCSV string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	content := "status:\n"
	if statusCSV != "" {
		content += "  custom: \"" + statusCSV + "\"\n"
	}
	content += "types:\n"
	if typesCSV != "" {
		content += "  custom: \"" + typesCSV + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(dir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

// TestCustomConfigCacheRereadsAfterFailedRead pins GH#5581: a custom
// status/type read that ultimately fails must answer that one call from the
// config.yaml fallback with a nil error while leaving both caches unloaded,
// so the next getter reads the database again instead of serving the
// fallback for the rest of the store's life.
func TestCustomConfigCacheRereadsAfterFailedRead(t *testing.T) {
	failureModes := []struct {
		name string
		arm  func(mock sqlmock.Sqlmock)
		ctx  func(t *testing.T) context.Context
	}{
		{
			name: "begin error",
			arm:  func(mock sqlmock.Sqlmock) { mock.ExpectBegin().WillReturnError(errCustomVocabRead) },
			ctx:  func(t *testing.T) context.Context { return context.Background() },
		},
		{
			name: "cancelled context",
			arm:  func(mock sqlmock.Sqlmock) {},
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
	}
	for _, mode := range failureModes {
		t.Run(mode.name, func(t *testing.T) {
			// YAML vocabulary distinct from the database's, so a fallback
			// answer is recognizable as one.
			withCustomVocabYAML(t, "yamlstat", "yamltype")
			store, mock, _ := countedMockStore(t)
			mode.arm(mock)

			detailed, err := store.GetCustomStatusesDetailed(mode.ctx(t))
			if err != nil {
				t.Fatalf("GetCustomStatusesDetailed: %v", err)
			}
			if len(detailed) != 1 || detailed[0].Name != "yamlstat" || detailed[0].Category != types.CategoryUnspecified {
				t.Errorf("failed read answered %v, want the config.yaml fallback [yamlstat]", detailed)
			}

			store.cacheMu.Lock()
			statuses := store.customStatusCache
			customTypes := store.customTypeCache
			statusCached, typeCached := store.customStatusCached, store.customTypeCached
			store.cacheMu.Unlock()
			if len(statuses) != 1 || statuses[0] != "yamlstat" {
				t.Errorf("status cache after failed read = %v, want [yamlstat] served from fallback", statuses)
			}
			if len(customTypes) != 1 || customTypes[0] != "yamltype" {
				t.Errorf("type cache after failed read = %v, want [yamltype] served from fallback", customTypes)
			}
			if statusCached || typeCached {
				t.Fatalf("cache flags after failed read = (%v, %v), want (false, false): a failed read must not pin the fallback as loaded", statusCached, typeCached)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}
