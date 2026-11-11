package dolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
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
				"GetCustomTypes":            {"db-type"},
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
			// types are the database set unioned with config.yaml's, so the
			// database-only db-type proves the database was read again.
			after: map[string][]string{
				"GetCustomStatuses":         {"review"},
				"GetCustomStatusesDetailed": {"review:active"},
				"GetCustomTypes":            {"db-type", "spike", "research"},
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
					expectCustomConfigRead(mock, [][2]string{{"review", "active"}}, []string{"db-type"})

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

// A successful read, including one that finds no custom vocabulary at all, is
// cached until SetConfig invalidates the corresponding key; a failed reload of
// one invalidated half leaves the other cached half intact.
func TestCustomConfigGettersCacheSuccessfulResolve(t *testing.T) {
	withCustomConfigYAML(t, "")

	t.Run("unconfigured vocabulary is cached", func(t *testing.T) {
		for _, g := range customConfigGetters {
			t.Run(g.name, func(t *testing.T) {
				store, mock := newCustomConfigMockStore(t)
				ctx := context.Background()
				expectCustomConfigRead(mock, nil, nil)
				for i := 0; i < 2; i++ {
					got, err := g.get(ctx, store)
					if err != nil || got != nil {
						t.Fatalf("call %d: %s = %v, %v; want no vocabulary and nil error", i+1, g.name, got, err)
					}
				}
				if !store.customStatusCached || !store.customTypeCached {
					t.Fatalf("successful empty read not cached: status=%v type=%v", store.customStatusCached, store.customTypeCached)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet SQL expectations: %v", err)
				}
			})
		}
	})

	t.Run("invalidation rereads only the invalidated half", func(t *testing.T) {
		store, mock := newCustomConfigMockStore(t)
		ctx := context.Background()
		expectCustomConfigRead(mock, [][2]string{{"review", "active"}}, []string{"spike"})
		expectCustomConfigRead(mock, [][2]string{{"triage", "wip"}}, []string{"research"})

		if got, err := store.GetCustomTypes(ctx); err != nil || !slices.Equal(got, []string{"spike"}) {
			t.Fatalf("GetCustomTypes = %v, %v; want [spike]", got, err)
		}
		store.invalidateConfigCaches("types.custom")
		if got, err := store.GetCustomTypes(ctx); err != nil || !slices.Equal(got, []string{"research"}) {
			t.Fatalf("GetCustomTypes after invalidation = %v, %v; want [research]", got, err)
		}
		// The status half was never invalidated, so it keeps the first read.
		if got, err := store.GetCustomStatuses(ctx); err != nil || !slices.Equal(got, []string{"review"}) {
			t.Fatalf("GetCustomStatuses = %v, %v; want the cached [review]", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("failed reload keeps the other cached half", func(t *testing.T) {
		store, mock := newCustomConfigMockStore(t)
		ctx := context.Background()
		injected := errors.New("simulated read failure")
		expectCustomConfigRead(mock, [][2]string{{"review", "active"}}, []string{"spike"})
		mock.ExpectBegin().WillReturnError(injected)

		if _, err := store.GetCustomStatuses(ctx); err != nil {
			t.Fatalf("GetCustomStatuses: %v", err)
		}
		store.invalidateConfigCaches("types.custom")
		if got, err := store.GetCustomTypes(ctx); !errors.Is(err, injected) || got != nil {
			t.Fatalf("GetCustomTypes after failed reload = %v, %v; want an error wrapping %v", got, err, injected)
		}
		if store.customTypeCached {
			t.Fatal("failed reload cached the type half")
		}
		got, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil || len(got) != 1 || got[0].Name != "review" || got[0].Category != "active" {
			t.Fatalf("GetCustomStatusesDetailed = %v, %v; want the cached [review:active]", got, err)
		}

		// The next healthy read serves the database's current types.
		expectCustomConfigRead(mock, [][2]string{{"review", "active"}}, []string{"db-type-2"})
		if got, err := store.GetCustomTypes(ctx); err != nil || !slices.Equal(got, []string{"db-type-2"}) {
			t.Fatalf("GetCustomTypes after recovery = %v, %v; want [db-type-2]", got, err)
		}
		if got, err := store.GetCustomStatuses(ctx); err != nil || !slices.Equal(got, []string{"review"}) {
			t.Fatalf("GetCustomStatuses = %v, %v; want the cached [review]", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

// blockingConverter parks the first query argument it converts until
// release is closed, signalling arrived first, so a test can hold one
// read in flight while another runs.
type blockingConverter struct {
	once    sync.Once
	arrived chan struct{}
	release chan struct{}
}

func (c *blockingConverter) ConvertValue(v any) (driver.Value, error) {
	c.once.Do(func() {
		close(c.arrived)
		<-c.release
	})
	return driver.DefaultParameterConverter.ConvertValue(v)
}

// A failed read that finishes after a concurrent read has filled the cache
// must leave that cache alone: the failing caller gets its error, and the
// store keeps serving the successful vocabulary without reading again.
func TestCustomConfigFailedLoadKeepsConcurrentFill(t *testing.T) {
	withCustomConfigYAML(t, "")
	conv := &blockingConverter{arrived: make(chan struct{}), release: make(chan struct{})}
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp), sqlmock.ValueConverterOption(conv))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The two reads interleave, so match expectations by statement.
	mock.MatchExpectationsInOrder(false)
	store := &DoltStore{db: db}
	ctx := context.Background()
	injected := errors.New("simulated read failure")

	// The failing read finds both normalized tables empty, then parks on
	// the config-row query (the only one with arguments) until released,
	// and fails it.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")).
		WillReturnError(injected)
	mock.ExpectRollback()

	failed := make(chan error, 1)
	go func() {
		_, err := store.GetCustomTypes(ctx)
		failed <- err
	}()
	<-conv.arrived

	// The concurrent read succeeds and fills both halves while the failing
	// read is still in flight.
	expectCustomConfigRead(mock, [][2]string{{"review", "active"}}, []string{"db-type"})
	if got, err := store.GetCustomTypes(ctx); err != nil || !slices.Equal(got, []string{"db-type"}) {
		t.Fatalf("concurrent GetCustomTypes = %v, %v; want [db-type]", got, err)
	}

	close(conv.release)
	if err := <-failed; !errors.Is(err, injected) {
		t.Fatalf("failing GetCustomTypes err = %v, want it to wrap %v", err, injected)
	}

	// Both halves still hold the successful read, served without a read.
	if got, err := store.GetCustomTypes(ctx); err != nil || !slices.Equal(got, []string{"db-type"}) {
		t.Fatalf("GetCustomTypes after the failed read finished = %v, %v; want the cached [db-type]", got, err)
	}
	if got, err := store.GetCustomStatuses(ctx); err != nil || !slices.Equal(got, []string{"review"}) {
		t.Fatalf("GetCustomStatuses after the failed read finished = %v, %v; want the cached [review]", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
