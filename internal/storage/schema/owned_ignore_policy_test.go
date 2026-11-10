package schema

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectOwnedPolicyCursor mocks the main-cursor read LoadOwnedIgnorePolicy
// does: the existence probe (expectCursorProbe), then the version read when
// the cursor exists.
func expectOwnedPolicyCursor(mock sqlmock.Sqlmock, exists bool, version int) {
	expectCursorProbe(mock, "schema_migrations", exists)
	if exists {
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", version)
	}
}

// TestBeadsOwnedIgnorePolicy pins the residual-change exemption set (GH#5433):
// which dolt_status names a commit-verification may exempt — Beads' owned
// clone-local patterns, gated by the main cursor — and how those patterns
// match. A user's pattern covering a tracked table must never be an exemption,
// and a rename is exempt only when both endpoints are owned.
func TestBeadsOwnedIgnorePolicy(t *testing.T) {
	t.Run("cursor gates the events pattern", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			cursorGone  bool
			mainVersion int
		}{
			{name: "absent cursor is version 0", cursorGone: true, mainVersion: 0},
			{name: "pre-flip cursor", cursorGone: false, mainVersion: 61},
			{name: "flipped cursor", cursorGone: false, mainVersion: 62},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("create sql mock: %v", err)
				}
				defer db.Close()
				expectOwnedPolicyCursor(mock, !tc.cursorGone, tc.mainVersion)

				policy, err := LoadOwnedIgnorePolicy(t.Context(), db)
				if err != nil {
					t.Fatalf("LoadOwnedIgnorePolicy: %v", err)
				}
				if got := policy.Exempts("events"); got != (tc.mainVersion >= 62 && !tc.cursorGone) {
					t.Errorf("Exempts(events) at cursor %d (absent=%v) = %v, want %v",
						tc.mainVersion, tc.cursorGone, got, tc.mainVersion >= 62 && !tc.cursorGone)
				}
				// The canonical clone-local set is exempt at every cursor.
				for _, table := range []string{"wisps", "wisp_dependencies", "local_metadata", "leases", "repo_mtimes", "ignored_schema_migrations", "bd_events_journal", "bd_events_seq"} {
					if !policy.Exempts(table) {
						t.Errorf("Exempts(%q) at cursor %d = false, want true", table, tc.mainVersion)
					}
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet SQL expectations: %v", err)
				}
			})
		}
	})

	t.Run("tracked tables and operator patterns are never exempt", func(t *testing.T) {
		policy := OwnedIgnorePolicy{patterns: expectedIgnoreSeedCandidates(63)}
		for _, table := range []string{"issues", "config", "dependencies", "events_archive", "wispX"} {
			if policy.Exempts(table) {
				t.Errorf("Exempts(%q) = true, want false (only owned clone-local tables are exempt)", table)
			}
		}
	})

	t.Run("underscore is literal and trailing % is a prefix", func(t *testing.T) {
		policy := OwnedIgnorePolicy{patterns: expectedIgnoreSeedCandidates(63)}
		if !policy.Exempts("wisp_some_table") {
			t.Error("Exempts(\"wisp_some_table\") = false, want true (wisp_% prefix)")
		}
		if !policy.Exempts("__temp__events_flip") {
			t.Error("Exempts(\"__temp__events_flip\") = false, want true (__temp__% prefix with literal underscores)")
		}
		if policy.Exempts("wisps_archive") {
			t.Error("Exempts(\"wisps_archive\") = true, want false (wisps is exact, not a prefix)")
		}
		if policy.Exempts("wisp") {
			t.Error("Exempts(\"wisp\") = true, want false (wisp_% needs the underscore)")
		}
	})

	t.Run("unsupported pattern syntax matches nothing", func(t *testing.T) {
		// Not the owned set — a direct probe of the matcher's rule: a pattern
		// this matcher cannot interpret (embedded %, ? or *) is never
		// guessed at; the table is reported, the safe direction.
		policy := OwnedIgnorePolicy{patterns: []string{"a%b", "x?y", "z*"}}
		for _, table := range []string{"a%b", "aXb", "x?y", "xay", "z*", "za", "anything"} {
			if policy.Exempts(table) {
				t.Errorf("Exempts(%q) with unsupported patterns = true, want false", table)
			}
		}
		// The real owned set contains only the two supported forms, pinned
		// so adding a third form fails here instead of silently never
		// matching.
		for _, pattern := range expectedIgnoreSeedCandidates(63) {
			if strings.ContainsAny(pattern, "?*") {
				t.Errorf("owned pattern %q uses ? or * syntax the matcher does not support", pattern)
			}
			if rest, ok := strings.CutSuffix(pattern, "%"); ok && strings.Contains(rest, "%") {
				t.Errorf("owned pattern %q has an embedded %% the matcher does not support", pattern)
			}
		}
	})

	t.Run("renames are exempt only when both endpoints are owned", func(t *testing.T) {
		policy := OwnedIgnorePolicy{patterns: expectedIgnoreSeedCandidates(63)}
		if !policy.Exempts("__temp__wisps -> wisps") {
			t.Error("Exempts rename of owned tables = false, want true")
		}
		if policy.Exempts("wisps -> issues") {
			t.Error("Exempts rename onto a tracked table = true, want false")
		}
		if policy.Exempts("issues -> config") {
			t.Error("Exempts rename between tracked tables = true, want false")
		}
	})

	t.Run("Owns separates the canonical set", func(t *testing.T) {
		policy := OwnedIgnorePolicy{patterns: expectedIgnoreSeedCandidates(63)}
		for _, pattern := range expectedIgnoreSeedCandidates(63) {
			if !policy.Owns(pattern) {
				t.Errorf("Owns(%q) = false, want true", pattern)
			}
		}
		if policy.Owns("issues") {
			t.Error("Owns(\"issues\") = true, want false (an operator pattern is not canonical)")
		}
	})

	t.Run("unreadable cursor is an error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()
		expectCursorProbe(mock, "schema_migrations", true)
		unreadable := errors.New("cursor read failed")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
			WillReturnError(unreadable)

		if _, err := LoadOwnedIgnorePolicy(t.Context(), db); err == nil {
			t.Fatal("LoadOwnedIgnorePolicy with an unreadable cursor = nil error, want the read failure")
		} else if !errors.Is(err, unreadable) {
			t.Fatalf("LoadOwnedIgnorePolicy error = %v, want the unreadable-cursor cause", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
