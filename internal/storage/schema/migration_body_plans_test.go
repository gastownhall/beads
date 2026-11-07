package schema

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The migration body plan machinery (gastownhall/beads#6520, AC-4): a plan
// substitutes only a registered middle segment and fails closed on any drift
// between the plan and the shipped bytes; an unregistered migration must
// resolve to its frozen bytes byte-for-byte. The machinery tests below
// inject synthetic registry entries (withBodyPlan) so they hold regardless
// of which real plans are shipped in migrationBodyPlans.

// withBodyPlan registers plan under key for one test and restores whatever
// was registered there before (usually nothing) on cleanup.
func withBodyPlan(t *testing.T, key repairKey, plan migrationBodyPlan) {
	t.Helper()
	prev, had := migrationBodyPlans[key]
	migrationBodyPlans[key] = plan
	t.Cleanup(func() {
		if had {
			migrationBodyPlans[key] = prev
			return
		}
		delete(migrationBodyPlans, key)
	})
}

// main0059Body returns the frozen bytes of main migration 0059, the file the
// plan machinery's substitution and drift cases exercise.
func main0059Body(t *testing.T) []byte {
	t.Helper()
	const name = "0059_recompute_null_gate_is_blocked.up.sql"
	data, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return data
}

// main0059BasePlan returns a plan whose identity fields all match the frozen
// 0059 file, with throwaway DDL and CTEs: correct in every way the resolver
// checks, so each drift case can mutate exactly one thing.
func main0059BasePlan(t *testing.T) migrationBodyPlan {
	t.Helper()
	const name = "0059_recompute_null_gate_is_blocked.up.sql"
	sum := sha256.Sum256(main0059Body(t))
	return migrationBodyPlan{
		dir:            mainSource.dir,
		name:           name,
		expectedSHA256: hex.EncodeToString(sum[:]),
		finalUpdate:    "\nUPDATE issues\n",
		indexDDL:       []string{"CREATE INDEX idx_probe ON __bd_0059_recompute_wisp_deps (issue_id);"},
		replacementCTEs: "WITH RECURSIVE\n  probe(x) AS (SELECT 1 UNION SELECT x + 1 FROM probe WHERE x < 1)\n",
	}
}

func TestResolveMigrationBodyUnregisteredPassesFrozenBytesThrough(t *testing.T) {
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			if _, planned := migrationBodyPlans[repairKey{src.cursorTable, mf.version}]; planned {
				continue // registered files assert their own substitution below
			}
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("reading %s/%s: %v", src.dir, mf.name, err)
			}
			got, err := resolveMigrationBody(src, mf, data)
			if err != nil {
				t.Fatalf("%s/%s: resolving an unregistered migration: %v", src.dir, mf.name, err)
			}
			if got != string(data) {
				t.Errorf("%s/%s: unregistered migration's resolved body is not byte-identical to its embedded bytes", src.dir, mf.name)
			}
		}
	}
}

func TestResolveMigrationBodySubstitutesOnlyTheMiddleSegment(t *testing.T) {
	const head = "\nWITH RECURSIVE\n"
	const finalUpdate = "\nUPDATE issues\n"
	data := main0059Body(t)
	plan := main0059BasePlan(t)
	withBodyPlan(t, repairKey{mainSource.cursorTable, 59}, plan)

	got, err := resolveMigrationBody(mainSource, migrationFile{version: 59, name: plan.name}, data)
	if err != nil {
		t.Fatalf("resolveMigrationBody: %v", err)
	}
	frozen := string(data)
	i, j := strings.Index(frozen, head), strings.Index(frozen, finalUpdate)
	prefix, suffix := frozen[:i], frozen[j:]
	if !strings.HasPrefix(got, prefix) {
		t.Error("planned body does not preserve the frozen prefix byte-for-byte (stand-in creation, guarded copies, zeroing UPDATE)")
	}
	if !strings.HasSuffix(got, suffix) {
		t.Error("planned body does not preserve the frozen suffix byte-for-byte (final assignments, stand-in DROPs)")
	}
	middle := got[len(prefix) : len(got)-len(suffix)]
	for _, want := range []string{"\n" + plan.indexDDL[0] + "\n", plan.replacementCTEs} {
		if !strings.Contains(middle, want) {
			t.Errorf("planned middle %q does not contain %q", middle, want)
		}
	}
	if n := strings.Count(middle, "WITH RECURSIVE"); n != 1 {
		t.Errorf("planned middle carries %d WITH RECURSIVE heads, want 1", n)
	}
	if strings.Contains(got[:len(prefix)], "CREATE INDEX") || strings.Contains(suffix, "CREATE INDEX") {
		t.Error("plan DDL leaked into the preserved prefix or suffix")
	}
}

