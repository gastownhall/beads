package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/config"
)

// withCustomConfigYAML points the process config at a throwaway
// .beads/config.yaml holding yaml, with HOME and XDG_CONFIG_HOME isolated so
// no user-level config can leak in. An empty yaml declares no vocabulary.
func withCustomConfigYAML(t *testing.T, yaml string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(dir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

func newCustomConfigMockStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db}, mock
}

// expectCustomConfigRead expects one successful combined read whose
// normalized tables hold statuses ("name:category") and customTypes. Either
// table may be empty, in which case the config rows are read as well.
func expectCustomConfigRead(mock sqlmock.Sqlmock, statuses [][2]string, customTypes []string) {
	mock.ExpectBegin()
	statusRows := sqlmock.NewRows([]string{"name", "category"})
	for _, st := range statuses {
		statusRows.AddRow(st[0], st[1])
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(statusRows)
	typeRows := sqlmock.NewRows([]string{"name"})
	for _, name := range customTypes {
		typeRows.AddRow(name)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")).
		WillReturnRows(typeRows)
	if len(statuses) == 0 || len(customTypes) == 0 {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")).
			WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	}
	mock.ExpectRollback()
}

type customConfigGetter struct {
	name string
	// get returns the getter's vocabulary as a slice of names.
	get func(ctx context.Context, s *DoltStore) ([]string, error)
	// cached reports whether the getter's half of the cache is populated.
	cached func(s *DoltStore) bool
}

var customConfigGetters = []customConfigGetter{
	{
		name: "GetCustomStatuses",
		get:  func(ctx context.Context, s *DoltStore) ([]string, error) { return s.GetCustomStatuses(ctx) },
		cached: func(s *DoltStore) bool {
			return s.customStatusCached || s.customStatusCache != nil || s.customStatusDetailedCache != nil
		},
	},
	{
		name: "GetCustomStatusesDetailed",
		get: func(ctx context.Context, s *DoltStore) ([]string, error) {
			statuses, err := s.GetCustomStatusesDetailed(ctx)
			if statuses == nil {
				return nil, err
			}
			names := make([]string, 0, len(statuses))
			for _, st := range statuses {
				names = append(names, st.Name+":"+string(st.Category))
			}
			return names, err
		},
		cached: func(s *DoltStore) bool {
			return s.customStatusCached || s.customStatusCache != nil || s.customStatusDetailedCache != nil
		},
	},
	{
		name:   "GetCustomTypes",
		get:    func(ctx context.Context, s *DoltStore) ([]string, error) { return s.GetCustomTypes(ctx) },
		cached: func(s *DoltStore) bool { return s.customTypeCached || s.customTypeCache != nil },
	},
}

// GH#5581: a failed custom-config read with no config.yaml vocabulary to fall
// back on must surface its cause and leave the cache empty, instead of
// returning (nil, nil) and caching "no custom vocabulary" for the store's
// lifetime.
func TestCustomConfigGettersDoNotCacheFailedResolve(t *testing.T) {
	withCustomConfigYAML(t, "")
	for _, g := range customConfigGetters {
		t.Run(g.name, func(t *testing.T) {
			store, mock := newCustomConfigMockStore(t)
			injected := errors.New("simulated read failure")
			mock.ExpectBegin().WillReturnError(injected)

			got, err := g.get(context.Background(), store)
			if !errors.Is(err, injected) {
				t.Fatalf("%s err = %v, want it to wrap %v", g.name, err, injected)
			}
			if got != nil {
				t.Fatalf("%s = %v, want no vocabulary on failure", g.name, got)
			}
			if g.cached(store) {
				t.Fatalf("%s populated its cache after a failed read", g.name)
			}
			if store.customStatusCached || store.customTypeCached {
				t.Fatalf("failed read set cache flags: status=%v type=%v", store.customStatusCached, store.customTypeCached)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

const (
	yamlCustomStatuses = "status:\n  custom: \"review:active,parked:frozen\"\n"
	yamlCustomTypes    = "types:\n  custom: \"spike, spike,research\"\n"
)

// GH#5581: when the read fails but config.yaml declares the requested
// vocabulary, the getter serves the parsed config.yaml vocabulary for that
// call only; the degraded answer is never cached.
func TestCustomConfigGettersServeUncachedYAMLFallback(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		// want maps a getter to its vocabulary; a missing getter must fail.
		want map[string][]string
	}{
		{
			name: "both vocabularies declared",
			yaml: yamlCustomStatuses + yamlCustomTypes,
			want: map[string][]string{
				"GetCustomStatuses":         {"review", "parked"},
				"GetCustomStatusesDetailed": {"review:active", "parked:frozen"},
				"GetCustomTypes":            {"spike", "research"},
			},
		},
		{
			name: "only statuses declared",
			yaml: yamlCustomStatuses,
			want: map[string][]string{
				"GetCustomStatuses":         {"review", "parked"},
				"GetCustomStatusesDetailed": {"review:active", "parked:frozen"},
			},
		},
		{
			name: "only types declared",
			yaml: yamlCustomTypes,
			want: map[string][]string{
				"GetCustomTypes": {"spike", "research"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCustomConfigYAML(t, tc.yaml)
			for _, g := range customConfigGetters {
				t.Run(g.name, func(t *testing.T) {
					store, mock := newCustomConfigMockStore(t)
					injected := errors.New("simulated read failure")
					mock.ExpectBegin().WillReturnError(injected)

					got, err := g.get(context.Background(), store)
					want, ok := tc.want[g.name]
					switch {
					case ok && err != nil:
						t.Fatalf("%s err = %v, want config.yaml fallback %v", g.name, err, want)
					case ok && !slices.Equal(got, want):
						t.Fatalf("%s = %v, want config.yaml fallback %v", g.name, got, want)
					case !ok && (!errors.Is(err, injected) || got != nil):
						t.Fatalf("%s = %v, %v; want no vocabulary and an error wrapping %v", g.name, got, err, injected)
					}
					if store.customStatusCached || store.customTypeCached ||
						store.customStatusCache != nil || store.customStatusDetailedCache != nil || store.customTypeCache != nil {
						t.Fatalf("%s cached the degraded config.yaml fallback", g.name)
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatalf("unmet SQL expectations: %v", err)
					}
				})
			}
		})
	}
}

// GH#5581: after a failed read returned an error or the config.yaml fallback,
// the next call on the same store reads the database again, returns the
// database-backed vocabulary composed with config.yaml as usual, and caches it.
func TestCustomConfigGettersRecoverAfterFailedResolve(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		// before is each getter's answer to the failed read; nil means an error.
		before map[string][]string
		after  map[string][]string
	}{
		{
			name: "after an error",
			yaml: "",
			after: map[string][]string{
				"GetCustomStatuses":         {"review"},
				"GetCustomStatusesDetailed": {"review:active"},
				"GetCustomTypes":            {"spike"},
			},
		},
		{
			name: "after the config.yaml fallback",
			yaml: yamlCustomStatuses + yamlCustomTypes,
			before: map[string][]string{
				"GetCustomStatuses":         {"review", "parked"},
				"GetCustomStatusesDetailed": {"review:active", "parked:frozen"},
				"GetCustomTypes":            {"spike", "research"},
			},
			// The normalized status table wins over config.yaml; custom
			// types are the database set unioned with config.yaml's.
			after: map[string][]string{
				"GetCustomStatuses":         {"review"},
				"GetCustomStatusesDetailed": {"review:active"},
				"GetCustomTypes":            {"spike", "research"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCustomConfigYAML(t, tc.yaml)
			for _, g := range customConfigGetters {
				t.Run(g.name, func(t *testing.T) {
					store, mock := newCustomConfigMockStore(t)
					ctx := context.Background()
					injected := errors.New("simulated read failure")
					mock.ExpectBegin().WillReturnError(injected)
					expectCustomConfigRead(mock, [][2]string{{"review", "active"}}, []string{"spike"})

					got, err := g.get(ctx, store)
					if want := tc.before[g.name]; want == nil {
						if !errors.Is(err, injected) {
							t.Fatalf("failed read: %s err = %v, want it to wrap %v", g.name, err, injected)
						}
					} else if err != nil || !slices.Equal(got, want) {
						t.Fatalf("failed read: %s = %v, %v; want %v", g.name, got, err, want)
					}

					want := tc.after[g.name]
					got, err = g.get(ctx, store)
					if err != nil || !slices.Equal(got, want) {
						t.Fatalf("recovered read: %s = %v, %v; want %v", g.name, got, err, want)
					}
					if !g.cached(store) {
						t.Fatalf("recovered read: %s did not cache the database vocabulary", g.name)
					}
					// A third call is served from the cache: any read would
					// hit sqlmock with no expectation left and fail.
					got, err = g.get(ctx, store)
					if err != nil || !slices.Equal(got, want) {
						t.Fatalf("cached read: %s = %v, %v; want %v", g.name, got, err, want)
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatalf("unmet SQL expectations: %v", err)
					}
				})
			}
		})
	}
}
