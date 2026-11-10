package schema

import (
	"context"
	"fmt"
	"strings"
)

// OwnedIgnorePolicy is the set of Beads-owned clone-local dolt_ignore patterns
// (doltIgnorePatterns plus the version-gated ones this database's main cursor
// qualifies) that residual-change checks exempt: Beads' own clone-local tables
// are expected to stay dirty in the working set, and must never be reported as
// uncommitted operator data. The exemption is by this owned policy ONLY,
// never by whatever dolt_ignore rows happen to be enabled: a user's pattern
// covering a tracked table is exactly the stall that the residual check
// exists to report (GH#5433), so honoring arbitrary rows would silence it.
type OwnedIgnorePolicy struct {
	patterns []string
}

// LoadOwnedIgnorePolicy loads the applicable owned patterns once, reading the
// main cursor through mainSource.currentVersion so a gated pattern (events,
// whose flip is migration 0062) is exempted only once that migration actually
// ran: exempting events on a pre-flip database would silence a tracked events
// table's real changes. An absent cursor reads as version 0 (the gated
// patterns stay out); an unreadable cursor is an error the caller must
// surface, not guess at.
func LoadOwnedIgnorePolicy(ctx context.Context, db DBConn) (OwnedIgnorePolicy, error) {
	mainVersion, err := mainSource.currentVersion(ctx, db)
	if err != nil {
		return OwnedIgnorePolicy{}, fmt.Errorf("reading the main migration cursor for the owned dolt_ignore policy: %w", err)
	}
	patterns := append([]string(nil), doltIgnorePatterns...)
	for _, gated := range versionGatedDoltIgnorePatterns {
		if mainVersion < gated.minMainVersion {
			continue
		}
		patterns = append(patterns, gated.pattern)
	}
	return OwnedIgnorePolicy{patterns: patterns}, nil
}

// Exempts reports whether a dolt_status table_name is a Beads-owned
// clone-local table. Dolt writes a rename row as "<from> -> <to>"; it is
// exempt only when BOTH endpoints are owned local tables, so a rename onto or
// off a tracked table stays a residual change.
func (p OwnedIgnorePolicy) Exempts(statusName string) bool {
	if from, to, ok := strings.Cut(statusName, " -> "); ok {
		return p.exemptsTable(from) && p.exemptsTable(to)
	}
	return p.exemptsTable(statusName)
}

// exemptsTable matches one table name against the owned patterns in the two
// forms Dolt's own matcher gives them: exact names and trailing-% prefixes.
// Dolt matches ? * % only and treats underscores as literal (the __temp__%
// pattern's comment), so the prefix is compared literally; any other wildcard
// syntax is unsupported here and matches nothing rather than guessing at
// Dolt's rules — the safe direction, which reports the table.
func (p OwnedIgnorePolicy) exemptsTable(table string) bool {
	for _, pattern := range p.patterns {
		if prefix, ok := strings.CutSuffix(pattern, "%"); ok {
			if !strings.ContainsAny(prefix, "%?*") && strings.HasPrefix(table, prefix) {
				return true
			}
			continue
		}
		if !strings.ContainsAny(pattern, "%?*") && pattern == table {
			return true
		}
	}
	return false
}

// Owns reports whether an enabled dolt_ignore pattern (as stored in the
// table) belongs to the owned set, matched case-insensitively the way the
// seed probe folds spellings (missingDoltIgnorePatterns). It separates the
// canonical patterns from operator-added rows for diagnostics.
func (p OwnedIgnorePolicy) Owns(pattern string) bool {
	for _, owned := range p.patterns {
		if strings.EqualFold(owned, pattern) {
			return true
		}
	}
	return false
}
