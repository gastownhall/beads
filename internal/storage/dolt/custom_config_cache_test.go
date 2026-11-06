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

	// One cache invalidated alone: a failed reload leaves the other cache
	// loaded and intact, and the next read restores the invalidated one.
	for _, key := range []string{"types.custom", "status.custom"} {
		t.Run("partial invalidation of "+key, func(t *testing.T) {
			withCustomVocabularyYAML(t, "", "")
			store, mock, _ := vhCountedStore(t)
			expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
			ccAssertVocabulary(t, store, ccDatabaseVocabulary)

			store.invalidateConfigCaches(key)
			mock.ExpectBegin().WillReturnError(errCustomConfigInjected)
			if _, err := store.GetCustomTypes(context.Background()); err != nil {
				t.Fatalf("GetCustomTypes after a failed reload: %v", err)
			}
			statusLoaded, typeLoaded := ccFlags(store)
			if key == "types.custom" {
				if !statusLoaded || typeLoaded {
					t.Errorf("flags after a failed type reload = (status %v, type %v), want (true, false)", statusLoaded, typeLoaded)
				}
				got, _ := store.GetCustomStatusesDetailed(context.Background())
				if len(got) != 1 || got[0] != (types.CustomStatus{Name: "review", Category: types.CategoryWIP}) {
					t.Errorf("status cache after a failed type reload = %#v, want [review:wip]", got)
				}
			} else if statusLoaded || !typeLoaded {
				t.Errorf("flags after a failed status reload = (status %v, type %v), want (false, true)", statusLoaded, typeLoaded)
			}

			expectVocabularyRead(mock, []ccStatusRow{{"review", "wip"}}, []string{"molecule"})
			ccAssertVocabulary(t, store, ccDatabaseVocabulary)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestCustomConfigCacheHitSkipsDatabase pins AC-3: a successful read, empty
// included, stays cached, so repeated getters make no driver call. The calls
// are counted at the driver because an unexpected sqlmock statement would
// fail quietly into the public fallback.
func TestCustomConfigCacheHitSkipsDatabase(t *testing.T) {
	cases := []struct {
		name     string
		statuses []ccStatusRow
		types    []string
		want     map[string][]string
	}{
		{"populated", []ccStatusRow{{"review", "wip"}}, []string{"molecule"}, ccDatabaseVocabulary},
		{"empty", nil, nil, map[string][]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCustomVocabularyYAML(t, "", "")
			store, mock, counts := vhCountedStore(t)
			expectVocabularyRead(mock, tc.statuses, tc.types)
			ccAssertVocabulary(t, store, tc.want)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
			if statusLoaded, typeLoaded := ccFlags(store); !statusLoaded || !typeLoaded {
				t.Fatalf("cache flags after a successful read = (status %v, type %v), want both true", statusLoaded, typeLoaded)
			}

			before := counts.Snapshot()
			for range 3 {
				ccAssertVocabulary(t, store, tc.want)
			}
			if delta := counts.Snapshot().Sub(before); delta.Total() != 0 {
				t.Errorf("cached getters reached the database: %+v", delta)
			}
		})
	}
}
