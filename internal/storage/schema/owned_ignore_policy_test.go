package schema

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

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

	t.Run("absent cursor is version 0", func(t *testing.T) {
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
}