// TestResolveMigrationBodyFailsClosedOnDrift proves the fail-closed half of
// the contract: any mismatch between a registered plan and the shipped file
// is a descriptive error before a body is returned — never a silent fallback
// to the frozen bytes and never a partial substitution.
func TestResolveMigrationBodyFailsClosedOnDrift(t *testing.T) {
	const head = "\nWITH RECURSIVE\n"
	const finalUpdate = "\nUPDATE issues\n"
	cases := []struct {
		name    string
		mutPlan func(*migrationBodyPlan)
		mutData func([]byte) []byte
		wantErr string
	}{
		{
			name:    "frozen hash drifted from the pin",
			mutPlan: func(p *migrationBodyPlan) { p.expectedSHA256 = strings.Repeat("0", 64) },
			wantErr: "does not match the plan's pinned hash",
		},
		{
			name:    "plan registered for another source directory",
			mutPlan: func(p *migrationBodyPlan) { p.dir = "migrations/ignored" },
			wantErr: "identity mismatch",
		},
		{
			name:    "plan registered for another filename",
			mutPlan: func(p *migrationBodyPlan) { p.name = "0059_recompute_some_other_body.up.sql" },
			wantErr: "identity mismatch",
		},
		{
			name:    "same version, wrong source cursor table",
			mutPlan: func(p *migrationBodyPlan) { p.finalUpdate = "\nUPDATE wisps\n" },
			wantErr: "final UPDATE anchor",
		},
		{
			name:    "head anchor missing from the frozen body",
			mutData: func(d []byte) []byte { return bytes.Replace(d, []byte(head), []byte("\nWITH PLAIN\n"), 1) },
			wantErr: "head anchor",
		},
		{
			name: "head anchor appears twice",
			mutData: func(d []byte) []byte {
				// The separator keeps the two occurrences from merging into
				// one non-overlapping strings.Count match.
				return bytes.Replace(d, []byte(head), []byte(head+"-- duplicated\n"+head), 1)
			},
			wantErr: "head anchor",
		},
		{
			name:    "final UPDATE anchor missing from the frozen body",
			mutData: func(d []byte) []byte { return bytes.Replace(d, []byte(finalUpdate), []byte("\nUPDATE issues SET"), 1) },
			wantErr: "final UPDATE anchor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := main0059Body(t)
			plan := main0059BasePlan(t)
			if tc.mutData != nil {
				data = tc.mutData(data)
				// Pin the plan to the TAMPERED bytes so the case exercises
				// the anchor validation, not the hash check that any byte
				// edit would trip first.
				sum := sha256.Sum256(data)
				plan.expectedSHA256 = hex.EncodeToString(sum[:])
			}
			if tc.mutPlan != nil {
				tc.mutPlan(&plan)
			}
			withBodyPlan(t, repairKey{mainSource.cursorTable, 59}, plan)
			got, err := resolveMigrationBody(mainSource, migrationFile{version: 59, name: "0059_recompute_null_gate_is_blocked.up.sql"}, data)
			if err == nil {
				t.Fatalf("resolveMigrationBody succeeded; want a fail-closed error mentioning %q", tc.wantErr)
			}
			if got != "" {
				t.Error("resolveMigrationBody returned a body alongside the error; fail-closed means no body")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), "0059_recompute_null_gate_is_blocked.up.sql") {
				t.Errorf("error %q does not name the drifted migration file", err)
			}
		})
	}
}

