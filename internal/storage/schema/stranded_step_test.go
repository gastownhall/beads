package schema

import (
	"sort"
	"testing"
)

// The classifier behind the stranded-step finish (bd-0eetsy) decides, from
// two cursor-table snapshots alone, whether the working set is exactly one
// stranded main-series step. These tests pin every conjunct of that shape on
// a synthetic series: they need no database.
func TestRecognizeStrandedMainStep(t *testing.T) {
	series := []migrationFile{
		{version: 47, name: "0047_wisp_tables.up.sql"},
		{version: 48, name: "0048_prior_step.up.sql"},
		{version: 49, name: "0049_next_step.up.sql"},
		{version: 50, name: "0050_after_next.up.sql"},
	}
	// hashes holds the content_hash each cursor row would carry for the
	// synthetic series, keyed as runMigrations records them.
	hashes := map[int]string{
		47: "hash-of-0047",
		48: "hash-of-0048",
		49: "hash-of-0049",
		50: "hash-of-0050",
	}
	// prior is HEAD's cursor: every step through 48 committed.
	prior := map[int]string{
		47: "hash-of-0047",
		48: "hash-of-0048",
	}

	tests := []struct {
		name     string
		series   []migrationFile
		head     map[int]string
		working  map[int]string
		wantVer  int
		wantName string
		wantOK   bool
	}{
		{
			name:    "recognizes the series' next step",
			series:  series,
			head:    prior,
			working: withRow(prior, 49, "hash-of-0049"),
			wantVer: 49, wantName: "0049_next_step.up.sql", wantOK: true,
		},
		{
			// Cursor rows written before #4259 carry NULL content_hash; the
			// compare is NULL-safe ("" equals only "").
			name:    "recognizes over NULL head hashes",
			series:  series,
			head:    map[int]string{47: "", 48: ""},
			working: map[int]string{47: "", 48: "", 49: "hash-of-0049"},
			wantVer: 49, wantName: "0049_next_step.up.sql", wantOK: true,
		},
		{
			// HEAD's set need not be contiguous: recognition keys off HEAD's
			// highest version, not the row count's shape.
			name:    "recognizes over a sparse head",
			series:  series,
			head:    map[int]string{10: "hash-of-0010", 48: "hash-of-0048"},
			working: map[int]string{10: "hash-of-0010", 48: "hash-of-0048", 49: "hash-of-0049"},
			wantVer: 49, wantName: "0049_next_step.up.sql", wantOK: true,
		},
		{
			name:    "declines two stranded rows",
			series:  series,
			head:    prior,
			working: withRows(prior, map[int]string{49: "hash-of-0049", 50: "hash-of-0050"}),
			wantOK:  false,
		},
		{
			name:    "declines a modified head row",
			series:  series,
			head:    prior,
			working: withRows(prior, map[int]string{48: "hash-of-0048-edited", 49: "hash-of-0049"}),
			wantOK:  false,
		},
		{
			name:    "declines a missing head row",
			series:  series,
			head:    prior,
			working: map[int]string{48: "hash-of-0048", 49: "hash-of-0049"},
			wantOK:  false,
		},
		{
			name:    "declines a row that skips the series' next step",
			series:  series,
			head:    prior,
			working: withRow(prior, 50, "hash-of-0050"),
			wantOK:  false,
		},
		{
			name:    "declines a NULL hash on the stranded row",
			series:  series,
			head:    prior,
			working: withRow(prior, 49, ""),
			wantOK:  false,
		},
		{
			name:    "declines a hash matching no shipped file",
			series:  series,
			head:    prior,
			working: withRow(prior, 49, "deadbeef"),
			wantOK:  false,
		},
		{
			name:    "declines when no row was added",
			series:  series,
			head:    prior,
			working: map[int]string{47: "hash-of-0047", 48: "hash-of-0048"},
			wantOK:  false,
		},
		{
			// A row beyond the series' end is not a step this binary ships,
			// even though the shape (HEAD + exactly one row) otherwise fits.
			name:    "declines a version beyond the series' end",
			series:  series[:2],
			head:    prior,
			working: withRow(prior, 99, "hash-of-0099"),
			wantOK:  false,
		},
		{
			name:    "declines on an empty series",
			series:  nil,
			head:    map[int]string{},
			working: map[int]string{1: "hash-of-0001"},
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mf, ok := recognizeStrandedMainStep(tt.head, tt.working, tt.series, hashes)
			if ok != tt.wantOK {
				t.Fatalf("recognizeStrandedMainStep() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if mf.version != tt.wantVer || mf.name != tt.wantName {
				t.Fatalf("recognizeStrandedMainStep() = {%d %s}, want {%d %s}",
					mf.version, mf.name, tt.wantVer, tt.wantName)
			}
		})
	}
}

