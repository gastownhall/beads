package schema

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectOwnedPolicyCursor mocks the main-cursor read LoadOwnedIgnorePolicy
// does: the existence probe, then the version read when the cursor exists.
func expectOwnedPolicyCursor(mock sqlmock.Sqlmock, exists bool, version int) {
	expectCursorProbe(mock, "schema_migrations", exists)
	if exists {
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", version)
	}
}

func TestBeadsOwnedIgnorePolicy(t *testing.T) {
	t.Run("tables", func(t *testing.T) {
		policy := ownedIgnorePolicyAt(62)
		for table, want := range map[string]bool{
			"wisps":                     true,
			"wisp_dependencies":         true,
			"local_metadata":            true,
			"__temp__wisps":             true,
			"events":                    true,
			"issues":                    false,
			"config":                    false,
			"dolt_ignore":               false,
			"wispsx":                    false,
			"wisp":                      false,
			"__temp__wisps -> wisps":    true,
			"__temp__issues -> issues":  false,
			"issues -> wisps":           false,
			"wisps -> issues":           false,
			"local_metadataX -> wisps":  false,
			"bd_events_journal":         true,
			"bd_events_journal_archive": false,
		} {
			if got := policy.OwnsTable(table); got != want {
				t.Errorf("OwnsTable(%q) = %v, want %v", table, got, want)
			}
		}
	})

	t.Run("events gated on main cursor 62", func(t *testing.T) {
		if ownedIgnorePolicyAt(61).OwnsTable("events") {
			t.Error("events owned at main cursor 61, want only from 62")
		}
		if !ownedIgnorePolicyAt(62).OwnsPattern("events") {
			t.Error("events pattern not owned at main cursor 62")
		}
		if ownedIgnorePolicyAt(62).OwnsPattern("issues") {
			t.Error("operator pattern issues reported as owned")
		}
	})

	t.Run("pattern syntax", func(t *testing.T) {
		for _, tc := range []struct {
			pattern, table string
			want           bool
		}{
			{"wisp_%", "wisp_x", true},
			{"wisp_%", "wispAx", false}, // underscore is literal
			{"wisps", "wisps", true},
			{"wisps", "Wisps", false},
			{"%", "issues", false},
			{"wisp%s", "wispxs", false},
			{"wisp?", "wisps", false},
			{"wisp*", "wisps", false},
			{"wisp_%%", "wisp_x", false},
		} {
			if got := ownedPatternMatches(tc.pattern, tc.table); got != tc.want {
				t.Errorf("ownedPatternMatches(%q, %q) = %v, want %v", tc.pattern, tc.table, got, tc.want)
			}
		}
	})

	t.Run("loaded cursor gates the events pattern", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			exists      bool
			mainVersion int
			wantEvents  bool
		}{
			{name: "absent cursor is version 0", exists: false, wantEvents: false},
			{name: "pre-flip cursor", exists: true, mainVersion: 61, wantEvents: false},
			{name: "flipped cursor", exists: true, mainVersion: 62, wantEvents: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				expectOwnedPolicyCursor(mock, tc.exists, tc.mainVersion)
				policy, err := LoadOwnedIgnorePolicy(context.Background(), db)
				if err != nil {
					t.Fatalf("LoadOwnedIgnorePolicy: %v", err)
				}
				if got := policy.OwnsTable("events"); got != tc.wantEvents {
					t.Errorf("OwnsTable(events) = %v, want %v", got, tc.wantEvents)
				}
				// The canonical clone-local set is owned at every cursor.
				for _, table := range []string{"wisps", "wisp_dependencies", "local_metadata", "leases", "repo_mtimes", "ignored_schema_migrations", "bd_events_journal", "bd_events_seq"} {
					if !policy.OwnsTable(table) {
						t.Errorf("OwnsTable(%q) = false, want true", table)
					}
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet SQL expectations: %v", err)
				}
			})
		}
	})

	t.Run("canonical patterns use only supported syntax", func(t *testing.T) {
		// Adding a pattern shape the matcher does not interpret would make it
		// silently own nothing; pin every canonical pattern as matchable.
		policy := ownedIgnorePolicyAt(63)
		for _, pattern := range doltIgnorePatternsAt(63) {
			if !policy.OwnsPattern(pattern) {
				t.Errorf("OwnsPattern(%q) = false, want true", pattern)
			}
			if strings.ContainsAny(pattern, "?*") {
				t.Errorf("canonical pattern %q uses ? or * syntax the matcher does not support", pattern)
			}
			table := pattern
			if prefix, ok := strings.CutSuffix(pattern, "%"); ok {
				if strings.Contains(prefix, "%") {
					t.Errorf("canonical pattern %q has an embedded %% the matcher does not support", pattern)
				}
				table = prefix + "x"
			}
			if !policy.OwnsTable(table) {
				t.Errorf("canonical pattern %q does not own table %q", pattern, table)
			}
		}
	})

	t.Run("no current database is version 0", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery(`information_schema\.schemata`).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		policy, err := LoadOwnedIgnorePolicy(context.Background(), db)
		if err != nil {
			t.Fatalf("LoadOwnedIgnorePolicy: %v", err)
		}
		if policy.OwnsTable("events") || !policy.OwnsTable("wisps") {
			t.Errorf("absent cursor: events owned=%v wisps owned=%v, want false/true",
				policy.OwnsTable("events"), policy.OwnsTable("wisps"))
		}
	})

	t.Run("unreadable cursor is an error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cause := errors.New("cursor probe failed")
		mock.ExpectQuery(`information_schema\.schemata`).WillReturnError(cause)
		if _, err := LoadOwnedIgnorePolicy(context.Background(), db); !errors.Is(err, cause) {
			t.Fatalf("LoadOwnedIgnorePolicy error = %v, want %v", err, cause)
		}
	})

	t.Run("existing cursor whose version read fails is an error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		expectCursorProbe(mock, "schema_migrations", true)
		cause := errors.New("cursor read failed")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
			WillReturnError(cause)
		if _, err := LoadOwnedIgnorePolicy(context.Background(), db); !errors.Is(err, cause) {
			t.Fatalf("LoadOwnedIgnorePolicy error = %v, want %v", err, cause)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
