//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// strictDepsImport runs `bd import <args>` in dir and returns stdout, stderr
// and the process exit code, without failing on a nonzero exit.
func strictDepsImport(t *testing.T, bd, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"import"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("bd import %s: %v", strings.Join(args, " "), err)
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// strictDepsRows returns the ids of the issue rows and the "from -> to"
// dependency edges stored under prefix, both sorted, read straight from the
// embedded database rather than through bd.
func strictDepsRows(t *testing.T, beadsDir, prefix string) (ids, edges []string) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), prefix, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	rows, err := db.QueryContext(t.Context(), "SELECT id FROM issues WHERE id LIKE ?", prefix+"-%")
	if err != nil {
		t.Fatalf("query issues: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan issue: %v", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()

	rows, err = db.QueryContext(t.Context(),
		"SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) FROM dependencies WHERE issue_id LIKE ?",
		prefix+"-%")
	if err != nil {
		t.Fatalf("query dependencies: %v", err)
	}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			t.Fatalf("scan dependency: %v", err)
		}
		edges = append(edges, from+" -> "+to)
	}
	_ = rows.Close()
	sort.Strings(ids)
	sort.Strings(edges)
	return ids, edges
}

// TestEmbeddedImportStrictDeps covers the skipped-dependency signal of a
// split import (GH#6113) end to end through the classic import route: a part
// carrying an edge whose target lands in a later part commits its rows and
// skips the edge; by default it says so with a count and exits 0, and with
// --strict-deps it exits 1 so a restore script can gate on it.
func TestEmbeddedImportStrictDeps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	when := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	issue := func(id string, dependsOn ...string) types.Issue {
		row := types.Issue{
			ID: id, Title: "Row " + id, Status: types.StatusOpen, IssueType: types.TypeTask,
			CreatedAt: when, UpdatedAt: when,
		}
		for _, target := range dependsOn {
			row.Dependencies = append(row.Dependencies, &types.Dependency{
				IssueID: id, DependsOnID: target, Type: types.DepBlocks, CreatedAt: when,
			})
		}
		return row
	}
	// partA carries prefix-a, whose blocks edge targets prefix-b, which only
	// the later part carries.
	partA := func(t *testing.T, prefix string) string {
		path := filepath.Join(t.TempDir(), "part-a.jsonl")
		writeJSONLFile(t, path, []types.Issue{issue(prefix+"-a", prefix+"-b")})
		return path
	}

	t.Run("default_reports_count_and_exits_zero", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sddef")
		path := partA(t, "sddef")

		stdout, stderr, code := strictDepsImport(t, bd, dir, path)
		if code != 0 {
			t.Fatalf("default import exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "Skipped dependency: sddef-a -> sddef-b: target not found") {
			t.Errorf("stderr lacks the per-edge report:\n%s", stderr)
		}
		if got := strings.Count(stderr, "Warning: 1 dependency edge(s) were reported skipped"); got != 1 {
			t.Errorf("stderr carries the skipped-count summary %d times, want 1:\n%s", got, stderr)
		}
		if !strings.Contains(stderr, "Hint: ") {
			t.Errorf("stderr lacks the recovery hint:\n%s", stderr)
		}
		ids, edges := strictDepsRows(t, beadsDir, "sddef")
		if strings.Join(ids, ",") != "sddef-a" || len(edges) != 0 {
			t.Errorf("after default import rows = %v edges = %v, want [sddef-a] and no edge", ids, edges)
		}

		// --json: the existing single result object on stdout, and no new
		// diagnostic on stderr.
		stdout, stderr, code = strictDepsImport(t, bd, dir, "--json", path)
		if code != 0 {
			t.Fatalf("default --json import exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		obj := decodeSingleJSONObject(t, stdout)
		if deps, _ := obj["skipped_dependencies"].([]any); len(deps) != 1 {
			t.Errorf("skipped_dependencies = %v, want the one skipped edge", obj["skipped_dependencies"])
		}
		if strings.Contains(stderr, "dependency edge(s)") || strings.Contains(stderr, "Hint:") {
			t.Errorf("default --json import added a stderr diagnostic:\n%s", stderr)
		}
	})

	t.Run("strict_text_exits_one_rows_committed", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sdtxt")

		stdout, stderr, code := strictDepsImport(t, bd, dir, "--strict-deps", partA(t, "sdtxt"))
		if code != 1 {
			t.Fatalf("--strict-deps import exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "Skipped dependency: sdtxt-a -> sdtxt-b: target not found") {
			t.Errorf("stderr lacks the per-edge report:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Error: --strict-deps: 1 dependency edge(s) were reported skipped; imported rows remain committed") {
			t.Errorf("stderr lacks the strict-deps error:\n%s", stderr)
		}
		// The failure is the verdict on a committed import, not a refused
		// flag: the source row landed, only the edge is missing.
		ids, edges := strictDepsRows(t, beadsDir, "sdtxt")
		if strings.Join(ids, ",") != "sdtxt-a" || len(edges) != 0 {
			t.Errorf("after --strict-deps import rows = %v edges = %v, want [sdtxt-a] and no edge", ids, edges)
		}
	})

	t.Run("strict_json_exits_one_one_result_object", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sdjsn")

		stdout, stderr, code := strictDepsImport(t, bd, dir, "--json", "--strict-deps", partA(t, "sdjsn"))
		if code != 1 {
			t.Fatalf("--json --strict-deps import exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		obj := decodeSingleJSONObject(t, stdout)
		deps, _ := obj["skipped_dependencies"].([]any)
		if len(deps) != 1 || deps[0] != "sdjsn-a -> sdjsn-b: target not found" {
			t.Errorf("skipped_dependencies = %v, want the one skipped edge", obj["skipped_dependencies"])
		}
		if created, _ := obj["created"].(float64); created != 1 {
			t.Errorf("created = %v, want 1", obj["created"])
		}
		msg := decodeStrictDepsJSONError(t, stderr)
		if !strings.Contains(msg["error"], "1 dependency edge(s)") || !strings.Contains(msg["error"], "imported rows remain committed") {
			t.Errorf("stderr error = %q, want the count and the committed-rows note", msg["error"])
		}
		if !strings.Contains(msg["hint"], "re-run") {
			t.Errorf("stderr hint = %q, want the recovery hint", msg["hint"])
		}
		ids, edges := strictDepsRows(t, beadsDir, "sdjsn")
		if strings.Join(ids, ",") != "sdjsn-a" || len(edges) != 0 {
			t.Errorf("after --json --strict-deps import rows = %v edges = %v, want [sdjsn-a] and no edge", ids, edges)
		}
	})

	t.Run("strict_with_available_targets_exits_zero", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sdok")

		// sdok-db is already in the database; sdok-x targets it, and sdok-y
		// targets sdok-x in the same input.
		first := filepath.Join(t.TempDir(), "first.jsonl")
		writeJSONLFile(t, first, []types.Issue{issue("sdok-db")})
		if stdout, stderr, code := strictDepsImport(t, bd, dir, "--strict-deps", first); code != 0 {
			t.Fatalf("seed import exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		second := filepath.Join(t.TempDir(), "second.jsonl")
		writeJSONLFile(t, second, []types.Issue{issue("sdok-y", "sdok-x"), issue("sdok-x", "sdok-db")})

		for _, args := range [][]string{{"--strict-deps", second}, {"--json", "--strict-deps", second}} {
			stdout, stderr, code := strictDepsImport(t, bd, dir, args...)
			if code != 0 {
				t.Fatalf("bd import %v exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
			}
			for _, unwanted := range []string{"Skipped dependency", "dependency edge(s)", "Error:", "Hint:", `"error"`} {
				if strings.Contains(stderr, unwanted) || strings.Contains(stdout, unwanted) {
					t.Errorf("bd import %v reported %q on a clean import\nstdout:\n%s\nstderr:\n%s", args, unwanted, stdout, stderr)
				}
			}
		}
		_, edges := strictDepsRows(t, beadsDir, "sdok")
		if want := "sdok-x -> sdok-db,sdok-y -> sdok-x"; strings.Join(edges, ",") != want {
			t.Errorf("edges = %v, want %s", edges, want)
		}
	})

	// The split restore of GH#6113: part A carries A -> B, part B carries B.
	// Strict-importing A first fails (the gate a restore script needs);
	// importing B, then strict-importing A again, wires the edge exactly once.
	t.Run("split_restore_rerun_converges", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sdspl")
		pathA := partA(t, "sdspl")
		pathB := filepath.Join(t.TempDir(), "part-b.jsonl")
		writeJSONLFile(t, pathB, []types.Issue{issue("sdspl-b")})

		if stdout, stderr, code := strictDepsImport(t, bd, dir, "--strict-deps", pathA); code != 1 {
			t.Fatalf("first strict import of part A exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if ids, _ := strictDepsRows(t, beadsDir, "sdspl"); strings.Join(ids, ",") != "sdspl-a" {
			t.Fatalf("after part A rows = %v, want [sdspl-a] committed", ids)
		}
		if stdout, stderr, code := strictDepsImport(t, bd, dir, pathB); code != 0 {
			t.Fatalf("import of part B exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		stdout, stderr, code := strictDepsImport(t, bd, dir, "--strict-deps", pathA)
		if code != 0 {
			t.Fatalf("strict re-import of part A exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if strings.Contains(stderr, "Skipped dependency") {
			t.Errorf("re-import still skipped an edge:\n%s", stderr)
		}
		ids, edges := strictDepsRows(t, beadsDir, "sdspl")
		if strings.Join(ids, ",") != "sdspl-a,sdspl-b" {
			t.Errorf("rows = %v, want [sdspl-a sdspl-b]", ids)
		}
		if want := "sdspl-a -> sdspl-b"; strings.Join(edges, ",") != want {
			t.Errorf("edges = %v, want exactly [%s] (no duplicate)", edges, want)
		}
	})
}
