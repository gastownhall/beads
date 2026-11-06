package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

var (
	customStatusesQuery = regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")
	customTypesQuery    = regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")
	legacyConfigQuery   = regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")
)

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

// expectCustomVocabRead arms one successful loadCustomConfigCache read: a
// transaction reading both normalized vocabulary tables — and the legacy
// config rows when either table answers empty — rolled back like every store
// read.
func expectCustomVocabRead(mock sqlmock.Sqlmock, statuses []types.CustomStatus, customTypes []string) {
	mock.ExpectBegin()
	statusRows := sqlmock.NewRows([]string{"name", "category"})
	for _, s := range statuses {
		statusRows.AddRow(s.Name, string(s.Category))
	}
	mock.ExpectQuery(customStatusesQuery).WillReturnRows(statusRows)
	typeRows := sqlmock.NewRows([]string{"name"})
	for _, name := range customTypes {
		typeRows.AddRow(name)
	}
	mock.ExpectQuery(customTypesQuery).WillReturnRows(typeRows)
	if len(statuses) == 0 || len(customTypes) == 0 {
		mock.ExpectQuery(legacyConfigQuery).WithArgs("status.custom", "types.custom").
			WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	}
	mock.ExpectRollback()
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
		{
			name: "statuses select error",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery(customStatusesQuery).WillReturnError(errCustomVocabRead)
				mock.ExpectRollback()
			},
			ctx: func(t *testing.T) context.Context { return context.Background() },
		},
		{
			// A partial normalized-table success followed by a failed legacy
			// config read used to return the partial database result as a
			// successful vocabulary — and pin it. The resolver now fails the
			// whole read, so the fallback answers and the caches stay
			// unloaded for the next call.
			name: "legacy config select error after partial success",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery(customStatusesQuery).
					WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip"))
				mock.ExpectQuery(customTypesQuery).WillReturnRows(sqlmock.NewRows([]string{"name"}))
				mock.ExpectQuery(legacyConfigQuery).WithArgs("status.custom", "types.custom").
					WillReturnError(errCustomVocabRead)
				mock.ExpectRollback()
			},
			ctx: func(t *testing.T) context.Context { return context.Background() },
		},
	}
	for _, mode := range failureModes {
		t.Run(mode.name, func(t *testing.T) {
			// YAML vocabulary distinct from the database's, so a fallback
			// answer is recognizable as one.
			withCustomVocabYAML(t, "yamlstat", "yamltype")
			store, mock, counts := countedMockStore(t)
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

			// Recovery: without recreating the store or writing config, the
			// next read returns the database vocabulary — replacing the
			// status fallback and unioning the database types with the YAML
			// types, as a successful read always composed them.
			expectCustomVocabRead(mock, []types.CustomStatus{{Name: "review", Category: types.CategoryWIP}}, []string{"molecule"})
			statuses, err = store.GetCustomStatuses(context.Background())
			if err != nil {
				t.Fatalf("GetCustomStatuses after recovery: %v", err)
			}
			if len(statuses) != 1 || statuses[0] != "review" {
				t.Errorf("GetCustomStatuses after recovery = %v, want [review] from the database", statuses)
			}
			detailed, err = store.GetCustomStatusesDetailed(context.Background())
			if err != nil {
				t.Fatalf("GetCustomStatusesDetailed after recovery: %v", err)
			}
			if len(detailed) != 1 || detailed[0].Name != "review" || detailed[0].Category != types.CategoryWIP {
				t.Errorf("GetCustomStatusesDetailed after recovery = %v, want [review/wip] from the database", detailed)
			}
			customTypes, err = store.GetCustomTypes(context.Background())
			if err != nil {
				t.Fatalf("GetCustomTypes after recovery: %v", err)
			}
			if len(customTypes) != 2 || customTypes[0] != "molecule" || customTypes[1] != "yamltype" {
				t.Errorf("GetCustomTypes after recovery = %v, want [molecule yamltype] (database type unioned with config.yaml)", customTypes)
			}

			store.cacheMu.Lock()
			statusCached, typeCached = store.customStatusCached, store.customTypeCached
			store.cacheMu.Unlock()
			if !statusCached || !typeCached {
				t.Fatalf("cache flags after recovery = (%v, %v), want (true, true)", statusCached, typeCached)
			}

			// Cache hit: after a successful read, repeated getters must not
			// touch the driver at all — counted at the connector, since an
			// unexpected driver error swallowed by the fallback would not
			// show up as unmet expectations.
			prior := counts.Snapshot()
			_, err = store.GetCustomStatuses(context.Background())
			if err != nil {
				t.Fatalf("GetCustomStatuses cached repeat: %v", err)
			}
			_, err = store.GetCustomStatusesDetailed(context.Background())
			if err != nil {
				t.Fatalf("GetCustomStatusesDetailed cached repeat: %v", err)
			}
			_, err = store.GetCustomTypes(context.Background())
			if err != nil {
				t.Fatalf("GetCustomTypes cached repeat: %v", err)
			}
			if delta := counts.Snapshot().Sub(prior); delta != (sqlcount.Snapshot{}) {
				t.Errorf("repeated getters drove %+v of database traffic, want none: a successful read is cached", delta)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestCustomConfigCacheCachesEmptyVocabulary pins the other side of the
// contract: a successful read of an empty vocabulary — both normalized
// tables present and empty, legacy config rows absent, no config.yaml
// fallback — is cached exactly like a populated one, so repeated getters
// answer from the cache without another database read.
func TestCustomConfigCacheCachesEmptyVocabulary(t *testing.T) {
	withCustomVocabYAML(t, "", "")
	store, mock, counts := countedMockStore(t)
	expectCustomVocabRead(mock, nil, nil)

	statuses, err := store.GetCustomStatuses(context.Background())
	if err != nil {
		t.Fatalf("GetCustomStatuses: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("GetCustomStatuses on an empty vocabulary = %v, want empty", statuses)
	}
	customTypes, err := store.GetCustomTypes(context.Background())
	if err != nil {
		t.Fatalf("GetCustomTypes: %v", err)
	}
	if len(customTypes) != 0 {
		t.Errorf("GetCustomTypes on an empty vocabulary = %v, want empty", customTypes)
	}

	store.cacheMu.Lock()
	statusCached, typeCached := store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if !statusCached || !typeCached {
		t.Fatalf("cache flags after an empty read = (%v, %v), want (true, true): a successful empty read is cached", statusCached, typeCached)
	}

	prior := counts.Snapshot()
	_, _ = store.GetCustomStatuses(context.Background())
	_, _ = store.GetCustomStatusesDetailed(context.Background())
	_, _ = store.GetCustomTypes(context.Background())
	if delta := counts.Snapshot().Sub(prior); delta != (sqlcount.Snapshot{}) {
		t.Errorf("repeated getters drove %+v of database traffic, want none", delta)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCustomConfigCachePartialInvalidationKeepsValidHalf pins the !cached
// guards around partial invalidation: when one half of the vocabulary is
// invalidated and the reload fails, the still-valid half keeps both its
// value and its loaded flag, the failed half answers from config.yaml
// without being certified loaded, and the next successful read refreshes
// only the invalidated half.
func TestCustomConfigCachePartialInvalidationKeepsValidHalf(t *testing.T) {
	withCustomVocabYAML(t, "yamlstat", "yamltype")
	store, mock, _ := countedMockStore(t)
	expectCustomVocabRead(mock, []types.CustomStatus{{Name: "review", Category: types.CategoryWIP}}, []string{"molecule"})
	if _, err := store.GetCustomStatuses(context.Background()); err != nil {
		t.Fatalf("warm GetCustomStatuses: %v", err)
	}

	// Invalidate only the type half; the reload fails.
	store.invalidateConfigCaches("types.custom")
	mock.ExpectBegin().WillReturnError(errCustomVocabRead)
	if _, err := store.GetCustomTypes(context.Background()); err != nil {
		t.Fatalf("GetCustomTypes during failed reload: %v", err)
	}
	store.cacheMu.Lock()
	statuses, customTypes := store.customStatusCache, store.customTypeCache
	statusCached, typeCached := store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if len(statuses) != 1 || statuses[0] != "review" || !statusCached {
		t.Errorf("status half after failed type reload = (%v, %v), want ([review], true) untouched", statuses, statusCached)
	}
	if len(customTypes) != 1 || customTypes[0] != "yamltype" || typeCached {
		t.Errorf("type half after failed reload = (%v, %v), want ([yamltype] served from fallback, false)", customTypes, typeCached)
	}

	// The next successful read refreshes only the invalidated half.
	expectCustomVocabRead(mock, []types.CustomStatus{{Name: "review", Category: types.CategoryWIP}}, []string{"molecule"})
	customTypes, err := store.GetCustomTypes(context.Background())
	if err != nil {
		t.Fatalf("GetCustomTypes after recovery: %v", err)
	}
	if len(customTypes) != 2 || customTypes[0] != "molecule" || customTypes[1] != "yamltype" {
		t.Errorf("GetCustomTypes after type recovery = %v, want [molecule yamltype]", customTypes)
	}
	store.cacheMu.Lock()
	statuses = store.customStatusCache
	statusCached, typeCached = store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if len(statuses) != 1 || statuses[0] != "review" {
		t.Errorf("status half after type recovery = %v, want [review] still guarded", statuses)
	}
	if !statusCached || !typeCached {
		t.Errorf("cache flags after type recovery = (%v, %v), want (true, true)", statusCached, typeCached)
	}

	// Mirror: invalidate only the status half; the reload fails and the type
	// half stays intact.
	store.invalidateConfigCaches("status.custom")
	mock.ExpectBegin().WillReturnError(errCustomVocabRead)
	if _, err := store.GetCustomStatuses(context.Background()); err != nil {
		t.Fatalf("GetCustomStatuses during failed reload: %v", err)
	}
	store.cacheMu.Lock()
	statuses, customTypes = store.customStatusCache, store.customTypeCache
	statusCached, typeCached = store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if len(customTypes) != 2 || customTypes[0] != "molecule" || !typeCached {
		t.Errorf("type half after failed status reload = (%v, %v), want ([molecule yamltype], true) untouched", customTypes, typeCached)
	}
	if len(statuses) != 1 || statuses[0] != "yamlstat" || statusCached {
		t.Errorf("status half after failed reload = (%v, %v), want ([yamlstat] served from fallback, false)", statuses, statusCached)
	}

	expectCustomVocabRead(mock, []types.CustomStatus{{Name: "review", Category: types.CategoryWIP}}, []string{"molecule"})
	statuses, err = store.GetCustomStatuses(context.Background())
	if err != nil {
		t.Fatalf("GetCustomStatuses after recovery: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != "review" {
		t.Errorf("GetCustomStatuses after status recovery = %v, want [review]", statuses)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCustomConfigCacheRecoversRetryableErrorWithinCall pins that a
// retryable SELECT failure flows through the existing read wrapper's retry
// unchanged: one failed attempt followed by a successful transaction answers
// the same getter call, with no new retry policy involved.
func TestCustomConfigCacheRecoversRetryableErrorWithinCall(t *testing.T) {
	withCustomVocabYAML(t, "yamlstat", "yamltype")
	store, mock, _ := countedMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(customStatusesQuery).WillReturnError(errors.New("connection reset by peer"))
	mock.ExpectRollback()
	expectCustomVocabRead(mock, []types.CustomStatus{{Name: "review", Category: types.CategoryWIP}}, []string{"molecule"})

	statuses, err := store.GetCustomStatuses(context.Background())
	if err != nil {
		t.Fatalf("GetCustomStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != "review" {
		t.Errorf("GetCustomStatuses = %v, want [review] recovered within the call", statuses)
	}
	store.cacheMu.Lock()
	statusCached, typeCached := store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if !statusCached || !typeCached {
		t.Errorf("cache flags = (%v, %v), want (true, true) after in-call recovery", statusCached, typeCached)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
