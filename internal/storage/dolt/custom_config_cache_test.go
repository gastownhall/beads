package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// The custom status/type vocabulary cache (bd-dpn4op, GH#5581): a read that
// fails must answer that one call from config.yaml without marking the caches
// loaded, so the next getter reads the database again instead of serving the
// fallback until the store is recreated.

var errCustomConfigInjected = errors.New("injected custom config read failure")

var (
	ccStatusesQuery = regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")
	ccTypesQuery    = regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")
	ccConfigQuery   = regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")
)

// withCustomVocabularyYAML points the process config at a throwaway
// .beads/config.yaml declaring status.custom and types.custom ("" = unset),
// with HOME and XDG_CONFIG_HOME isolated so no user-level config leaks in.
func withCustomVocabularyYAML(t *testing.T, statusCSV, typeCSV string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	content := ""
	if statusCSV != "" {
		content += "status:\n  custom: \"" + statusCSV + "\"\n"
	}
	if typeCSV != "" {
		content += "types:\n  custom: \"" + typeCSV + "\"\n"
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

// ccFailure arms one vocabulary read that ultimately fails and returns the
// context the failing getter call must use.
type ccFailure struct {
	name string
	arm  func(t *testing.T, mock sqlmock.Sqlmock) context.Context
}

var ccReadFailures = []ccFailure{
	{"begin fails", func(_ *testing.T, mock sqlmock.Sqlmock) context.Context {
		mock.ExpectBegin().WillReturnError(errCustomConfigInjected)
		return context.Background()
	}},
	{"context cancelled", func(t *testing.T, _ sqlmock.Sqlmock) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}},
}

// ccGetter is one of the three public vocabulary getters, reduced to the
// names it reports.
type ccGetter struct {
	name string
	get  func(ctx context.Context, s *DoltStore) ([]string, error)
}

var ccGetters = []ccGetter{
	{"GetCustomStatuses", func(ctx context.Context, s *DoltStore) ([]string, error) {
		return s.GetCustomStatuses(ctx)
	}},
	{"GetCustomStatusesDetailed", func(ctx context.Context, s *DoltStore) ([]string, error) {
		detailed, err := s.GetCustomStatusesDetailed(ctx)
		var out []string
		for _, st := range detailed {
			out = append(out, st.Name+":"+string(st.Category))
		}
		return out, err
	}},
	{"GetCustomTypes", func(ctx context.Context, s *DoltStore) ([]string, error) {
		return s.GetCustomTypes(ctx)
	}},
}

func ccFlags(s *DoltStore) (statusLoaded, typeLoaded bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return s.customStatusCached, s.customTypeCached
}

func ccAssertNames(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", label, got, want)
	}
}

// TestCustomConfigCacheFailedReadServesFallbackUnloaded pins AC-1: a getter
// whose read fails answers from config.yaml (empty when absent) with a nil
// error and leaves both caches unloaded.
func TestCustomConfigCacheFailedReadServesFallbackUnloaded(t *testing.T) {
	yamlCases := []struct {
		name               string
		statusCSV, typeCSV string
		want               map[string][]string
	}{
		{"yaml set", "triage:wip", "gizmo", map[string][]string{
			"GetCustomStatuses":         {"triage"},
			"GetCustomStatusesDetailed": {"triage:" + string(types.CategoryWIP)},
			"GetCustomTypes":            {"gizmo"},
		}},
		{"yaml absent", "", "", map[string][]string{}},
	}
	for _, yc := range yamlCases {
		for _, failure := range ccReadFailures {
			for _, getter := range ccGetters {
				t.Run(yc.name+"/"+failure.name+"/"+getter.name, func(t *testing.T) {
					withCustomVocabularyYAML(t, yc.statusCSV, yc.typeCSV)
					store, mock, _ := vhCountedStore(t)
					ctx := failure.arm(t, mock)

					got, err := getter.get(ctx, store)
					if err != nil {
						t.Fatalf("%s after a failed read: error %v, want nil", getter.name, err)
					}
					ccAssertNames(t, getter.name, got, yc.want[getter.name])
					if statusLoaded, typeLoaded := ccFlags(store); statusLoaded || typeLoaded {
						t.Errorf("cache flags after a failed read = (status %v, type %v), want both false", statusLoaded, typeLoaded)
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Errorf("unmet SQL expectations: %v", err)
					}
				})
			}
		}
	}
}

// ccStatusRow is one custom_statuses row.
type ccStatusRow struct{ name, category string }

// expectVocabularyRead arms one read transaction that resolves both
// normalized tables. When either table is empty the resolver also reads the
// legacy config rows, which configRows supplies (key, value pairs).
func expectVocabularyRead(mock sqlmock.Sqlmock, statuses []ccStatusRow, typeNames []string, configRows ...[2]string) {
	mock.ExpectBegin()
	statusRows := sqlmock.NewRows([]string{"name", "category"})
	for _, st := range statuses {
		statusRows.AddRow(st.name, st.category)
	}
	mock.ExpectQuery(ccStatusesQuery).WillReturnRows(statusRows)
	typeRows := sqlmock.NewRows([]string{"name"})
	for _, n := range typeNames {
		typeRows.AddRow(n)
	}
	mock.ExpectQuery(ccTypesQuery).WillReturnRows(typeRows)
	if len(statuses) == 0 || len(typeNames) == 0 {
		cfgRows := sqlmock.NewRows([]string{"key", "value"})
		for _, kv := range configRows {
			cfgRows.AddRow(kv[0], kv[1])
		}
		mock.ExpectQuery(ccConfigQuery).WithArgs("status.custom", "types.custom").WillReturnRows(cfgRows)
	}
	mock.ExpectRollback()
}

