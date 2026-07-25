package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func strp(s string) *string { return &s }

func resetConflictResolveFlags() {
	conflictsResolveOurs = false
	conflictsResolveTheirs = false
	conflictsResolveStrat = ""
}

func TestResolveStrategy(t *testing.T) {
	t.Cleanup(resetConflictResolveFlags)

	cases := []struct {
		name    string
		ours    bool
		theirs  bool
		strat   string
		want    string
		wantErr bool
	}{
		{name: "ours flag", ours: true, want: "ours"},
		{name: "theirs flag", theirs: true, want: "theirs"},
		{name: "strategy flag", strat: "theirs", want: "theirs"},
		{name: "agreeing flags", ours: true, strat: "ours", want: "ours"},
		{name: "both sides", ours: true, theirs: true, wantErr: true},
		{name: "contradiction", ours: true, strat: "theirs", wantErr: true},
		{name: "unset", wantErr: true},
		{name: "garbage strategy", strat: "mine", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetConflictResolveFlags()
			conflictsResolveOurs = c.ours
			conflictsResolveTheirs = c.theirs
			conflictsResolveStrat = c.strat
			got, err := resolveStrategy()
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolveStrategy() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStrategy() error = %v", err)
			}
			if got != c.want {
				t.Errorf("resolveStrategy() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDifferingFieldsDropsAgreement(t *testing.T) {
	fields := []storage.ConflictFieldValue{
		{Name: "id", Base: strp("bd-1"), Ours: strp("bd-1"), Theirs: strp("bd-1")},
		{Name: "status", Base: strp("open"), Ours: strp("closed"), Theirs: strp("open")},
		{Name: "assignee", Ours: strp("zelda"), Theirs: nil},
	}
	got := differingFields(fields)
	if len(got) != 2 {
		t.Fatalf("want status and assignee, got %d fields: %+v", len(got), got)
	}
	if got[0].Name != "status" || got[1].Name != "assignee" {
		t.Errorf("differing fields = %q,%q", got[0].Name, got[1].Name)
	}
}

func TestFilterShownFieldsHonorsAllFields(t *testing.T) {
	rows := []storage.ConflictRow{{
		Table: "issues",
		Key:   "bd-1",
		Fields: []storage.ConflictFieldValue{
			{Name: "id", Ours: strp("bd-1"), Theirs: strp("bd-1")},
			{Name: "status", Ours: strp("open"), Theirs: strp("closed")},
		},
	}}
	if got := filterShownFields(rows, true); len(got[0].Fields) != 2 {
		t.Errorf("--all-fields must keep every column, got %d", len(got[0].Fields))
	}
	got := filterShownFields(rows, false)
	if len(got[0].Fields) != 1 || got[0].Fields[0].Name != "status" {
		t.Errorf("default view = %+v, want only the diverged field", got[0].Fields)
	}
	// The caller's slice must not be mutated: `bd conflicts show` renders the
	// same rows again after filtering for JSON.
	if len(rows[0].Fields) != 2 {
		t.Error("filterShownFields mutated its input")
	}
}

// The conflict class is what tells an operator what ours/theirs will DO, so
// each combination has to be named correctly.
func TestConflictKind(t *testing.T) {
	cases := []struct {
		row  storage.ConflictRow
		want string
	}{
		{storage.ConflictRow{BaseExists: true, OurExists: true, TheirExists: true}, "both sides modified"},
		{storage.ConflictRow{OurExists: true, TheirExists: true}, "both sides added"},
		{storage.ConflictRow{BaseExists: true, OurExists: true}, "we modified / they deleted"},
		{storage.ConflictRow{BaseExists: true, TheirExists: true}, "we deleted / they modified"},
		{storage.ConflictRow{BaseExists: true}, "both sides deleted"},
	}
	for _, c := range cases {
		if got := conflictKind(c.row); got != c.want {
			t.Errorf("conflictKind(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}

func TestConflictValueDistinguishesNullFromEmpty(t *testing.T) {
	if got := conflictValue(nil); got != "(null)" {
		t.Errorf("nil renders as %q, want (null)", got)
	}
	if got := conflictValue(strp("")); got != `""` {
		t.Errorf("empty renders as %q, want \"\"", got)
	}
	if got := conflictValue(strp("a\nb")); got != `a\nb` {
		t.Errorf("newlines must not break the field layout, got %q", got)
	}
}
