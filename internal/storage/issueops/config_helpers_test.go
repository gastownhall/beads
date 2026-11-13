package issueops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// snapshotQuery matches the one keyed read that fills a config snapshot of n keys.
func snapshotQuery(n int) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", n), ",")
	return regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (" + placeholders + ")")
}

// configRows mocks that read, returning the rows given.
func configRows(mock sqlmock.Sqlmock, keys int, rows map[string]string) {
	result := sqlmock.NewRows([]string{"key", "value"})
	for key, value := range rows {
		result.AddRow(key, value)
	}
	mock.ExpectQuery(snapshotQuery(keys)).WillReturnRows(result)
}

// TestConfigSnapshotAnswersEveryReaderFromOneRead pins what a create's write
// transaction pays for its settings: one statement however many keys it asks
// about, with each reader's answer unchanged. The mock is primed with the one
// read, so a reader that still queried its own key fails on an unexpected query
// (or, for a reader that swallows errors, on its answer).
func TestConfigSnapshotAnswersEveryReaderFromOneRead(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	configRows(mock, len(createConfigKeys), map[string]string{
		"issue_prefix":       "gt-",
		"allowed_prefixes":   "gt,hq",
		"issue_id_mode":      "counter",
		"types.infra":        "agent",
		"max_collision_prob": "0.5",
		"min_hash_length":    "4",
		"max_hash_length":    "6",
	})
	mock.ExpectRollback()
	ctx := withConfigSnapshot(context.Background(), createConfigKeys...)

	prefix, err := ReadConfigPrefix(ctx, tx)
	if err != nil || prefix != "gt" {
		t.Fatalf("ReadConfigPrefix = %q, %v; want gt", prefix, err)
	}
	if allowed, err := GetConfigInTx(ctx, tx, "allowed_prefixes"); err != nil || allowed != "gt,hq" {
		t.Fatalf("GetConfigInTx(allowed_prefixes) = %q, %v", allowed, err)
	}
	if counter, err := IsCounterModeTx(ctx, tx); err != nil || !counter {
		t.Fatalf("IsCounterModeTx = %v, %v; want true", counter, err)
	}
	if infra := ResolveInfraTypesInTx(ctx, tx); !infra["agent"] || len(infra) != 1 {
		t.Fatalf("ResolveInfraTypesInTx = %v, want the configured agent type alone", infra)
	}
	got := GetAdaptiveConfigTx(ctx, tx)
	if want := (AdaptiveIDConfig{MaxCollisionProbability: 0.5, MinLength: 4, MaxLength: 6}); got != want {
		t.Fatalf("GetAdaptiveConfigTx = %+v, want %+v", got, want)
	}
	if absent, err := GetConfigInTx(ctx, tx, "status.custom"); err != nil || absent != "" {
		t.Fatalf("GetConfigInTx of a snapshot key with no row = %q, %v; want empty and no error", absent, err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestConfigSnapshotLeavesOtherKeysToTheirOwnRead: only the named keys are
// loaded, so the config table's kv.* rows never leave the database for a create.
func TestConfigSnapshotLeavesOtherKeysToTheirOwnRead(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	configRows(mock, 1, map[string]string{"a": "1"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).WithArgs("other").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("2"))
	mock.ExpectRollback()
	ctx := withConfigSnapshot(context.Background(), "a")

	if got, err := GetConfigInTx(ctx, tx, "a"); err != nil || got != "1" {
		t.Fatalf("GetConfigInTx(a) = %q, %v", got, err)
	}
	if got, err := GetConfigInTx(ctx, tx, "other"); err != nil || got != "2" {
		t.Fatalf("GetConfigInTx(other) = %q, %v", got, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestGetConfigKeysKeepsTheSpellingAskedFor: on a case-insensitive collation the
// IN query returns a row spelled differently from the key asked for, and a read of
// that one key would have found it. Callers look rows up by the key they asked
// for, so that is the spelling the row must come back under.
func TestGetConfigKeysKeepsTheSpellingAskedFor(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	mock.ExpectQuery(snapshotQuery(2)).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
		AddRow("ISSUE_PREFIX", "gt").AddRow("issue_id_mode", "counter"))
	mock.ExpectRollback()

	got, err := GetConfigKeysInTx(context.Background(), tx, "issue_prefix", "issue_id_mode")
	if err != nil {
		t.Fatal(err)
	}
	if got["issue_prefix"] != "gt" || got["issue_id_mode"] != "counter" || len(got) != 2 {
		t.Fatalf("GetConfigKeysInTx = %v", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

// TestExecuteCreateReadsItsSettingsOnce drives the real create up to the first
// statement past its settings, on both ID paths. Every config lookup on the way
// has to come from the one read: a lookup that queried its own key would hit an
// expectation for something else and end the create with the wrong error.
func TestExecuteCreateReadsItsSettingsOnce(t *testing.T) {
	stop := errors.New("stop after the settings")
	settings := map[string]string{"issue_prefix": "gt", "allowed_prefixes": "gt,hq"}
	for _, tc := range []struct {
		name     string
		settings map[string]string
		next     func(sqlmock.Sqlmock)
	}{
		{
			name:     "hash ids",
			settings: settings,
			next: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`SELECT COUNT\(\*\)\s+FROM issues\s+WHERE id LIKE`).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
				mock.ExpectQuery(`SELECT \(SELECT COUNT\(\*\) FROM issues WHERE id = \?\)`).WillReturnError(stop)
			},
		},
		{
			name:     "counter ids",
			settings: map[string]string{"issue_prefix": "gt", "issue_id_mode": "counter"},
			next: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = last_id + 1")).WillReturnError(stop)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
				WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
			configRows(mock, len(createConfigKeys), tc.settings)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")).
				WillReturnRows(sqlmock.NewRows([]string{"name"}))
			tc.next(mock)

			_, _, err := ExecuteCreate(context.Background(), tx, publicops.CreateRequest{
				Actor: "tester",
				Issue: &types.Issue{Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			})
			if !errors.Is(err, stop) {
				t.Fatalf("ExecuteCreate = %v, want it to reach the statement past its settings", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestConfigSnapshotMissingPrefixStillReportsNotInitialized keeps the refusal a
// create gives a database with no issue_prefix when it reads through a snapshot.
func TestConfigSnapshotMissingPrefixStillReportsNotInitialized(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	configRows(mock, len(createConfigKeys), map[string]string{"allowed_prefixes": "gt"})
	mock.ExpectRollback()

	_, err := ReadConfigPrefix(withConfigSnapshot(context.Background(), createConfigKeys...), tx)
	if !errors.Is(err, storage.ErrNotInitialized) {
		t.Fatalf("ReadConfigPrefix = %v, want ErrNotInitialized", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

// TestConfigSnapshotDropsAfterAConfigWrite: a config write in the scope makes
// the next read see it, as it did when every read went to the transaction.
func TestConfigSnapshotDropsAfterAConfigWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expect string
		write  func(context.Context, *sql.Tx) error
		want   string
	}{
		{
			name:   "set",
			expect: "REPLACE INTO config",
			write:  func(ctx context.Context, tx *sql.Tx) error { return SetConfigInTx(ctx, tx, "k", "new") },
			want:   "new",
		},
		{
			name:   "delete",
			expect: "DELETE FROM config",
			write:  func(ctx context.Context, tx *sql.Tx) error { return DeleteConfigInTx(ctx, tx, "k") },
			want:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			configRows(mock, 1, map[string]string{"k": "old"})
			mock.ExpectExec(tc.expect).WillReturnResult(sqlmock.NewResult(0, 1))
			after := map[string]string{}
			if tc.want != "" {
				after["k"] = tc.want
			}
			configRows(mock, 1, after)
			mock.ExpectRollback()
			ctx := withConfigSnapshot(context.Background(), "k")

			if got, err := GetConfigInTx(ctx, tx, "k"); err != nil || got != "old" {
				t.Fatalf("read before the write = %q, %v", got, err)
			}
			if err := tc.write(ctx, tx); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got, err := GetConfigInTx(ctx, tx, "k"); err != nil || got != tc.want {
				t.Fatalf("read after the write = %q, %v; want %q", got, err, tc.want)
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

// TestConfigSnapshotFallsBackWhenItsReadFails: a failed keyed read leaves each
// key's own read, and its error, exactly as they were without a snapshot.
func TestConfigSnapshotFallsBackWhenItsReadFails(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	mock.ExpectQuery(snapshotQuery(2)).WillReturnError(errors.New("snapshot read failed"))
	keyRead := regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")
	mock.ExpectQuery(keyRead).WithArgs("a").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("1"))
	wantErr := errors.New("key read failed")
	mock.ExpectQuery(keyRead).WithArgs("b").WillReturnError(wantErr)
	mock.ExpectRollback()
	ctx := withConfigSnapshot(context.Background(), "a", "b")

	if got, err := GetConfigInTx(ctx, tx, "a"); err != nil || got != "1" {
		t.Fatalf("GetConfigInTx(a) = %q, %v", got, err)
	}
	if _, err := GetConfigInTx(ctx, tx, "b"); !errors.Is(err, wantErr) {
		t.Fatalf("GetConfigInTx(b) = %v, want the key read's error", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestConfigReadersWithoutASnapshotReadTheirOwnKey: only a context
// withConfigSnapshot built shares a read.
func TestConfigReadersWithoutASnapshotReadTheirOwnKey(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	keyRead := regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")
	mock.ExpectQuery(keyRead).WithArgs("a").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("1"))
	mock.ExpectQuery(keyRead).WithArgs("a").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("2"))
	mock.ExpectRollback()

	for _, want := range []string{"1", "2"} {
		if got, err := GetConfigInTx(context.Background(), tx, "a"); err != nil || got != want {
			t.Fatalf("GetConfigInTx = %q, %v; want %q", got, err, want)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

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
