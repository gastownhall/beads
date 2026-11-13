package schema

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// OwnedIgnorePolicy is the set of dolt_ignore patterns bd itself asserts on a
// database (doltIgnorePatterns plus the version-gated patterns its main cursor
// qualifies): the clone-local tables whose working-set changes a commit is
// expected to leave behind. Other enabled patterns, an operator's or an older
// tool's, are not part of it, so a commit check that exempts only this policy
// still reports a tracked table some other pattern hides (GH#5433).
type OwnedIgnorePolicy struct {
	patterns []string
}

// LoadOwnedIgnorePolicy reads the main migration cursor once and returns the
// policy it qualifies. A missing cursor table is version 0 (no gated pattern
// applies yet); a cursor that cannot be read is an error, never version 0, so
// a caller cannot exempt less or more than the database actually owns.
func LoadOwnedIgnorePolicy(ctx context.Context, db DBConn) (OwnedIgnorePolicy, error) {
	mainVersion, err := mainSource.currentVersion(ctx, db)
	if err != nil {
		return OwnedIgnorePolicy{}, fmt.Errorf("read main migration cursor: %w", err)
	}
	return ownedIgnorePolicyAt(mainVersion), nil
}

func ownedIgnorePolicyAt(mainVersion int) OwnedIgnorePolicy {
	return OwnedIgnorePolicy{patterns: doltIgnorePatternsAt(mainVersion)}
}

// OwnsPattern reports whether pattern is one bd asserts itself.
func (p OwnedIgnorePolicy) OwnsPattern(pattern string) bool {
	return slices.Contains(p.patterns, pattern)
}

// OwnsTable reports whether a dolt_status table_name is bd clone-local state.
// A rename row ("old -> new") is owned only when both endpoints are.
func (p OwnedIgnorePolicy) OwnsTable(table string) bool {
	if from, to, ok := strings.Cut(table, " -> "); ok {
		return p.ownsName(from) && p.ownsName(to)
	}
	return p.ownsName(table)
}

func (p OwnedIgnorePolicy) ownsName(table string) bool {
	for _, pattern := range p.patterns {
		if ownedPatternMatches(pattern, table) {
			return true
		}
	}
	return false
}

// ownedPatternMatches supports the two shapes bd's own patterns use: an exact
// name, or a literal prefix followed by one trailing %. Underscores are
// literal, as they are in dolt_ignore. Any other wildcard (? * or a % that is
// not last) matches nothing, and neither does a bare catch-all %, so an
// unsupported pattern exempts no table.
func ownedPatternMatches(pattern, table string) bool {
	prefix, wildcard := strings.CutSuffix(pattern, "%")
	if prefix == "" || strings.ContainsAny(prefix, "%?*") {
		return false
	}
	if wildcard {
		return strings.HasPrefix(table, prefix)
	}
	return table == pattern
}