// ccAssertVocabulary calls all three getters with a live context and checks
// the names each reports.
func ccAssertVocabulary(t *testing.T, store *DoltStore, want map[string][]string) {
	t.Helper()
	for _, getter := range ccGetters {
		got, err := getter.get(context.Background(), store)
		if err != nil {
			t.Fatalf("%s: %v", getter.name, err)
		}
		ccAssertNames(t, getter.name, got, want[getter.name])
	}
}

var ccStatusSelectFails = ccFailure{"custom_statuses select fails", func(_ *testing.T, mock sqlmock.Sqlmock) context.Context {
	mock.ExpectBegin()
	mock.ExpectQuery(ccStatusesQuery).WillReturnError(errCustomConfigInjected)
	mock.ExpectRollback()
	return context.Background()
}}

var ccDatabaseVocabulary = map[string][]string{
	"GetCustomStatuses":         {"review"},
	"GetCustomStatusesDetailed": {"review:wip"},
	"GetCustomTypes":            {"molecule"},
}

// TestCustomConfigCacheRereadsAfterFailedRead pins AC-2: after a failed
// vocabulary read, the next getter reads the database again and all three
// getters report the database vocabulary, with no store recreation or config
// write in between.
func TestCustomConfigCacheRereadsAfterFailedRead(t *testing.T) {
	failures := append(append([]ccFailure{}, ccReadFailures...), ccStatusSelectFails)
	for _, failure := range failures {
		for _, getter := range ccGetters {
			t.Run(failure.name+"/"+getter.name, func(t *testing.T) {
				withCustomVocabularyYAML(t, "", "")
				store, mock, _ := vhCountedStore(t)
				ctx := failure.arm(t, mock)
				if _, err := getter.get(ctx, store); err != nil {
					t.Fatalf("%s after a failed read: error %v, want nil", getter.name, err)
				}
				if statusLoaded, typeLoaded := ccFlags(store); statusLoaded || typeLoaded {
					t.Fatalf("cache flags after a failed read = (status %v, type %v), want both false", statusLoaded, typeLoaded)
				}

				expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
				ccAssertVocabulary(t, store, ccDatabaseVocabulary)
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Errorf("the read after a failure was not made: %v", err)
				}
				if statusLoaded, typeLoaded := ccFlags(store); !statusLoaded || !typeLoaded {
					t.Errorf("cache flags after a successful read = (status %v, type %v), want both true", statusLoaded, typeLoaded)
				}
			})
		}
	}

	// config.yaml distinct from the database: the failed call serves YAML,
	// recovery replaces the status fallback and unions types with YAML as
	// before.
	t.Run("yaml distinct from database", func(t *testing.T) {
		withCustomVocabularyYAML(t, "triage:wip", "gizmo")
		store, mock, _ := vhCountedStore(t)
		ccStatusSelectFails.arm(t, mock)
		got, err := store.GetCustomTypes(context.Background())
		if err != nil {
			t.Fatalf("GetCustomTypes after a failed read: %v", err)
		}
		ccAssertNames(t, "GetCustomTypes after a failed read", got, []string{"gizmo"})

		expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
		ccAssertVocabulary(t, store, map[string][]string{
			"GetCustomStatuses":         {"review"},
			"GetCustomStatusesDetailed": {"review:wip"},
			"GetCustomTypes":            {"molecule", "gizmo"},
		})
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet SQL expectations: %v", err)
		}
	})

	// The existing read wrapper retries a transient error within the call;
	// nothing new is added on top of it.
	t.Run("retryable select error recovers within the call", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "")
		store, mock, _ := vhCountedStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(ccStatusesQuery).WillReturnError(errors.New("write: broken pipe"))
		mock.ExpectRollback()
		expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
		ccAssertVocabulary(t, store, ccDatabaseVocabulary)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet SQL expectations: %v", err)
		}
	})
}

