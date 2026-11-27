package schema

import (
	"database/sql"
	"maps"
	"slices"
	"testing"
)

// The recognition behind finishStrandedMainStep was first written by
// @bee-ghosttrack in PR #8603; these cases pin its DB-free core.

func TestClassifyStrandedStep(t *testing.T) {
	series := []migrationFile{
		{version: 47, name: "0047_x.up.sql"},
		{version: 48, name: "0048_x.up.sql"},
		{version: 49, name: "0049_x.up.sql"},
		{version: 50, name: "0050_x.up.sql"},
	}
	hashOf := func(mf migrationFile) (string, error) { return "hash-" + mf.name, nil }
	h := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	head := map[int]sql.NullString{47: h("hash-0047_x.up.sql"), 48: {}}
	with := func(extra map[int]sql.NullString) map[int]sql.NullString {
		out := maps.Clone(head)
		maps.Copy(out, extra)
		return out
	}

	for _, tc := range []struct {
		name    string
		working map[int]sql.NullString
		want    int // 0: declined
	}{
		{"next step with its file hash", with(map[int]sql.NullString{49: h("hash-0049_x.up.sql")}), 49},
		{"no new row", with(nil), 0},
		{"two new rows", with(map[int]sql.NullString{49: h("hash-0049_x.up.sql"), 50: h("hash-0050_x.up.sql")}), 0},
		{"skips a version", with(map[int]sql.NullString{50: h("hash-0050_x.up.sql")}), 0},
		{"null hash", with(map[int]sql.NullString{49: {}}), 0},
		{"another file's hash", with(map[int]sql.NullString{49: h("hash-0050_x.up.sql")}), 0},
		{"HEAD row hash changed", with(map[int]sql.NullString{47: h("other"), 49: h("hash-0049_x.up.sql")}), 0},
		{"HEAD null hash filled in", with(map[int]sql.NullString{48: h("hash-0048_x.up.sql"), 49: h("hash-0049_x.up.sql")}), 0},
		{"HEAD row replaced by the step", map[int]sql.NullString{47: h("hash-0047_x.up.sql"), 49: h("hash-0049_x.up.sql"), 50: h("hash-0050_x.up.sql")}, 0},
		{"past the series end", map[int]sql.NullString{47: h("a"), 48: h("b"), 49: h("c"), 50: h("d"), 51: h("e")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := head
			if tc.name == "past the series end" {
				h = map[int]sql.NullString{47: tc.working[47], 48: tc.working[48], 49: tc.working[49], 50: tc.working[50]}
			}
			mf, ok, err := classifyStrandedStep(h, tc.working, series, hashOf)
			if err != nil {
				t.Fatalf("classifyStrandedStep: %v", err)
			}
			if got := map[bool]int{true: mf.version, false: 0}[ok]; got != tc.want {
				t.Fatalf("classifyStrandedStep = (%d, %v), want %d", mf.version, ok, tc.want)
			}
		})
	}
}

func TestStrandedStepTouchedTables(t *testing.T) {
	file := func(version int) migrationFile {
		t.Helper()
		for _, mf := range mainSource.list() {
			if mf.version == version {
				return mf
			}
		}
		t.Fatalf("no main migration %d", version)
		return migrationFile{}
	}
	for _, tc := range []struct {
		version int
		table   string
		want    bool
	}{
		{49, "issues", true},
		{49, "comments", true},
		{49, "config", false},
		{49, "labels", false},
		{62, "dolt_ignore", true},
		{62, "events", true},
		{73, "issues", true},
		{73, "labels", false},
	} {
		got, err := mainSource.stepTouchesTable(file(tc.version), tc.table)
		if err != nil {
			t.Fatalf("stepTouchesTable(%d, %s): %v", tc.version, tc.table, err)
		}
		if got != tc.want {
			t.Errorf("stepTouchesTable(%d, %s) = %v, want %v", tc.version, tc.table, got, tc.want)
		}
	}
}

// TestStrandedStepPreRepairsNeedNoTouchedTables is a tripwire. A stranded
// step is finished by the tables its migration SQL (or registered body plan)
// touches. A pre-repair runs before that SQL and can write tables of its
// own; each one registered when the finish was written either commits its
// own writes, writes only tables its migration's SQL also touches, or writes
// nothing a cursor row could outlive. A new registration must be checked the
// same way, and stepTouchesTable widened if its writes would be left behind.
func TestStrandedStepPreRepairsNeedNoTouchedTables(t *testing.T) {
	var got []int
	for key := range preMigrationRepairs {
		if key.cursorTable == mainSource.cursorTable {
			got = append(got, key.version)
		}
	}
	slices.Sort(got)
	if want := []int{40, 41, 47, 53, 58}; !slices.Equal(got, want) {
		t.Fatalf("main pre-migration repairs = %v, want %v; check the new repair's writes against stepTouchesTable", got, want)
	}
}
