package journalscan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSQLWritesBeadTable pins the DML detector the completeness guards rest on.
// A false negative here silently disarms every guard, so the templated forms —
// plain %s and the explicit-argument-index %[1]s — are covered alongside the
// literal table names.
func TestSQLWritesBeadTable(t *testing.T) {
	writes := []string{
		"INSERT INTO issues (id) VALUES (?)",
		"insert into issues (id) values (?)",
		"INSERT IGNORE INTO wisp_labels (issue_id, label) VALUES (?, ?)",
		"REPLACE INTO comments (id) VALUES (?)",
		"UPDATE wisps SET status = ? WHERE id = ?",
		"DELETE FROM dependencies WHERE issue_id = ?",
		"INSERT INTO %s (issue_id, label) VALUES (?, ?)",
		"INSERT INTO %[1]s (parent_id, last_child) VALUES (?, ?)",
		"UPDATE %[2]s SET x = 1 WHERE id = ?",
		"\n\t\tDELETE FROM  wisp_comments\n\t\tWHERE issue_id = ?\n\t",
		"DELETE FROM `issues` WHERE id = ?",
	}
	for _, lit := range writes {
		if !SQLWritesBeadTable(lit) {
			t.Errorf("SQLWritesBeadTable(%q) = false, want true", lit)
		}
	}

	reads := []string{
		"SELECT id FROM issues WHERE id = ?",
		"SELECT COUNT(*) FROM dependencies",
		// Aux tables that are not work-bead state.
		"INSERT INTO events (id) VALUES (?)",
		"DELETE FROM leases WHERE issue_id = ?",
		"UPDATE config SET value = ? WHERE `key` = ?",
		"INSERT INTO bd_events_journal (seq) VALUES (?)",
		// A table whose name merely starts with a bead table's name.
		"INSERT INTO issues_archive (id) VALUES (?)",
	}
	for _, lit := range reads {
		if SQLWritesBeadTable(lit) {
			t.Errorf("SQLWritesBeadTable(%q) = true, want false", lit)
		}
	}
}

// TestCallsOnlyWithLiteralFalse pins the gate predicate the versioned-history
// guard rests on: a call that passes the literal false for a helper's
// mintVersion-style parameter is switched off, and nothing else is. A false
// positive here would drop a real minting edge (a spurious guard failure); a
// false negative would let a composite mutation keep inheriting the mint it
// told its constituents to skip (gastownhall/beads#6358 reviewer Probe 2),
// which is the hole the predicate exists to close.
func TestCallsOnlyWithLiteralFalse(t *testing.T) {
	dir := t.TempDir()
	src := `package probe

func seam() {}

func helper(mintVersion bool) {
	if mintVersion {
		seam()
	}
}

// grouped declares the gate inside a grouped parameter list, so its position
// must be counted per name, not per group.
func grouped(a, b int, recordEvent, mintVersion bool) {
	if mintVersion {
		seam()
	}
}

func SwitchedOff()              { helper(false) }
func SwitchedOffGrouped()       { grouped(1, 2, true, false) }
func SwitchedOn()               { helper(true) }
func Variable()                 { off := false; helper(off) }
func Forwards(mintVersion bool) { helper(mintVersion) }
func Mixed()                    { helper(false); helper(true) }
func MintsItself()              { helper(false); seam() }
func TooShort()                 { helper() }
func NoGate()                   { ungated(false) }
func ungated(other bool)        { seam() }
func NeverCalls()               {}
`
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	fns, err := ParsePackage(dir)
	if err != nil {
		t.Fatalf("ParsePackage: %v", err)
	}

	// The two facts the predicate is built from: declared parameter positions
	// and the reduced argument list of every call.
	if got := fns["grouped"].Params; !reflect.DeepEqual(got, []string{"a", "b", "recordEvent", "mintVersion"}) {
		t.Errorf("grouped.Params = %q, want each name of a grouped parameter list counted separately", got)
	}
	if got := fns["grouped"].ParamIndex("mintVersion"); got != 3 {
		t.Errorf("grouped.ParamIndex(mintVersion) = %d, want 3", got)
	}
	if got := fns["ungated"].ParamIndex("mintVersion"); got != -1 {
		t.Errorf("ungated.ParamIndex(mintVersion) = %d, want -1", got)
	}
	if got := fns["Mixed"].Calls; !reflect.DeepEqual(got, []Call{{Name: "helper", Args: []string{"false"}}, {Name: "helper", Args: []string{"true"}}}) {
		t.Errorf("Mixed.Calls = %+v, want both helper calls with their literal argument", got)
	}

	for _, tc := range []struct {
		caller, callee string
		want           bool
	}{
		{"SwitchedOff", "helper", true},
		{"SwitchedOffGrouped", "grouped", true},
		{"MintsItself", "helper", true}, // the helper edge is off; what MintsItself does on its own is the fixpoint's business
		{"SwitchedOn", "helper", false},
		{"Variable", "helper", false},
		{"Forwards", "helper", false},
		{"Mixed", "helper", false},
		{"TooShort", "helper", false},
		{"NoGate", "ungated", false},
		{"NeverCalls", "helper", false},
		{"SwitchedOff", "missing", false},
	} {
		if got := CallsOnlyWithLiteralFalse(fns, fns[tc.caller], tc.callee, "mintVersion"); got != tc.want {
			t.Errorf("CallsOnlyWithLiteralFalse(%s -> %s) = %v, want %v", tc.caller, tc.callee, got, tc.want)
		}
	}

	// End to end: following only the edges the predicate leaves live credits
	// exactly the callers that can reach the seam, and the predicate is what
	// makes the difference — the plain edge set the journal guard follows
	// still credits the switched-off callers.
	seed := func(f *FuncInfo) bool { return f.CallsAnyOf(map[string]bool{"seam": true}) }
	plain := Fixpoint(fns, seed, func(f *FuncInfo) []string { return f.AllCallNames() })
	gated := Fixpoint(fns, seed, func(f *FuncInfo) []string {
		var live []string
		for _, callee := range f.AllCallNames() {
			if !CallsOnlyWithLiteralFalse(fns, f, callee, "mintVersion") {
				live = append(live, callee)
			}
		}
		return live
	})
	for name, want := range map[string]bool{
		"SwitchedOff": false, "SwitchedOffGrouped": false, "NeverCalls": false, "seam": false,
		"SwitchedOn": true, "Variable": true, "Forwards": true, "Mixed": true, "MintsItself": true,
		"TooShort": true, "NoGate": true, "helper": true, "grouped": true, "ungated": true,
	} {
		if gated[name] != want {
			t.Errorf("gated fixpoint reaches seam via %s = %v, want %v", name, gated[name], want)
		}
	}
	for _, name := range []string{"SwitchedOff", "SwitchedOffGrouped"} {
		if !plain[name] {
			t.Errorf("plain fixpoint does not reach seam via %s; the probe no longer shows the gate doing any work", name)
		}
	}
}
