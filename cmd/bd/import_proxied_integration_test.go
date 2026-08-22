//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// importFixtureJSONL builds an export-shaped JSONL stream: an optional
// header line, issue rows marshalled from types.Issue (so the fixture cannot
// drift from the parser's schema), a memory record and a tombstone row the
// importer must skip.
func importFixtureJSONL(t *testing.T, issues []*types.Issue, extraLines ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"_schema":"beads-jsonl/1","_sort":"stable-v1"}` + "\n")
	for _, issue := range issues {
		line, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("marshal fixture issue %s: %v", issue.ID, err)
		}
		b.Write(line)
		b.WriteString("\n")
	}
	for _, line := range extraLines {
		b.WriteString(line + "\n")
	}
	return b.String()
}

// bdProxiedImport runs `bd import <args>` and returns the stderr report
// (import writes its human report to stderr), failing the test on error.
func bdProxiedImport(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"import"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd import %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stderr
}

func bdProxiedImportWithInput(t *testing.T, bd, dir, input string, args ...string) (string, string, error) {
	t.Helper()
	fullArgs := append([]string{"import"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	cmd.Stdin = strings.NewReader(input)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String(), stderr.String(), err
}

func proxiedImportQueryInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func proxiedImportQueryString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s
}

func TestProxiedServerImport(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	when := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// fixtureIssues returns the canonical three-row batch: one row carrying
	// labels and a comment, one carrying a blocks edge onto the first, one
	// plain row — enough shape to prove the aux data actually lands.
	fixtureIssues := func(prefix string) []*types.Issue {
		return []*types.Issue{
			{
				ID: prefix + "-r1", Title: "Round-trip one", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2,
				Labels:    []string{"lane:test", "imported"},
				Comments:  []*types.Comment{{ID: prefix + "-r1-c1", Author: "fixture", Text: "carried comment", CreatedAt: when}},
				CreatedAt: when, UpdatedAt: when,
			},
			{
				ID: prefix + "-r2", Title: "Round-trip two", Status: types.StatusOpen,
				IssueType: types.TypeBug, Priority: 1,
				Dependencies: []*types.Dependency{{IssueID: prefix + "-r2", DependsOnID: prefix + "-r1", Type: types.DepBlocks}},
				CreatedAt:    when, UpdatedAt: when,
			},
			{
				ID: prefix + "-r3", Title: "Round-trip three", Status: types.StatusClosed,
				IssueType: types.TypeChore, Priority: 3, CloseReason: "done in fixture",
				CreatedAt: when, UpdatedAt: when,
			},
		}
	}

	// A workspace WITH hooks is the case that broke: proxied mode wraps its
	// provider so writes fire the workspace's hook scripts, and the import role
	// asks the unit of work for its raw statement runner (importer.go) — an
	// assertion on the concrete type, which the wrapper is not. Every import in
	// a hooks-enabled proxied workspace failed on it.
	//
	// The import itself still fires no hook, on either plumbing: both run the
	// shared batch-upsert engine rather than the per-issue verbs.
	t.Run("import_runs_in_a_workspace_with_hooks", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("hook script form is POSIX shell")
		}
		marker := filepath.Join(t.TempDir(), "any_hook_marker")
		script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(marker) + "\n"
		p := newSharedProxiedProjectWithHooks(t, bd, "imph", map[string]string{
			"on_create": script,
			"on_update": script,
			"on_close":  script,
		})
		db := openProxiedDB(t, p)

		path := filepath.Join(p.dir, "hooked.jsonl")
		if err := os.WriteFile(path, []byte(importFixtureJSONL(t, fixtureIssues("imph"))), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		report := bdProxiedImport(t, bd, p.dir, path)
		if !strings.Contains(report, "Imported 3 issues") {
			t.Errorf("import report = %q, want 'Imported 3 issues'", report)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id LIKE 'imph-r%'"); got != 3 {
			t.Errorf("issue rows = %d, want 3", got)
		}
		if data, err := os.ReadFile(marker); err == nil {
			t.Errorf("import fired hooks: %q", string(data))
		}
	})

	t.Run("roundtrip_one_commit_with_content", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impa")
		db := openProxiedDB(t, p)

		fixture := importFixtureJSONL(t, fixtureIssues("impa"),
			`{"_type":"memory","key":"import-probe","value":"remembered"}`,
			`{"id":"impa-dead","title":"Tombstoned","status":"tombstone"}`,
		)
		path := filepath.Join(p.dir, "roundtrip.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		head := proxiedDoltHead(t, db)
		report := bdProxiedImport(t, bd, p.dir, path)
		if !strings.Contains(report, "Imported 3 issues and 1 memories") {
			t.Errorf("import report = %q, want 'Imported 3 issues and 1 memories'", report)
		}

		// ONE commit for the whole invocation: rows, aux data and the memory
		// together. The proxied path has no PostRun auto-commit, so this is
		// the Importer capability's single DOLT_COMMIT.
		if n := proxiedDoltCommitCountSince(t, db, head); n != 1 {
			t.Errorf("commits for one import = %d, want exactly 1", n)
		}

		// Direct content assertions — presence of the aux data, not just row
		// counts or byte-equality.
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id LIKE 'impa-r%'"); got != 3 {
			t.Errorf("issue rows = %d, want 3", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM labels WHERE issue_id = 'impa-r1'"); got != 2 {
			t.Errorf("impa-r1 labels = %d, want 2", got)
		}
		if got := proxiedImportQueryString(t, db, "SELECT text FROM comments WHERE issue_id = 'impa-r1'"); got != "carried comment" {
			t.Errorf("impa-r1 comment = %q, want the carried comment", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM dependencies WHERE issue_id = 'impa-r2' AND depends_on_issue_id = 'impa-r1' AND type = 'blocks'"); got != 1 {
			t.Errorf("impa-r2 blocks edge = %d, want 1", got)
		}
		if got := proxiedImportQueryString(t, db, "SELECT value FROM config WHERE `key` = 'kv.memory.import-probe'"); got != "remembered" {
			t.Errorf("memory value = %q, want %q", got, "remembered")
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id = 'impa-dead'"); got != 0 {
			t.Errorf("tombstone row imported: %d rows, want 0", got)
		}
		if got := proxiedImportQueryString(t, db, "SELECT status FROM issues WHERE id = 'impa-r3'"); got != "closed" {
			t.Errorf("impa-r3 status = %q, want closed", got)
		}

		// bd's own view agrees with the raw oracle.
		shown := bdProxiedShow(t, bd, p.dir, "impa-r1")
		if shown.Title != "Round-trip one" || shown.Priority != 2 {
			t.Errorf("show impa-r1 = title %q priority %d, want the imported row", shown.Title, shown.Priority)
		}

		// Re-import of the identical snapshot converges: no duplicated aux
		// data, and nothing new to commit.
		headBeforeReimport := proxiedDoltHead(t, db)
		_ = bdProxiedImport(t, bd, p.dir, path)
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM labels WHERE issue_id = 'impa-r1'"); got != 2 {
			t.Errorf("labels after re-import = %d, want 2 (idempotent merge)", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM comments WHERE issue_id = 'impa-r1'"); got != 1 {
			t.Errorf("comments after re-import = %d, want 1 (idempotent merge)", got)
		}
		if n := proxiedDoltCommitCountSince(t, db, headBeforeReimport); n != 0 {
			t.Errorf("re-import of an identical snapshot made %d commits, want 0 (working-set no-op)", n)
		}
	})

	// bd-r9uce: import routes the storage plane by the export stream's
	// explicit "wisp_plane" marker, never by the no_history flag. A no_history=true
	// record WITHOUT the marker is a promoted no-history wisp — a durable
	// issues-table row whose stray flag must not re-plane it into the wisps
	// table (which would drop its cross-plane relations as "cross-bucket");
	// WITH the marker it is a genuine unpromoted no-history wisp and keeps
	// its wisps-plane home. Same marker contract as the classic route
	// (TestEmbeddedImportPromotedWispRoundtrip), through the shared parse
	// loop and the shared issueops batch engine.
	t.Run("promoted_no_history_routes_durable", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impw")
		db := openProxiedDB(t, p)

		fixture := importFixtureJSONL(t, []*types.Issue{
			{
				ID: "impw-wisp-promo", Title: "Promoted no-history wisp", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2, NoHistory: true,
				CreatedAt: when, UpdatedAt: when,
			},
			{
				ID: "impw-frend", Title: "Durable friend", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2,
				Dependencies: []*types.Dependency{{IssueID: "impw-frend", DependsOnID: "impw-wisp-promo", Type: types.DepBlocks}},
				CreatedAt:    when, UpdatedAt: when,
			},
		},
			// A genuine unpromoted no-history wisp: same flags, but carrying
			// the explicit plane marker.
			`{"id":"impw-wisp-real","title":"Real no-history wisp","status":"open","issue_type":"task","priority":2,"no_history":true,"wisp_plane":true,"created_at":"2026-08-01T12:00:00Z","updated_at":"2026-08-01T12:00:00Z"}`,
		)
		path := filepath.Join(p.dir, "planes.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		report := bdProxiedImport(t, bd, p.dir, path)
		if strings.Contains(report, "Skipped dependency") {
			t.Errorf("import dropped a relation (promoted row re-planed into the wisps bucket?):\n%s", report)
		}

		// Marker absent => durable plane, flag preserved on the row.
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id = 'impw-wisp-promo' AND no_history = 1"); got != 1 {
			t.Errorf("promoted row in issues table with no_history intact = %d, want 1", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM wisps WHERE id = 'impw-wisp-promo'"); got != 0 {
			t.Errorf("promoted row re-planed into wisps table: %d rows, want 0", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM dependencies WHERE issue_id = 'impw-frend' AND depends_on_issue_id = 'impw-wisp-promo' AND type = 'blocks'"); got != 1 {
			t.Errorf("friend blocks edge onto promoted row = %d, want 1 (relation dropped?)", got)
		}

		// Marker present => wisps plane.
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM wisps WHERE id = 'impw-wisp-real' AND no_history = 1"); got != 1 {
			t.Errorf("marked no-history wisp in wisps table = %d, want 1", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id = 'impw-wisp-real'"); got != 0 {
			t.Errorf("marked no-history wisp leaked into issues table: %d rows, want 0", got)
		}
	})

	t.Run("stdin_dash_and_redirect_guard", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impb")
		db := openProxiedDB(t, p)

		fixture := importFixtureJSONL(t, []*types.Issue{{
			ID: "impb-s1", Title: "From stdin", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2, CreatedAt: when, UpdatedAt: when,
		}})

		// bd import - reads the piped stream.
		_, stderr, err := bdProxiedImportWithInput(t, bd, p.dir, fixture, "-")
		if err != nil {
			t.Fatalf("bd import -: %v\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "Imported 1 issues") {
			t.Errorf("stdin import report = %q, want 'Imported 1 issues'", stderr)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id = 'impb-s1'"); got != 1 {
			t.Errorf("stdin row = %d, want 1", got)
		}

		// Redirected stdin WITHOUT "-" must refuse rather than silently
		// importing the default JSONL (bd-axluy).
		_, stderr, err = bdProxiedImportWithInput(t, bd, p.dir, fixture)
		if err == nil {
			t.Fatal("bd import with redirected stdin and no \"-\" should refuse")
		}
		if !strings.Contains(stderr, "use 'bd import -'") {
			t.Errorf("redirect-guard message = %q, want the bd-axluy hint", stderr)
		}
	})

	t.Run("upsert_updates_and_stale_guard", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impc")
		db := openProxiedDB(t, p)

		// Seed a live row through the ordinary create door.
		created := bdProxiedCreate(t, bd, p.dir, "Live local row", "--type", "task", "--priority", "2")

		// An older snapshot of that row is stale-skipped: local state wins.
		old := when.Add(-30 * 24 * time.Hour)
		staleFixture := importFixtureJSONL(t, []*types.Issue{{
			ID: created.ID, Title: "Older snapshot title", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 4, CreatedAt: old, UpdatedAt: old,
		}})
		stalePath := filepath.Join(p.dir, "stale.jsonl")
		if err := os.WriteFile(stalePath, []byte(staleFixture), 0o644); err != nil {
			t.Fatalf("write stale fixture: %v", err)
		}
		report := bdProxiedImport(t, bd, p.dir, stalePath)
		if !strings.Contains(report, "stale skipped") {
			t.Errorf("stale import report = %q, want a stale-skipped notice", report)
		}
		if got := proxiedImportQueryString(t, db, "SELECT title FROM issues WHERE id = ?", created.ID); got != "Live local row" {
			t.Errorf("title after stale import = %q, want local row kept", got)
		}

		// --allow-stale deliberately restores the older snapshot.
		_ = bdProxiedImport(t, bd, p.dir, "--allow-stale", stalePath)
		if got := proxiedImportQueryString(t, db, "SELECT title FROM issues WHERE id = ?", created.ID); got != "Older snapshot title" {
			t.Errorf("title after --allow-stale = %q, want the older snapshot restored", got)
		}

		// A strictly-newer row upserts and the report names the change.
		newer := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		newerFixture := importFixtureJSONL(t, []*types.Issue{{
			ID: created.ID, Title: "Newer imported title", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 1, CreatedAt: old, UpdatedAt: newer,
		}})
		newerPath := filepath.Join(p.dir, "newer.jsonl")
		if err := os.WriteFile(newerPath, []byte(newerFixture), 0o644); err != nil {
			t.Fatalf("write newer fixture: %v", err)
		}
		report = bdProxiedImport(t, bd, p.dir, newerPath)
		if !strings.Contains(report, "Updated 1 existing issue(s)") {
			t.Errorf("newer import report = %q, want the updated-issues summary", report)
		}
		if got := proxiedImportQueryString(t, db, "SELECT title FROM issues WHERE id = ?", created.ID); got != "Newer imported title" {
			t.Errorf("title after newer import = %q, want the imported rewrite", got)
		}
	})

	t.Run("dry_run_classifies_and_writes_nothing", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impd")
		db := openProxiedDB(t, p)

		fixture := importFixtureJSONL(t, fixtureIssues("impd"),
			`{"_type":"memory","key":"dry-probe","value":"never"}`,
		)
		path := filepath.Join(p.dir, "dry.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		head := proxiedDoltHead(t, db)
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "import", "--dry-run", "--json", path)
		if err != nil {
			t.Fatalf("dry-run import: %v\n%s", err, stdout)
		}
		var report struct {
			Created int  `json:"created"`
			DryRun  bool `json:"dry_run"`
		}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("no JSON in dry-run output: %s", stdout)
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &report); err != nil {
			t.Fatalf("parse dry-run JSON: %v\n%s", err, stdout)
		}
		if !report.DryRun || report.Created != 3 {
			t.Errorf("dry-run report = %+v, want dry_run=true created=3", report)
		}
		if n := proxiedDoltCommitCountSince(t, db, head); n != 0 {
			t.Errorf("dry run made %d commits, want 0", n)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id LIKE 'impd-r%'"); got != 0 {
			t.Errorf("dry run wrote %d rows, want 0", got)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM config WHERE `key` = 'kv.memory.dry-probe'"); got != 0 {
			t.Errorf("dry run wrote the memory, want none")
		}
	})

	t.Run("dedup_skips_matching_open_title", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impe")
		db := openProxiedDB(t, p)

		bdProxiedCreate(t, bd, p.dir, "Duplicate title", "--type", "task")

		fixture := importFixtureJSONL(t, []*types.Issue{
			{
				ID: "impe-d1", Title: "Duplicate title", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2, CreatedAt: when, UpdatedAt: when,
			},
			{
				ID: "impe-d2", Title: "Fresh title", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2, CreatedAt: when, UpdatedAt: when,
			},
		})
		path := filepath.Join(p.dir, "dedup.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		report := bdProxiedImport(t, bd, p.dir, "--dedup", path)
		if !strings.Contains(report, "(1 duplicates skipped)") {
			t.Errorf("dedup report = %q, want '(1 duplicates skipped)'", report)
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id = 'impe-d1'"); got != 0 {
			t.Errorf("duplicate-titled row imported, want skipped")
		}
		if got := proxiedImportQueryInt(t, db, "SELECT COUNT(*) FROM issues WHERE id = 'impe-d2'"); got != 1 {
			t.Errorf("fresh-titled row missing, want imported")
		}
	})

	t.Run("import_is_the_sanctioned_upsert_door", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "impf")
		db := openProxiedDB(t, p)

		created := bdProxiedCreate(t, bd, p.dir, "Occupied id", "--type", "task")

		// Import IS the sanctioned upsert door: the occupied ID upserts.
		newer := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		fixture := importFixtureJSONL(t, []*types.Issue{{
			ID: created.ID, Title: "Upserted through the door", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2, CreatedAt: when, UpdatedAt: newer,
		}})
		path := filepath.Join(p.dir, "door.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		_ = bdProxiedImport(t, bd, p.dir, path)
		if got := proxiedImportQueryString(t, db, "SELECT title FROM issues WHERE id = ?", created.ID); got != "Upserted through the door" {
			t.Errorf("title after upsert = %q, want the imported rewrite", got)
		}
	})
}
