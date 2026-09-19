package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// Pure-logic coverage for `bd conflicts resolve --conclude` and the BLOCKING
// merge state (wy-36ilm F12). Command wiring, JSON output and real merge
// transitions are covered, dolt-backed, in conflicts_conclude_integration_test.go.
// The real system-table column names are guarded separately in
// internal/storage/dolt/conflicts_integration_test.go.

func schemaBlockers(tables ...string) storage.MergeBlockers {
	return storage.MergeBlockers{Merging: true, SchemaConflictTables: tables}
}

func violationBlockers(table string, n int) storage.MergeBlockers {
	return storage.MergeBlockers{
		Merging:              true,
		ConstraintViolations: []storage.ConstraintViolation{{Table: table, Count: n}},
	}
}

// TestPlanConcludeOutcomes pins every branch of --conclude's decision. The
// dangerous inversions are "blocked reads as committable" (pushes over a
// wedged merge) and "an open merge reads as nothing to conclude" (leaves the
// merge open while reporting success).
func TestPlanConcludeOutcomes(t *testing.T) {
	readErr := errStub("dolt_merge_status is missing")
	cases := []struct {
		name       string
		remaining  int
		blockers   storage.MergeBlockers
		blockerErr error
		haveStatus bool
		want       concludeAction
	}{
		{
			name:     "open merge, nothing outstanding, commits",
			blockers: storage.MergeBlockers{Merging: true}, haveStatus: true,
			want: concludeActionCommit,
		},
		{
			name:     "no merge open, nothing to conclude",
			blockers: storage.MergeBlockers{Merging: false}, haveStatus: true,
			want: concludeActionNothingToConclude,
		},
		{
			name:     "schema conflict blocks the commit",
			blockers: schemaBlockers("issues"), haveStatus: true,
			want: concludeActionBlocked,
		},
		{
			name:     "constraint violation blocks the commit",
			blockers: violationBlockers("comments", 1), haveStatus: true,
			want: concludeActionBlocked,
		},
		{
			// A blocker outranks "no merge open": Merging=false with
			// violations outstanding must never read as a no-op.
			name:     "blocked wins over a closed merge status",
			blockers: violationBlockers("labels", 3), haveStatus: true,
			want: concludeActionBlocked,
		},
		{
			name:      "live row conflicts outrank everything",
			remaining: 2, blockers: schemaBlockers("issues"), haveStatus: true,
			want: concludeActionConflictsLive,
		},
		{
			// No MergeBlockerInspector: Merging=false only means "unknown",
			// so the old behavior (attempt the commit) must survive.
			name:       "backend without merge status still attempts the commit",
			haveStatus: false,
			want:       concludeActionCommit,
		},
		{
			// Same for an errored read (adversarial review F3): absence of
			// evidence is not evidence of no merge.
			name:       "unreadable blockers still attempt the commit",
			blockerErr: readErr, haveStatus: true,
			want: concludeActionCommit,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planConclude(c.remaining, c.blockers, c.blockerErr, c.haveStatus)
			if got != c.want {
				t.Errorf("planConclude(%d, %+v, %v, %v) = %v, want %v",
					c.remaining, c.blockers, c.blockerErr, c.haveStatus, got, c.want)
			}
		})
	}
}

// TestShouldCommitResolutionHoldsTheCommit covers the resolve path's gate:
// the merge is committed only when this pass resolved something, nothing is
// left conflicted, --no-commit was not passed, and nothing blocks the commit.
func TestShouldCommitResolutionHoldsTheCommit(t *testing.T) {
	clean := storage.MergeBlockers{Merging: true}
	cases := []struct {
		name      string
		resolved  int
		remaining int
		noCommit  bool
		blockers  storage.MergeBlockers
		want      bool
	}{
		{name: "resolved and clean", resolved: 1, blockers: clean, want: true},
		{name: "nothing resolved", resolved: 0, blockers: clean, want: false},
		{name: "conflicts remain", resolved: 1, remaining: 1, blockers: clean, want: false},
		{name: "--no-commit", resolved: 1, noCommit: true, blockers: clean, want: false},
		{name: "schema conflict holds the commit", resolved: 1, blockers: schemaBlockers("issues"), want: false},
		{name: "violations hold the commit", resolved: 1, blockers: violationBlockers("comments", 2), want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldCommitResolution(c.resolved, c.remaining, c.noCommit, c.blockers); got != c.want {
				t.Errorf("shouldCommitResolution(%d, %d, %v, %+v) = %v, want %v",
					c.resolved, c.remaining, c.noCommit, c.blockers, got, c.want)
			}
		})
	}
}

