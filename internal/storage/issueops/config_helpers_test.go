package issueops

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

// TestResolveCustomConfigInTxReturnsReadErrors pins GH#5581's resolver half:
// a read error on any vocabulary query must surface as an error preserving
// the injected cause, not degrade into a successful (often empty) resolution
// that the store-side cache would then pin as loaded. Suppression is
// reserved for missing normalized tables (1146), which the fallback test
// below covers.
func TestResolveCustomConfigInTxReturnsReadErrors(t *testing.T) {
	statusesQuery := regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")
	typesQuery := regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")
	configQuery := regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")
	const cause = errSentinel("vocabulary read failed")

	cases := []struct {
		name string
		arm  func(mock sqlmock.Sqlmock)
	}{
		{
			name: "statuses select fails",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnError(cause)
			},
		},
		{
			name: "types select fails after statuses read",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).
					WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip"))
				mock.ExpectQuery(typesQuery).WillReturnError(cause)
			},
		},
		{
			name: "legacy config select fails after empty normalized tables",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
				mock.ExpectQuery(typesQuery).WillReturnRows(sqlmock.NewRows([]string{"name"}))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").WillReturnError(cause)
			},
		},
		{
			// The deliberate degraded-result change: statuses resolved from
			// the table, the types table empty, and the legacy config read
			// failing used to return the partial database result with a nil
			// error. The partial result is now discarded along with the error
			// so an incomplete resolution cannot be certified as loaded.
			name: "legacy config select fails after partial normalized success",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).
					WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip"))
				mock.ExpectQuery(typesQuery).WillReturnRows(sqlmock.NewRows([]string{"name"}))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").WillReturnError(cause)
			},
		},
		{
			name: "statuses rows fail mid-iteration",
			arm: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip")
				rows.RowError(0, cause)
				mock.ExpectQuery(statusesQuery).WillReturnRows(rows)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			tc.arm(mock)
			mock.ExpectRollback()

			statuses, customTypes, err := ResolveCustomConfigInTx(context.Background(), tx)
			if err == nil || !errors.Is(err, cause) {
				t.Fatalf("ResolveCustomConfigInTx err = %v, want the injected read failure", err)
			}
			if statuses != nil || customTypes != nil {
				t.Errorf("ResolveCustomConfigInTx = (%v, %v) alongside the error, want nil results", statuses, customTypes)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestResolveCustomConfigInTxFallsBackOnMissingTables pins the compatibility
// half of GH#5581's resolver change: suppressing query errors is reserved
// for missing normalized tables (MySQL/Dolt 1146), so a pre-migration
// database — and one whose migration created the tables but populated
// neither — still resolves its vocabulary from the legacy config rows, with
// the config.yaml type union intact and per-row scan leniency unchanged.
func TestResolveCustomConfigInTxFallsBackOnMissingTables(t *testing.T) {
	statusesQuery := regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")
	typesQuery := regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")
	configQuery := regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")
	tableMissing := func(table string) error {
		return errors.New("Error 1146 (42S02): Table 'beads." + table + "' doesn't exist")
	}
	reviewWIPRow := sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip")
	legacyRows := func(keys ...string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"key", "value"})
		for _, k := range keys {
			switch k {
			case "status.custom":
				rows.AddRow("status.custom", "review:wip")
			case "types.custom":
				rows.AddRow("types.custom", "molecule")
			}
		}
		return rows
	}

	cases := []struct {
		name     string
		yaml     bool
		arm      func(mock sqlmock.Sqlmock)
		wantType []string
	}{
		{
			name: "both normalized tables missing",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnError(tableMissing("custom_statuses"))
				mock.ExpectQuery(typesQuery).WillReturnError(tableMissing("custom_types"))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").
					WillReturnRows(legacyRows("status.custom", "types.custom"))
			},
			wantType: []string{"molecule"},
		},
		{
			name: "statuses missing, types populated",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnError(tableMissing("custom_statuses"))
				mock.ExpectQuery(typesQuery).
					WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("molecule"))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").
					WillReturnRows(legacyRows("status.custom"))
			},
			wantType: []string{"molecule"},
		},
		{
			name: "types missing, statuses populated",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnRows(reviewWIPRow)
				mock.ExpectQuery(typesQuery).WillReturnError(tableMissing("custom_types"))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").
					WillReturnRows(legacyRows("types.custom"))
			},
			wantType: []string{"molecule"},
		},
		{
			// The migration may create the tables without populating them;
			// empty tables resolve from the legacy rows exactly like missing
			// ones.
			name: "both tables present but empty",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
				mock.ExpectQuery(typesQuery).WillReturnRows(sqlmock.NewRows([]string{"name"}))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").
					WillReturnRows(legacyRows("status.custom", "types.custom"))
			},
			wantType: []string{"molecule"},
		},
		{
			// The config.yaml type overlay still union-adds on top of the
			// legacy fallback (gastownhall/beads#4024 semantics).
			name: "yaml type union over legacy fallback",
			yaml: true,
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).WillReturnError(tableMissing("custom_statuses"))
				mock.ExpectQuery(typesQuery).WillReturnError(tableMissing("custom_types"))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").
					WillReturnRows(legacyRows("status.custom", "types.custom"))
			},
			wantType: []string{"molecule", "gizmo"},
		},
		{
			// Per-row scan failures are still skipped leniently: a bad row
			// does not fail the read, and the legacy row supplies the status.
			name: "unscannable status row falls back leniently",
			arm: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusesQuery).
					WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", nil))
				mock.ExpectQuery(typesQuery).
					WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("molecule"))
				mock.ExpectQuery(configQuery).WithArgs("status.custom", "types.custom").
					WillReturnRows(legacyRows("status.custom"))
			},
			wantType: []string{"molecule"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.yaml {
				withYAMLCustomTypes(t, "gizmo")
			}
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			tc.arm(mock)
			mock.ExpectRollback()

			statuses, customTypes, err := ResolveCustomConfigInTx(context.Background(), tx)
			if err != nil {
				t.Fatalf("ResolveCustomConfigInTx: %v", err)
			}
			if len(statuses) != 1 || statuses[0].Name != "review" || statuses[0].Category != types.CategoryWIP {
				t.Errorf("statuses = %v, want [review/wip] from the legacy config row", statuses)
			}
			if len(customTypes) != len(tc.wantType) {
				t.Fatalf("custom types = %v, want %v", customTypes, tc.wantType)
			}
			for i, want := range tc.wantType {
				if customTypes[i] != want {
					t.Errorf("custom types = %v, want %v", customTypes, tc.wantType)
					break
				}
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
