package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

func TestReopenCategoryInTx(t *testing.T) {
	t.Run("literal closed is done without configuration", func(t *testing.T) {
		category, err := ReopenCategoryInTx(context.Background(), nil, types.StatusClosed)
		if err != nil {
			t.Fatalf("ReopenCategoryInTx: %v", err)
		}
		if category != types.CategoryDone {
			t.Fatalf("category = %q, want %q", category, types.CategoryDone)
		}
	})

	t.Run("normalized custom done status", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).
				AddRow("archived", string(types.CategoryDone)))
		mock.ExpectRollback()

		category, err := ReopenCategoryInTx(context.Background(), tx, "archived")
		if err != nil {
			t.Fatalf("ReopenCategoryInTx: %v", err)
		}
		if category != types.CategoryDone {
			t.Fatalf("category = %q, want %q", category, types.CategoryDone)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("legacy config fallback", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
			WillReturnError(errors.New("Error 1146 (42S02): Table 'beads.custom_statuses' doesn't exist"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
			WithArgs("status.custom").
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("archived:done"))
		mock.ExpectRollback()

		category, err := ReopenCategoryInTx(context.Background(), tx, "archived")
		if err != nil {
			t.Fatalf("ReopenCategoryInTx: %v", err)
		}
		if category != types.CategoryDone {
			t.Fatalf("category = %q, want %q", category, types.CategoryDone)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("normalized query failure does not fall back to legacy config", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()

		_, err := ReopenCategoryInTx(context.Background(), tx, "archived")
		if err == nil || !regexp.MustCompile("connection reset").MatchString(err.Error()) {
			t.Fatalf("err = %v, want normalized query failure", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("normalized row scan failure does not fall back to legacy config", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("archived", nil))
		mock.ExpectRollback()

		_, err := ReopenCategoryInTx(context.Background(), tx, "archived")
		if err == nil || !regexp.MustCompile("scan custom_statuses").MatchString(err.Error()) {
			t.Fatalf("err = %v, want normalized scan failure", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("malformed legacy config is unspecified", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
			WithArgs("status.custom").
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("archived:not-a-category"))
		mock.ExpectRollback()

		category, err := ReopenCategoryInTx(context.Background(), tx, "archived")
		if err != nil {
			t.Fatalf("ReopenCategoryInTx: %v", err)
		}
		if category != types.CategoryUnspecified {
			t.Fatalf("category = %q, want %q", category, types.CategoryUnspecified)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("configuration query failure is returned", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
			WithArgs("status.custom").
			WillReturnError(errors.New("configuration unavailable"))
		mock.ExpectRollback()

		_, err := ReopenCategoryInTx(context.Background(), tx, "archived")
		if err == nil || !regexp.MustCompile("configuration unavailable").MatchString(err.Error()) {
			t.Fatalf("err = %v, want configuration query failure", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

func TestParseStatusFallback(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []types.CustomStatus
	}{
		{
			name:  "simple names get CategoryUnspecified",
			input: []string{"review", "testing"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryUnspecified},
				{Name: "testing", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "category format parsed correctly",
			input: []string{"review:active", "testing:wip"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "testing", Category: types.CategoryWIP},
			},
		},
		{
			name:  "mixed format",
			input: []string{"review:active", "legacy"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "legacy", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "empty entries filtered",
			input: []string{"", "review", ""},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "empty list",
			input: []string{},
			want:  nil,
		},
		{
			name:  "nil input",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStatusFallback(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statuses, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Category != tt.want[i].Category {
					t.Errorf("status[%d] = {%q, %q}, want {%q, %q}",
						i, g.Name, g.Category, tt.want[i].Name, tt.want[i].Category)
				}
			}
		})
	}
}

func TestParseCommaSeparatedList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty string", "", nil},
		{"single value", "foo", []string{"foo"}},
		{"multiple values", "foo,bar,baz", []string{"foo", "bar", "baz"}},
		{"whitespace trimmed", " foo , bar , baz ", []string{"foo", "bar", "baz"}},
		{"empty entries filtered", "foo,,bar,,", []string{"foo", "bar"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommaSeparatedList(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestMergeWithYAMLCustomTypes pins the gastownhall/beads#4024 overlay
// semantics: project-extension types declared in .beads/config.yaml must
// union-add to whatever the database side already declared, with duplicates
// removed and order preserved (DB types first, then YAML-only types).
func TestMergeWithYAMLCustomTypes(t *testing.T) {
	tests := []struct {
		name      string
		dbTypes   []string
		yamlTypes []string
		want      []string
	}{
		{
			name:      "both empty returns nil",
			dbTypes:   nil,
			yamlTypes: nil,
			want:      nil,
		},
		{
			name:      "yaml only is returned alone",
			dbTypes:   nil,
			yamlTypes: []string{"step", "wisp"},
			want:      []string{"step", "wisp"},
		},
		{
			name:      "db only is returned alone (no yaml overlay)",
			dbTypes:   []string{"convoy", "gate"},
			yamlTypes: nil,
			want:      []string{"convoy", "gate"},
		},
		{
			name:      "db plus disjoint yaml is union-added",
			dbTypes:   []string{"convoy", "gate"},
			yamlTypes: []string{"step", "wisp"},
			want:      []string{"convoy", "gate", "step", "wisp"},
		},
		{
			name:      "yaml duplicate of db is deduped (db wins position)",
			dbTypes:   []string{"convoy", "gate"},
			yamlTypes: []string{"gate", "step"},
			want:      []string{"convoy", "gate", "step"},
		},
		{
			name:      "yaml internal duplicate deduped when db empty",
			dbTypes:   nil,
			yamlTypes: []string{"step", "step", "wisp"},
			want:      []string{"step", "wisp"},
		},
		{
			name:      "whitespace entries dropped",
			dbTypes:   []string{"convoy", "  ", "gate"},
			yamlTypes: []string{"step", "", " wisp "},
			want:      []string{"convoy", "gate", "step", "wisp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeWithYAMLCustomTypes(tt.dbTypes, func() []string { return tt.yamlTypes })
			if len(got) != len(tt.want) {
				t.Fatalf("got %v (len %d), want %v (len %d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestMergeWithYAMLCustomTypes_NilGetter verifies the nil-getter guard:
// callers that pass a nil yamlGetter (e.g. when YAML is intentionally
// disabled) must get the dbTypes unchanged.
func TestMergeWithYAMLCustomTypes_NilGetter(t *testing.T) {
	got := mergeWithYAMLCustomTypes([]string{"convoy"}, nil)
	if len(got) != 1 || got[0] != "convoy" {
		t.Fatalf("got %v, want [convoy]", got)
	}
}

// TestCustomTypesYAMLFallback pins the YAML-fallback decision in
// ResolveCustomTypesInTx. When the in-tx config-string read errors, we mirror
// ResolveCustomStatusesDetailedInTx: degraded DB + populated YAML must return
// the YAML overlay with a nil error so callers (NewBatchContext,
// UpdateIssueInTx) don't treat the YAML-fallback path as a fatal validation
// failure. When YAML supplies nothing, the original error propagates.
func TestCustomTypesYAMLFallback(t *testing.T) {
	sentinelErr := errSentinel("boom")

	tests := []struct {
		name      string
		yamlTypes []string
		wantTypes []string
		wantErr   bool
	}{
		{
			name:      "yaml supplies types -> return yaml with nil error",
			yamlTypes: []string{"step", "wisp"},
			wantTypes: []string{"step", "wisp"},
			wantErr:   false,
		},
		{
			name:      "yaml empty -> propagate db error",
			yamlTypes: nil,
			wantTypes: nil,
			wantErr:   true,
		},
		{
			name:      "yaml all-whitespace -> propagate db error",
			yamlTypes: []string{"  ", ""},
			wantTypes: nil,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := customTypesYAMLFallback(func() []string { return tt.yamlTypes }, sentinelErr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got nil error, want %v", sentinelErr)
				}
			} else {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
			}
			if len(got) != len(tt.wantTypes) {
				t.Fatalf("got %v, want %v", got, tt.wantTypes)
			}
			for i := range got {
				if got[i] != tt.wantTypes[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.wantTypes[i])
				}
			}
		})
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// withCustomVocabularyYAML isolates the process config with a throwaway
// .beads/config.yaml declaring status.custom and/or types.custom, with HOME
// and XDG_CONFIG_HOME redirected so no user-level config can leak in. The
// config package is process-global, so these tests never run in parallel.
func withCustomVocabularyYAML(t *testing.T, statusCSV, typesCSV string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	var b strings.Builder
	if statusCSV != "" {
		fmt.Fprintf(&b, "status:\n  custom: %q\n", statusCSV)
	}
	if typesCSV != "" {
		fmt.Fprintf(&b, "types:\n  custom: %q\n", typesCSV)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(dir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

const (
	combinedStatusesQuery = "SELECT name, category FROM custom_statuses ORDER BY name"
	combinedTypesQuery    = "SELECT name FROM custom_types ORDER BY name"
	combinedConfigQuery   = "SELECT `key`, value FROM config WHERE `key` IN (?,?)"
)

// resolveCombined runs ResolveCustomConfigInTx over the mocked transaction,
// rolling back and asserting every queued SQL expectation was met.
func resolveCombined(t *testing.T, tx *sql.Tx) (statuses []types.CustomStatus, customTypes []string, err error) {
	t.Helper()
	statuses, customTypes, err = ResolveCustomConfigInTx(context.Background(), tx)
	rollbackErr := tx.Rollback()
	if rollbackErr != nil {
		t.Errorf("rollback: %v", rollbackErr)
	}
	return statuses, customTypes, err
}

// TestResolveCustomConfigInTxPropagatesReadErrors pins the resolver half of
// bd-2e1byb (GH#5581): a real read failure must surface as an error the store
// can distinguish from a cacheable success, instead of composing into a
// degraded-but-successful answer. Genuinely absent pre-migration tables and
// empty tables keep the legacy config-row and YAML fallbacks.
func TestResolveCustomConfigInTxPropagatesReadErrors(t *testing.T) {
	expectationsMet := func(t *testing.T, mock sqlmock.Sqlmock) {
		t.Helper()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	}

	requireResolveCause := func(t *testing.T, statuses []types.CustomStatus, customTypes []string, err error, cause error) {
		t.Helper()
		if !errors.Is(err, cause) {
			t.Fatalf("err = %v, want the injected read failure", err)
		}
		if statuses != nil || customTypes != nil {
			t.Fatalf("partial result escaped a failed resolve: statuses=%v customTypes=%v", statuses, customTypes)
		}
	}

	t.Run("status table query failure returns the cause", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "")
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		cause := errors.New("vocabulary read failed")
		mock.ExpectQuery(regexp.QuoteMeta(combinedStatusesQuery)).WillReturnError(cause)
		mock.ExpectRollback()

		statuses, customTypes, err := resolveCombined(t, tx)
		requireResolveCause(t, statuses, customTypes, err, cause)
		expectationsMet(t, mock)
	})

	t.Run("status table query failure with yaml still returns the cause", func(t *testing.T) {
		withCustomVocabularyYAML(t, "review:active", "spike")
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		cause := errors.New("vocabulary read failed")
		mock.ExpectQuery(regexp.QuoteMeta(combinedStatusesQuery)).WillReturnError(cause)
		mock.ExpectRollback()

		statuses, customTypes, err := resolveCombined(t, tx)
		requireResolveCause(t, statuses, customTypes, err, cause)
		expectationsMet(t, mock)
	})

	t.Run("types table query failure after successful statuses returns the cause", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "")
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		cause := errors.New("vocabulary read failed")
		mock.ExpectQuery(regexp.QuoteMeta(combinedStatusesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "active"))
		mock.ExpectQuery(regexp.QuoteMeta(combinedTypesQuery)).WillReturnError(cause)
		mock.ExpectRollback()

		statuses, customTypes, err := resolveCombined(t, tx)
		requireResolveCause(t, statuses, customTypes, err, cause)
		expectationsMet(t, mock)
	})

	t.Run("config row read failure after empty tables returns the cause", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "")
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		cause := errors.New("config read failed")
		mock.ExpectQuery(regexp.QuoteMeta(combinedStatusesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
		mock.ExpectQuery(regexp.QuoteMeta(combinedTypesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}))
		mock.ExpectQuery(regexp.QuoteMeta(combinedConfigQuery)).
			WithArgs("status.custom", "types.custom").WillReturnError(cause)
		mock.ExpectRollback()

		statuses, customTypes, err := resolveCombined(t, tx)
		requireResolveCause(t, statuses, customTypes, err, cause)
		expectationsMet(t, mock)
	})

	t.Run("config row read failure with yaml still returns the cause", func(t *testing.T) {
		withCustomVocabularyYAML(t, "review:active", "spike")
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		cause := errors.New("config read failed")
		mock.ExpectQuery(regexp.QuoteMeta(combinedStatusesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
		mock.ExpectQuery(regexp.QuoteMeta(combinedTypesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}))
		mock.ExpectQuery(regexp.QuoteMeta(combinedConfigQuery)).
			WithArgs("status.custom", "types.custom").WillReturnError(cause)
		mock.ExpectRollback()

		statuses, customTypes, err := resolveCombined(t, tx)
		requireResolveCause(t, statuses, customTypes, err, cause)
		expectationsMet(t, mock)
	})

	t.Run("status row iteration failure returns the cause", func(t *testing.T) {
		withCustomVocabularyYAML(t, "", "")
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		cause := errors.New("row iteration failed")
		mock.ExpectQuery(regexp.QuoteMeta(combinedStatusesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "active").RowError(0, cause))
		mock.ExpectRollback()

		statuses, customTypes, err := resolveCombined(t, tx)
		requireResolveCause(t, statuses, customTypes, err, cause)
		expectationsMet(t, mock)
	})
}

// Missing pre-migration tables and empty normalized tables still fall back to
// the legacy config rows and then config.yaml, with status categories and the
// custom-type composition (database layer unioned with config.yaml) intact.
func TestResolveCustomConfigInTxPreservesFallbacks(t *testing.T) {
	statusesMissing := errors.New("Error 1146 (42S02): Table 'beads.custom_statuses' doesn't exist")
	typesMissing := errors.New("Error 1146 (42S02): table not found: custom_types")
	cases := []struct {
		name         string
		yaml         string
		expect       func(mock sqlmock.Sqlmock)
		wantStatuses []types.CustomStatus
		wantTypes    []string
	}{
		{
			name: "missing tables use legacy config rows",
			yaml: "types:\n  custom: \"extra\"\n",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(customStatusesQuery)).WillReturnError(statusesMissing)
				mock.ExpectQuery(regexp.QuoteMeta(customTypesQuery)).WillReturnError(typesMissing)
				mock.ExpectQuery(regexp.QuoteMeta(customConfigQuery)).
					WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
						AddRow("status.custom", "review:active,parked:frozen").
						AddRow("types.custom", "spike,research"))
			},
			wantStatuses: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "parked", Category: types.CategoryFrozen},
			},
			wantTypes: []string{"spike", "research", "extra"},
		},
		{
			name: "empty tables and no config rows use config.yaml",
			yaml: "status:\n  custom: \"triage:wip\"\ntypes:\n  custom: \"extra, extra\"\n",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(customStatusesQuery)).WillReturnRows(statusRows())
				mock.ExpectQuery(regexp.QuoteMeta(customTypesQuery)).WillReturnRows(typeRows())
				mock.ExpectQuery(regexp.QuoteMeta(customConfigQuery)).
					WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
			},
			wantStatuses: []types.CustomStatus{{Name: "triage", Category: types.CategoryWIP}},
			wantTypes:    []string{"extra"},
		},
		{
			name: "nothing configured anywhere",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(customStatusesQuery)).WillReturnRows(statusRows())
				mock.ExpectQuery(regexp.QuoteMeta(customTypesQuery)).WillReturnRows(typeRows())
				mock.ExpectQuery(regexp.QuoteMeta(customConfigQuery)).
					WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
			},
		},
		{
			name: "populated tables union config.yaml types",
			yaml: "status:\n  custom: \"ignored:done\"\ntypes:\n  custom: \"extra,spike\"\n",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(customStatusesQuery)).
					WillReturnRows(statusRows([2]string{"archived", "done"}, [2]string{"review", "active"}))
				mock.ExpectQuery(regexp.QuoteMeta(customTypesQuery)).WillReturnRows(typeRows("spike"))
			},
			wantStatuses: []types.CustomStatus{
				{Name: "archived", Category: types.CategoryDone},
				{Name: "review", Category: types.CategoryActive},
			},
			wantTypes: []string{"spike", "extra"},
		},
		{
			name: "status table resolved, missing type table uses its config row",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(customStatusesQuery)).
					WillReturnRows(statusRows([2]string{"review", "active"}))
				mock.ExpectQuery(regexp.QuoteMeta(customTypesQuery)).WillReturnError(typesMissing)
				mock.ExpectQuery(regexp.QuoteMeta(customConfigQuery)).
					WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
						AddRow("status.custom", "ignored:done").
						AddRow("types.custom", "legacy"))
			},
			wantStatuses: []types.CustomStatus{{Name: "review", Category: types.CategoryActive}},
			wantTypes:    []string{"legacy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withYAMLCustomConfig(t, tc.yaml)
			_, mock, tx := beginMockTx(t)
			tc.expect(mock)

			statuses, customTypes, err := ResolveCustomConfigInTx(context.Background(), tx)
			if err != nil {
				t.Fatalf("ResolveCustomConfigInTx: %v", err)
			}
			if !reflect.DeepEqual(statuses, tc.wantStatuses) {
				t.Fatalf("statuses = %v, want %v", statuses, tc.wantStatuses)
			}
			if !reflect.DeepEqual(customTypes, tc.wantTypes) {
				t.Fatalf("types = %v, want %v", customTypes, tc.wantTypes)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}