// TestConcludeFlagValidation: --conclude performs no resolution, so anything
// describing one is a user error rather than a silently ignored flag. --table
// in particular would imply a scoped conclude that does not exist (review F8).
func TestConcludeFlagValidation(t *testing.T) {
	t.Cleanup(resetConcludeFlags)

	cases := []struct {
		name    string
		args    []string
		setup   func()
		wantErr string
	}{
		{name: "bare --conclude"},
		{name: "with an issue id", args: []string{"bd-1"}, wantErr: "takes no issue IDs"},
		{name: "with --all", setup: func() { conflictsResolveAll = true }, wantErr: "takes no issue IDs"},
		{name: "with --ours", setup: func() { conflictsResolveOurs = true }, wantErr: "takes no issue IDs"},
		{name: "with --theirs", setup: func() { conflictsResolveTheirs = true }, wantErr: "takes no issue IDs"},
		{name: "with --strategy", setup: func() { conflictsResolveStrat = "ours" }, wantErr: "takes no issue IDs"},
		{name: "with --table", setup: func() { conflictsResolveTable = "config" }, wantErr: "takes no issue IDs"},
		{name: "with --no-commit", setup: func() { conflictsNoCommit = true }, wantErr: "opposites"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetConcludeFlags()
			if c.setup != nil {
				c.setup()
			}
			err := concludeFlagConflict(c.args)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("concludeFlagConflict() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("concludeFlagConflict() = nil, want an error mentioning %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("concludeFlagConflict() = %q, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func resetConcludeFlags() {
	conflictsResolveOurs = false
	conflictsResolveTheirs = false
	conflictsResolveStrat = ""
	conflictsResolveAll = false
	conflictsResolveTable = ""
	conflictsNoCommit = false
}

// TestWriteMergeBlockersRemedies asserts the diagnosis an operator gets, per
// class. The F1 guard is the important one: dolt refuses `dolt conflicts
// resolve` outright while a schema conflict is live, so the remedy must never
// send anyone there.
func TestWriteMergeBlockersRemedies(t *testing.T) {
	t.Run("nothing blocking prints nothing", func(t *testing.T) {
		var sb strings.Builder
		writeMergeBlockers(&sb, storage.MergeBlockers{Merging: true})
		if sb.String() != "" {
			t.Errorf("an unblocked merge must print nothing, got %q", sb.String())
		}
	})

	t.Run("schema conflict", func(t *testing.T) {
		var sb strings.Builder
		writeMergeBlockers(&sb, schemaBlockers("issues"))
		out := sb.String()
		for _, want := range []string{"schema conflict: issues", "dolt merge --abort", "ALTER TABLE"} {
			if !strings.Contains(out, want) {
				t.Errorf("schema-conflict remedy missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "dolt conflicts resolve") {
			t.Errorf("schema-conflict remedy points at a dolt command that always errors (review F1):\n%s", out)
		}
		if strings.Contains(out, "Constraint violations:") {
			t.Errorf("constraint remedy shown with no violations outstanding:\n%s", out)
		}
	})

	t.Run("constraint violations", func(t *testing.T) {
		var sb strings.Builder
		writeMergeBlockers(&sb, violationBlockers("comments", 3))
		out := sb.String()
		for _, want := range []string{
			"constraint violations: comments (3)",
			"dolt_constraint_violations_",
			"bd conflicts resolve --conclude",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("violation remedy missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Schema conflicts:") {
			t.Errorf("schema remedy shown with no schema conflict outstanding:\n%s", out)
		}
	})

	t.Run("both classes", func(t *testing.T) {
		var sb strings.Builder
		b := schemaBlockers("issues")
		b.ConstraintViolations = violationBlockers("labels", 1).ConstraintViolations
		writeMergeBlockers(&sb, b)
		out := sb.String()
		if !strings.Contains(out, "Schema conflicts:") || !strings.Contains(out, "Constraint violations:") {
			t.Errorf("both classes outstanding must show both remedies:\n%s", out)
		}
		if strings.Contains(out, "bd conflicts resolve --conclude") {
			t.Errorf("must not recommend --conclude while a schema conflict makes it refuse:\n%s", out)
		}
	})
}

// TestMergeBlockersBlocked pins Blocked() itself: an inversion here silently
// turns every gate above into a pass-through.
func TestMergeBlockersBlocked(t *testing.T) {
	cases := []struct {
		name string
		b    storage.MergeBlockers
		want bool
	}{
		{name: "zero value", want: false},
		{name: "merging but nothing outstanding", b: storage.MergeBlockers{Merging: true}, want: false},
		{name: "schema conflict", b: schemaBlockers("issues"), want: true},
		{name: "constraint violation", b: violationBlockers("comments", 1), want: true},
		{
			// A violation row with a zero count cannot reach here — the query
			// filters num_violations > 0 — but the type permits it, and
			// "blocked" is the safe reading of a row dolt bothered to keep.
			name: "violation row with a zero count",
			b:    violationBlockers("comments", 0),
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.b.Blocked(); got != c.want {
				t.Errorf("MergeBlockers%+v.Blocked() = %v, want %v", c.b, got, c.want)
			}
		})
	}
}

// errStub is a minimal error for the blocker-read failure cases.
type errStub string

func (e errStub) Error() string { return string(e) }