// TestCustomConfigCacheRereadsAfterPartialReadFailure: when the normalized
// statuses read but the legacy config read fails, the failed call keeps
// nothing from the partial database read: the unloaded caches hold
// config.yaml alone and stay unloaded, and the next call reads the database
// again.
func TestCustomConfigCacheRereadsAfterPartialReadFailure(t *testing.T) {
	withCustomVocabularyYAML(t, "triage:wip", "gizmo")
	store, mock, _ := vhCountedStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(ccStatusesQuery).WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip"))
	mock.ExpectQuery(ccTypesQuery).WillReturnRows(sqlmock.NewRows([]string{"name"}))
	mock.ExpectQuery(ccConfigQuery).WithArgs("status.custom", "types.custom").WillReturnError(errCustomConfigInjected)
	mock.ExpectRollback()

	if _, err := store.GetCustomStatuses(context.Background()); err != nil {
		t.Fatalf("GetCustomStatuses after a partial read: %v", err)
	}
	store.cacheMu.Lock()
	statuses, detailed, customTypes := store.customStatusCache, store.customStatusDetailedCache, store.customTypeCache
	statusLoaded, typeLoaded := store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	ccAssertNames(t, "status cache after a partial read", statuses, []string{"triage"})
	if len(detailed) != 1 || detailed[0] != (types.CustomStatus{Name: "triage", Category: types.CategoryWIP}) {
		t.Errorf("detailed status cache after a partial read = %#v, want [triage:wip]", detailed)
	}
	ccAssertNames(t, "type cache after a partial read", customTypes, []string{"gizmo"})
	if statusLoaded || typeLoaded {
		t.Errorf("cache flags after a partial read = (status %v, type %v), want both false", statusLoaded, typeLoaded)
	}

	expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
	ccAssertVocabulary(t, store, map[string][]string{
		"GetCustomStatuses":         {"review"},
		"GetCustomStatusesDetailed": {"review:wip"},
		"GetCustomTypes":            {"molecule", "gizmo"},
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations: %v", err)
	}
}

// TestCustomConfigCachePartialInvalidationKeepsValidHalf pins the !cached
// guards around partial invalidation: when one half of the vocabulary is
// invalidated and the reload fails, the still-valid half keeps both its
// value and its loaded flag, the failed half answers from config.yaml
// without being certified loaded, and the next successful read refreshes
// only the invalidated half.
func TestCustomConfigCachePartialInvalidationKeepsValidHalf(t *testing.T) {
	withCustomVocabularyYAML(t, "yamlstat", "yamltype")
	store, mock, _ := vhCountedStore(t)
	expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
	if _, err := store.GetCustomStatuses(context.Background()); err != nil {
		t.Fatalf("warm GetCustomStatuses: %v", err)
	}

	// Invalidate only the type half; the reload fails.
	store.invalidateConfigCaches("types.custom")
	mock.ExpectBegin().WillReturnError(errCustomConfigInjected)
	if _, err := store.GetCustomTypes(context.Background()); err != nil {
		t.Fatalf("GetCustomTypes during failed reload: %v", err)
	}
	store.cacheMu.Lock()
	statuses, customTypes := store.customStatusCache, store.customTypeCache
	statusLoaded, typeLoaded := store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if !reflect.DeepEqual(statuses, []string{"review"}) || !statusLoaded {
		t.Errorf("status half after failed type reload = (%v, %v), want ([review], true) untouched", statuses, statusLoaded)
	}
	if !reflect.DeepEqual(customTypes, []string{"yamltype"}) || typeLoaded {
		t.Errorf("type half after failed reload = (%v, %v), want ([yamltype] served from fallback, false)", customTypes, typeLoaded)
	}

	// The next successful read refreshes only the invalidated half.
	expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
	customTypes, err := store.GetCustomTypes(context.Background())
	if err != nil {
		t.Fatalf("GetCustomTypes after recovery: %v", err)
	}
	ccAssertNames(t, "GetCustomTypes after type recovery", customTypes, []string{"molecule", "yamltype"})
	store.cacheMu.Lock()
	statuses = store.customStatusCache
	statusLoaded, typeLoaded = store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	ccAssertNames(t, "status half after type recovery", statuses, []string{"review"})
	if !statusLoaded || !typeLoaded {
		t.Errorf("cache flags after type recovery = (status %v, type %v), want both true", statusLoaded, typeLoaded)
	}

	// Mirror: invalidate only the status half; the reload fails and the type
	// half stays intact.
	store.invalidateConfigCaches("status.custom")
	mock.ExpectBegin().WillReturnError(errCustomConfigInjected)
	if _, err := store.GetCustomStatuses(context.Background()); err != nil {
		t.Fatalf("GetCustomStatuses during failed reload: %v", err)
	}
	store.cacheMu.Lock()
	statuses, customTypes = store.customStatusCache, store.customTypeCache
	statusLoaded, typeLoaded = store.customStatusCached, store.customTypeCached
	store.cacheMu.Unlock()
	if !reflect.DeepEqual(customTypes, []string{"molecule", "yamltype"}) || !typeLoaded {
		t.Errorf("type half after failed status reload = (%v, %v), want ([molecule yamltype], true) untouched", customTypes, typeLoaded)
	}
	if !reflect.DeepEqual(statuses, []string{"yamlstat"}) || statusLoaded {
		t.Errorf("status half after failed reload = (%v, %v), want ([yamlstat] served from fallback, false)", statuses, statusLoaded)
	}

	expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
	statuses, err = store.GetCustomStatuses(context.Background())
	if err != nil {
		t.Fatalf("GetCustomStatuses after recovery: %v", err)
	}
	ccAssertNames(t, "GetCustomStatuses after status recovery", statuses, []string{"review"})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations: %v", err)
	}
}
