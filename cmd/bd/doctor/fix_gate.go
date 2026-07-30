package doctor

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/schema"
)

// FixGate reports whether `bd doctor --fix` is safe given the schema/binary
// relationship (GH#4993): opening the store can apply pending migrations.
// Callers ask three distinct questions, so it answers three.
type FixGate struct {
	// Determined is false when the DB was reachable but its version could not
	// be read. Unknown is not safe.
	Determined bool
	// DBReachable is false when there is no database at all (no server, no
	// repo) — no schema hazard exists, so filesystem repair is unaffected.
	DBReachable bool

	// RecommendFix allows printed advice to steer at `bd doctor --fix`.
	RecommendFix bool
	// AllowDBFix allows repairs that may write schema or resume migrations.
	AllowDBFix bool
	// AllowFSFix allows filesystem-only repairs, which never touch the schema.
	AllowFSFix bool

	DBVersion     int // max applied schema_migrations version (0 if unknown)
	BinaryVersion int // schema.LatestVersion() for this binary
	Reason        string
	Ahead         bool // DB schema newer than this binary
	Pending       bool // binary knows migrations the DB has not applied
}

// AssessSchemaFixGate compares schema_migrations to this binary. Assess once
// per invocation, before anything that can write, and thread the result
// through: probing at print time cannot guard writes that already happened.
func AssessSchemaFixGate(path string) FixGate {
	binary := schema.LatestVersion()

	// No DB reachable: scoped allowance, not a fail-open — DB fixes stay
	// disallowed because there is no DB to fix.
	unreachable := FixGate{
		BinaryVersion: binary,
		Determined:    true,
		RecommendFix:  true,
		AllowFSFix:    true,
	}

	beadsDir := ResolveBeadsDirForRepo(path)
	db, _, err := openDoltDB(beadsDir)
	if err != nil {
		return unreachable
	}
	defer db.Close()

	// Reachable but unreadable: fail closed rather than claim safety.
	undetermined := FixGate{
		BinaryVersion: binary,
		DBReachable:   true,
		AllowFSFix:    true,
		Reason: "database schema version could not be determined; " +
			"`bd doctor --fix` may apply migrations blind — resolve the database state first",
	}

	ctx := context.Background()
	dbVer, err := schema.CurrentVersion(ctx, db)
	if err != nil || dbVer == 0 {
		return undetermined
	}

	gate := FixGate{
		Determined:    true,
		DBReachable:   true,
		DBVersion:     dbVer,
		BinaryVersion: binary,
		AllowFSFix:    true,
	}

	switch {
	case dbVer > binary:
		gate.Ahead = true
		gate.Reason = fmt.Sprintf(
			"database schema is at v%d, this binary knows up to v%d (%s ahead). "+
				"`bd doctor --fix` can write/migrate and is unsafe on a newer schema — upgrade bd first",
			dbVer, binary, pluralMigrations(dbVer-binary),
		)
	case dbVer < binary:
		gate.Pending = true
		gate.Reason = fmt.Sprintf(
			"database schema is at v%d, this binary expects v%d (%s pending). "+
				"`bd doctor --fix` may apply write migrations — upgrade/migrate deliberately, not as a cosmetic tip",
			dbVer, binary, pluralMigrations(binary-dbVer),
		)
	default:
		gate.RecommendFix = true
		gate.AllowDBFix = true
	}

	return gate
}

func pluralMigrations(n int) string {
	if n == 1 {
		return "1 migration"
	}
	return fmt.Sprintf("%d migrations", n)
}

// fixAdvicePattern matches a tip steering at `bd doctor --fix`, tolerating flag
// order and spacing. Best-effort: it is still a sniff over free-form prose.
var fixAdvicePattern = regexp.MustCompile(`(?i)\bdoctor\b[^\n]*--fix\b`)

// originalTipMarker prefixes preserved advice in a rewritten tip, and doubles
// as the idempotence sentinel.
const originalTipMarker = "Original tip was: "

// MentionsFixAdvice reports whether a tip steers at `bd doctor --fix`.
func MentionsFixAdvice(fix string) bool {
	return fixAdvicePattern.MatchString(fix)
}

// SanitizeFixRecommendation rewrites Fix text that steers into `bd doctor
// --fix` when the gate says that is unsafe (GH#4993). Apply once to the
// result, not per-renderer — see sanitizeFixAdvice in cmd/bd.
func SanitizeFixRecommendation(fix string, gate FixGate) string {
	if gate.RecommendFix || fix == "" || !MentionsFixAdvice(fix) {
		return fix
	}
	// The --fix path sanitizes again after re-running diagnostics.
	if strings.Contains(fix, originalTipMarker) {
		return fix
	}

	switch {
	case gate.Ahead:
		return fmt.Sprintf("Do NOT run 'bd doctor --fix' until bd is upgraded (%s). %s%s",
			gate.Reason, originalTipMarker, fix)
	case gate.Pending:
		return fmt.Sprintf(
			"Avoid 'bd doctor --fix' for cosmetic repair while schema migrations are pending (%s). "+
				"Prefer targeted fixes or an intentional migrate. %s%s",
			gate.Reason, originalTipMarker, fix)
	case !gate.Determined:
		return fmt.Sprintf("Do NOT run 'bd doctor --fix' (%s). %s%s",
			gate.Reason, originalTipMarker, fix)
	}
	return fix
}

// filesystemOnlyFixes are repairs that provably touch only files on disk.
// Unlisted names are treated as database-touching, so a fix added later is
// guarded by default rather than escaping the gate silently.
var filesystemOnlyFixes = map[string]bool{
	"Gitignore":             true,
	"Project Gitignore":     true,
	"Metadata Config":       true,
	"Redirect Tracking":     true,
	"Last-Touched Tracking": true,
	"Tracked Runtime Files": true,
	"Git Hooks":             true,
	"Permissions":           true,
	"Lock Files":            true,
	"Legacy MQ Files":       true,
	"Classic Artifacts":     true,
	"Btrfs NoCOW (dolt)":    true,
}

// IsFilesystemOnlyFix reports whether the named fix touches only the
// filesystem. Unknown names report false — see filesystemOnlyFixes.
func IsFilesystemOnlyFix(checkName string) bool {
	return filesystemOnlyFixes[strings.TrimSpace(checkName)]
}