func withRow(base map[int]string, version int, hash string) map[int]string {
	return withRows(base, map[int]string{version: hash})
}

func withRows(base, extra map[int]string) map[int]string {
	out := make(map[int]string, len(base)+len(extra))
	for version, hash := range base {
		out[version] = hash
	}
	for version, hash := range extra {
		out[version] = hash
	}
	return out
}

// repairWrittenTables records, for each registered main-series pre-repair,
// the committable (non-dolt-ignored) working-set tables the repair writes —
// the tables a stranded step's finish would have to stage for the repair's
// rows to land in HEAD. A repair that only writes dolt-ignored tables (the
// wisp_* family) or no-ops onto already-committed state maps to nil.
//
// The stranded-step finish stages exactly the dirty tables the stranded
// migration's own text touches (strandedStepTouchesTable), so this map must
// stay a subset of what that oracle matches: TestStrandedStepOracleCovers
// RegisteredPreRepairs enforces it. Registering a new repair without an
// entry here fails that test.
var repairWrittenTables = map[int][]string{
	// 40 and 41 write dolt_nonlocal_tables and self-commit that edit through
	// commitNonlocalRepair; their frozen texts also name the table, so the
	// oracle would stage it even without the self-commit.
	40: {"dolt_nonlocal_tables"},
	41: {"dolt_nonlocal_tables"},
	// 47 creates wisps / wisp_dependencies (both dolt-ignored) when missing.
	47: nil,
	// 53 backfills issues (ensureIssuesRigColumns) and dependencies
	// (ensureDependenciesIDColumn); 0053's own body copies into issues,
	// labels, dependencies, events, comments and child_counters.
	53: {"issues", "labels", "dependencies", "events", "comments", "child_counters"},
	// 58 rewrites wisp_dependencies only (dolt-ignored); it reads wisps and
	// issues for existence but never writes them.
	58: nil,
}

// TestStrandedStepOracleCoversRegisteredPreRepairs is the tripwire that keeps
// the stranded-step finish correct as repairs come and go: every registered
// main-series repair must be listed in repairWrittenTables, and every table
// listed there must be matched by the touched-table oracle over that
// migration's frozen text (or planned body). A repair whose writes the oracle
// misses would leave its rows uncommitted in the working set when the pass
// finishes the stranded step it ran before — exactly the residue the finish
// exists to commit.
func TestStrandedStepOracleCoversRegisteredPreRepairs(t *testing.T) {
	series := mainSource.list()
	byVersion := make(map[int]migrationFile, len(series))
	for _, mf := range series {
		byVersion[mf.version] = mf
	}

	versions := make([]int, 0, len(preMigrationRepairs))
	for key := range preMigrationRepairs {
		if key.cursorTable != mainSource.cursorTable {
			// The finish recognizes main-series steps only (it reads
			// mainSource's cursor and files), so an ignored-series repair's
			// writes never pass through it.
			continue
		}
		versions = append(versions, key.version)
	}
	sort.Ints(versions)
	if len(versions) == 0 {
		t.Fatal("no main-series pre-migration repairs registered; the tripwire has nothing to guard")
	}

	for _, version := range versions {
		tables, listed := repairWrittenTables[version]
		if !listed {
			t.Errorf("a pre-migration repair is registered for main version %d but repairWrittenTables has no entry for it: add one listing the committable tables the repair writes (nil when it only writes dolt-ignored tables or no-ops), or a stranded step could leave the repair's rows uncommitted", version)
			continue
		}
		mf, ok := byVersion[version]
		if !ok {
			t.Errorf("a pre-migration repair is registered for main version %d, which the main series does not ship; drop the repair or the entry", version)
			continue
		}
		for _, table := range tables {
			yes, err := strandedStepTouchesTable(mf, table)
			if err != nil {
				t.Errorf("touched-table oracle for %s, table %s: %v", mf.name, table, err)
				continue
			}
			if !yes {
				t.Errorf("the repair for main version %d writes %q, but the stranded-step oracle does not match %q in %s: a stranded %s would leave the repair's rows uncommitted (keep the repair's writes in dolt-ignored tables, self-commit them, or extend the oracle)", version, table, table, mf.name, mf.name)
			}
		}
	}

	for version := range repairWrittenTables {
		if _, ok := preMigrationRepairs[repairKey{mainSource.cursorTable, version}]; !ok {
			t.Errorf("repairWrittenTables lists main version %d but no repair is registered for it; drop the entry", version)
		}
	}
}