// TestRunMigrationsFailsClosedBeforeAnyMutationOnPlanDrift is the runner-level
// fail-closed proof: a registered-but-drifted plan for the next pending step
// aborts the pass before the step's repair, body or cursor mutation — before
// ANY statement reaches the database. The sqlmock connection has zero
// expectations, so any query or exec would fail the test twice over (at call
// time and via ExpectationsWereMet).
func TestRunMigrationsFailsClosedBeforeAnyMutationOnPlanDrift(t *testing.T) {
	orig := stderr
	stderr = &bytes.Buffer{}
	defer func() { stderr = orig }()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	const name = "0059_recompute_null_gate_is_blocked.up.sql"
	// The plan is registered under a foreign cursor table with a drifted
	// hash: the point is what the RUNNER does when the resolver rejects the
	// step, not which rejection it picks.
	poison := main0059BasePlan(t)
	poison.expectedSHA256 = strings.Repeat("0", 64)
	withBodyPlan(t, repairKey{"poison_schema_migrations", 59}, poison)
	src := migrationSource{files: upMigrations, dir: "migrations", cursorTable: "poison_schema_migrations"}

	_, err = runMigrations(context.Background(), db, src, 58, 60, false)
	if err == nil {
		t.Fatal("runMigrations succeeded with a drifted execution plan; want a fail-closed abort")
	}
	for _, want := range []string{"resolving execution plan for migration " + name, "poison_schema_migrations", "pinned hash"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the drifted plan must abort before any database call: %v", err)
	}
}

// bodyCaptureDB records every ExecContext so a test can prove which exact
// bodies the runner executed and what the cursor rows recorded.
type bodyCaptureDB struct {
	execQueries []string
	execArgs    [][]any
}

func (c *bodyCaptureDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	c.execQueries = append(c.execQueries, query)
	c.execArgs = append(c.execArgs, args)
	return nil, nil
}

func (c *bodyCaptureDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("bodyCaptureDB: QueryContext not expected on the unregistered path")
}

func (c *bodyCaptureDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("bodyCaptureDB: QueryRowContext not expected on the unregistered path")
}

// TestRunMigrationsExecutesFrozenBytesForUnregisteredSteps proves the seam's
// no-op half end to end through the runner: an unregistered step's ExecContext
// carries the frozen bytes verbatim, and the cursor row records the frozen
// file's SHA-256 — the identity a plan must never change.
func TestRunMigrationsExecutesFrozenBytesForUnregisteredSteps(t *testing.T) {
	orig := stderr
	stderr = &bytes.Buffer{}
	defer func() { stderr = orig }()

	// mockDB.QueryRowContext panics; the main-source row-count notice runs
	// on this cursor table, so stub it like the neighbouring runner tests.
	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	defer func() { issueRowCounter = origCounter }()
	stubUpdatedAtGuard(t)

	capture := &bodyCaptureDB{}
	if _, err := runMigrations(context.Background(), capture, mainSource, 0, 2, false); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	// The runner executes each step as its frozen body followed by its
	// cursor row: body(v1), cursor(v1), body(v2), cursor(v2), in order.
	if n := len(capture.execQueries); n != 4 {
		t.Fatalf("captured %d ExecContext calls, want 4 (body + cursor row per step): %q", n, capture.execQueries)
	}
	for v, name := range map[int]string{1: "0001_create_issues.up.sql", 2: "0002_create_dependencies.up.sql"} {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		wantHash := hex.EncodeToString(sum[:])
		bodyIdx, cursorIdx := 2*(v-1), 2*(v-1)+1
		if capture.execQueries[bodyIdx] != string(data) {
			t.Errorf("v%d (%s): the runner did not execute the frozen bytes verbatim", v, name)
		}
		q := capture.execQueries[cursorIdx]
		if !strings.HasPrefix(q, "INSERT IGNORE INTO schema_migrations (version, content_hash)") {
			t.Errorf("v%d: expected the cursor INSERT, got %q", v, q)
			continue
		}
		if got := capture.execArgs[cursorIdx]; len(got) != 2 || got[0] != v || got[1] != wantHash {
			t.Errorf("v%d cursor row recorded %v, want (%d, %s)", v, got, v, wantHash)
		}
	}
}
