package schema

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// stepRecordingDB fails the test on any SQL: a step whose execution plan does
// not resolve must not reach its dirty-table snapshot, updated_at snapshot,
// pre-repair, body or cursor row.
type stepRecordingDB struct {
	t     *testing.T
	calls []string
}

func (d *stepRecordingDB) record(query string) {
	d.t.Helper()
	d.calls = append(d.calls, query)
	d.t.Errorf("SQL issued for a step whose execution plan failed to resolve:\n%.300s", query)
}

func (d *stepRecordingDB) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	d.record(query)
	return execOK{}, nil
}

func (d *stepRecordingDB) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	d.record(query)
	return nil, sql.ErrNoRows
}

func (d *stepRecordingDB) QueryRowContext(_ context.Context, query string, _ ...any) *sql.Row {
	d.record(query)
	panic("stepRecordingDB: no row to return")
}

// withMigrationBodyPlans swaps the plan registry for the test.
func withMigrationBodyPlans(t *testing.T, plans map[repairKey]migrationBodyPlan) {
	t.Helper()
	orig := migrationBodyPlans
	migrationBodyPlans = plans
	t.Cleanup(func() { migrationBodyPlans = orig })
}

// TestMigrationBodyPlanDriftFailsBeforeStepMutation is #6520 AC-4's fail-closed
// half: a registered plan that no longer matches its file (identity, hash or
// anchors) errors out of runMigrations, naming the problem, before the step
// issues any SQL at all -- no snapshot, repair (ignored/0015 has one), body or
// cursor row.
func TestMigrationBodyPlanDriftFailsBeforeStepMutation(t *testing.T) {
	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	t.Cleanup(func() { issueRowCounter = origCounter })

	main59 := migrationBodyPlans[repairKey{"schema_migrations", 59}]
	ign15 := migrationBodyPlans[repairKey{"ignored_schema_migrations", 15}]
	if main59.name == "" || ign15.name == "" {
		t.Fatal("main 59 / ignored 15 execution plans are not registered")
	}

	type drift struct {
		name    string
		mutate  func(p migrationBodyPlan) migrationBodyPlan
		wantErr string
	}
	drifts := []drift{
		{"hash", func(p migrationBodyPlan) migrationBodyPlan {
			p.sha256 = strings.Repeat("0", 64)
			return p
		}, "expects content hash"},
		{"file name", func(p migrationBodyPlan) migrationBodyPlan {
			p.name = strings.Replace(p.name, "recompute", "recalc", 1)
			return p
		}, "does not match migration"},
		{"source dir", func(p migrationBodyPlan) migrationBodyPlan {
			p.dir = "migrations/elsewhere"
			return p
		}, "does not match migration"},
		{"absent head anchor", func(p migrationBodyPlan) migrationBodyPlan {
			p.head = "\nWITH RECURSIVE reachable\n"
			return p
		}, "occurs 0 times"},
		{"repeated head anchor", func(p migrationBodyPlan) migrationBodyPlan {
			p.head = "\n  )"
			return p
		}, "want 1"},
		{"final anchor before head", func(p migrationBodyPlan) migrationBodyPlan {
			p.finalUpdate = "\nDROP TABLE IF EXISTS __bd_0059_recompute_wisps;\n"
			return p
		}, "does not follow"},
		{"empty anchor", func(p migrationBodyPlan) migrationBodyPlan {
			p.finalUpdate = ""
			return p
		}, "empty anchor"},
	}

	for _, step := range []struct {
		name           string
		src            migrationSource
		key            repairKey
		plan           migrationBodyPlan
		from, to       int
		commitEachStep bool
	}{
		{"main 59", mainSource, repairKey{"schema_migrations", 59}, main59, 58, 59, true},
		{"ignored 15", ignoredSource, repairKey{"ignored_schema_migrations", 15}, ign15, 14, 15, false},
	} {
		for _, dr := range drifts {
			if dr.name == "final anchor before head" && step.key.version != 59 {
				continue // ignored/0015 has no stand-in DROP to anchor on
			}
			t.Run(step.name+"/"+dr.name, func(t *testing.T) {
				withMigrationBodyPlans(t, map[repairKey]migrationBodyPlan{step.key: dr.mutate(step.plan)})
				db := &stepRecordingDB{t: t}
				applied, err := runMigrations(context.Background(), db, step.src, step.from, step.to, step.commitEachStep)
				if err == nil {
					t.Fatalf("runMigrations applied a step whose plan drifted (%s)", dr.name)
				}
				if !strings.Contains(err.Error(), "planning migration") || !strings.Contains(err.Error(), dr.wantErr) {
					t.Errorf("runMigrations error = %q, want a planning error containing %q", err, dr.wantErr)
				}
				if applied != 0 || len(db.calls) != 0 {
					t.Errorf("applied %d steps and issued %d SQL calls; want none", applied, len(db.calls))
				}
			})
		}
	}

	t.Run("a plan registered under another source's key", func(t *testing.T) {
		// Main 59's plan keyed to an ignored-source version must not apply
		// to that version's file.
		withMigrationBodyPlans(t, map[repairKey]migrationBodyPlan{{"ignored_schema_migrations", 15}: main59})
		db := &stepRecordingDB{t: t}
		_, err := runMigrations(context.Background(), db, ignoredSource, 14, 15, false)
		if err == nil || !strings.Contains(err.Error(), "does not match migration") {
			t.Fatalf("runMigrations error = %v, want the plan/source mismatch", err)
		}
		if len(db.calls) != 0 {
			t.Errorf("issued %d SQL calls; want none", len(db.calls))
		}
	})
}

// TestMigrationBodyPlanUnregisteredIsByteIdentical is #6520 AC-4's other half:
// every migration file without a registered plan executes as its embedded
// bytes, and the two registered plans resolve against the shipped files.
func TestMigrationBodyPlanUnregisteredIsByteIdentical(t *testing.T) {
	planned := 0
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("reading %s/%s: %v", src.dir, mf.name, err)
			}
			body, err := src.plannedMigrationBody(mf.version, mf.name, data)
			if err != nil {
				t.Errorf("%s/%s: %v", src.dir, mf.name, err)
				continue
			}
			if _, ok := migrationBodyPlans[repairKey{src.cursorTable, mf.version}]; ok {
				planned++
				if body == string(data) {
					t.Errorf("%s/%s: registered plan left the body unchanged", src.dir, mf.name)
				}
				continue
			}
			if body != string(data) {
				t.Errorf("%s/%s has no plan but its executed body differs from the embedded bytes", src.dir, mf.name)
			}
		}
	}
	if planned != len(migrationBodyPlans) || planned != 2 {
		t.Errorf("resolved %d planned files for %d registered plans; want main 59 and ignored 15", planned, len(migrationBodyPlans))
	}
}
