package schema

import (
	"strings"
	"testing"
)

// TestProcedureCallReClassifiesStatements pins the statement-boundary matching
// that decides which migration bodies take the result-set-draining apply path
// (execMigrationBody). False positives are harmless (an extra, no-op drain);
// false negatives reintroduce the bug, so the boundary cases matter.
func TestProcedureCallReClassifiesStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"bare call at start", "CALL DOLT_COMMIT('-Am', 'x')", true},
		{"lowercase call", "call dolt_commit('-Am', 'x')", true},
		{"call after newline", "INSERT INTO t VALUES (1);\nCALL DOLT_COMMIT('-Am', 'x')", true},
		{"call after semicolon same line", "INSERT INTO t VALUES (1); CALL DOLT_COMMIT('-Am', 'x')", true},
		{"indented call", "  \tCALL DOLT_COMMIT('-Am', 'x')", true},
		{"plain ddl only", "ALTER TABLE issues ADD COLUMN is_blocked BOOLEAN", false},
		{"recall is not a call", "UPDATE t SET note = 'please recall this' WHERE id = 1", false},
		{"call as a column-ish word", "SELECT recall_count FROM t", false},
		{"call inside a midline string literal", "UPDATE t SET s = 'we CALL it later'", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := procedureCallRe.MatchString(tc.sql); got != tc.want {
				t.Fatalf("procedureCallRe.MatchString(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

// TestMigrationsWithProcedureCallsTakeDrainPath guards the real embedded
// migrations: every migration whose body actually contains a CALL DOLT_COMMIT
// stored-procedure invocation must be classified for the draining apply path,
// and migrations without one must not be. This is the blast radius of the fix —
// today exactly 0040 and 0041 — and it must track the migrations, not a
// hard-coded list, so a future CALL-bearing migration is covered automatically.
func TestMigrationsWithProcedureCallsTakeDrainPath(t *testing.T) {
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("reading migration %s: %v", mf.name, err)
			}
			body := string(data)
			hasCall := strings.Contains(strings.ToUpper(body), "CALL DOLT_COMMIT")
			detected := procedureCallRe.MatchString(body)
			if hasCall && !detected {
				t.Errorf("%s contains CALL DOLT_COMMIT but is NOT routed to the draining apply path", mf.name)
			}
			if detected && !hasCall {
				t.Errorf("%s is routed to the draining apply path but contains no CALL DOLT_COMMIT", mf.name)
			}
		}
	}
}

// TestFrozenNonIdempotentMigrationsHaveRepairs guards the second half of the
// shared-server brick fix (ahdr-ovz / hq-oi0db). The init retry loop re-runs a
// whole migration after a transient, so any migration that writes the
// dolt_nonlocal_tables system table and commits it with CALL DOLT_COMMIT must
// survive replay. Two shapes are non-idempotent:
//
//   - a bare INSERT INTO dolt_nonlocal_tables (not INSERT IGNORE) replays into
//     "duplicate primary key given: [wisps]" (migration 0040), and
//   - a CALL DOLT_COMMIT without --skip-empty replays into "nothing to commit"
//     after an idempotent no-op DELETE/INSERT (migrations 0040 and 0041).
//
// The shipped bytes of 0040/0041 are frozen and content-hashed
// (scripts/check-migration-hygiene.sh check C), so they are NOT rewritten to be
// idempotent — that would fork every already-upgraded clone via the recorded
// content_hash. Instead each such migration must have a pre-migration repair
// registered in preMigrationRepairs that normalises state so the frozen body
// replays cleanly. This test tracks the embedded migrations rather than a
// hard-coded list, so a future non-idempotent nonlocal-table migration either
// gets a repair or fails here.
func TestFrozenNonIdempotentMigrationsHaveRepairs(t *testing.T) {
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("reading migration %s: %v", mf.name, err)
			}
			body := string(data)
			upper := strings.ToUpper(body)
			if !strings.Contains(upper, "DOLT_NONLOCAL_TABLES") {
				continue
			}

			nonIdempotent := false
			// Bare "INSERT INTO dolt_nonlocal_tables" — the dup-key shape.
			// "INSERT IGNORE INTO ..." does not contain this substring.
			if strings.Contains(upper, "INSERT INTO DOLT_NONLOCAL_TABLES") {
				nonIdempotent = true
			}
			// A self-commit without --skip-empty — the nothing-to-commit shape.
			for _, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "--") {
					continue // SQL comment, not an executed statement
				}
				u := strings.ToUpper(trimmed)
				if strings.Contains(u, "CALL DOLT_COMMIT") && !strings.Contains(u, "--SKIP-EMPTY") {
					nonIdempotent = true
				}
			}
			if !nonIdempotent {
				continue
			}

			if _, ok := preMigrationRepairs[repairKey{src.cursorTable, mf.version}]; !ok {
				t.Errorf("%s (%s v%d) is a frozen non-idempotent dolt_nonlocal_tables migration but has no pre-migration repair registered in preMigrationRepairs; a retry replay will brick the database. Register a repair keyed to {%q, %d} rather than editing the shipped SQL.",
					mf.name, src.cursorTable, mf.version, src.cursorTable, mf.version)
			}
		}
	}
}
