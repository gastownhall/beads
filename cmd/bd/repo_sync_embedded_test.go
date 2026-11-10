//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedRepoSyncSkipsAnEdgeAcrossTwoReposHierarchy: repo b's issue
// b-kid1 is a grandchild of repo c's c-grand1 and also blocks on it. Repo b's
// own database never holds c-grand1, so it filed that edge as an unverified
// external ID and never checked it, and b's export carries it. Here both
// repos are hydrated, so the edge would block an issue on its own ancestor, a
// gate the write path refuses (GH#8299). `bd repo sync` skips and reports
// that one edge and hydrates the rest of b, whichever repo it syncs first and
// on every later sync; it no longer drops the whole repository.
func TestEmbeddedRepoSyncSkipsAnEdgeAcrossTwoReposHierarchy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)

	const stamps = `"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"`
	for _, tc := range []struct {
		name          string
		ancestorFirst bool
	}{
		{"ancestor repo first", true},
		{"dependent repo first", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "a")
			repoC := writeRepoSyncExport(t,
				`{"id":"c-grand1","title":"Grand","status":"open","priority":2,"issue_type":"epic",`+stamps+`}`)
			repoB := writeRepoSyncExport(t,
				`{"id":"b-par1","title":"Parent","status":"open","priority":2,"issue_type":"task",`+stamps+
					`,"dependencies":[{"issue_id":"b-par1","depends_on_id":"c-grand1","type":"parent-child"}]}`,
				`{"id":"b-kid1","title":"Kid","status":"open","priority":2,"issue_type":"task",`+stamps+
					`,"dependencies":[{"issue_id":"b-kid1","depends_on_id":"b-par1","type":"parent-child"},`+
					`{"issue_id":"b-kid1","depends_on_id":"c-grand1","type":"blocks"}]}`,
				`{"id":"b-other1","title":"Other","status":"open","priority":2,"issue_type":"task",`+stamps+`}`)
			repos := []string{repoB, repoC}
			if tc.ancestorFirst {
				repos = []string{repoC, repoB}
			}
			for _, repo := range repos {
				bdRunSplit(t, bd, dir, "repo", "add", repo)
			}

			// The second sync re-reads b's changed export: with c already
			// hydrated, the edge is checked there whichever repo came first.
			for sync := 1; sync <= 2; sync++ {
				if sync == 2 {
					later := time.Now().Add(time.Hour)
					if err := os.Chtimes(filepath.Join(repoB, ".beads", "issues.jsonl"), later, later); err != nil {
						t.Fatalf("touch repo b's export: %v", err)
					}
				}
				_, stderr := bdRunSplit(t, bd, dir, "repo", "sync")
				if strings.Contains(stderr, "failed to import") {
					t.Fatalf("sync %d dropped a repository:\n%s", sync, stderr)
				}
				held := embeddedIssueIDs(t, beadsDir)
				for _, id := range []string{"c-grand1", "b-par1", "b-kid1", "b-other1"} {
					if !held[id] {
						t.Errorf("sync %d: %s not hydrated\nstderr:\n%s", sync, id, stderr)
					}
				}
				refusalChecked := sync == 2 || tc.ancestorFirst
				if want := "skipped dependency b-kid1 -> c-grand1: b-kid1 cannot be blocked by its ancestor c-grand1"; refusalChecked && !strings.Contains(stderr, want) {
					t.Errorf("sync %d: stderr does not report the skipped edge (%q):\n%s", sync, want, stderr)
				}
			}

			for _, edge := range [][2]string{{"b-par1", "c-grand1"}, {"b-kid1", "b-par1"}} {
				issueCol, _, _, found := embeddedDepTargetColumns(t, beadsDir, edge[0], edge[1])
				if !found || issueCol.String != edge[1] {
					t.Errorf("parent-child edge %s -> %s: found=%v depends_on_issue_id=%v, want it local", edge[0], edge[1], found, issueCol)
				}
			}
			// The refused edge is never local: skipped when c came first, kept
			// external (where it gates nothing) when b's first sync stored it
			// before c-grand1 was here.
			if issueCol, _, _, _ := embeddedDepTargetColumns(t, beadsDir, "b-kid1", "c-grand1"); issueCol.Valid {
				t.Errorf("blocking edge b-kid1 -> c-grand1 filed local onto b-kid1's own ancestor")
			}
		})
	}
}

// writeRepoSyncExport returns a new beads repository directory whose
// .beads/issues.jsonl holds lines, ready for `bd repo add`.
func writeRepoSyncExport(t *testing.T, lines ...string) string {
	t.Helper()
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	return repo
}

// embeddedIssueIDs reads the id of every issues row of the embedded store.
func embeddedIssueIDs(t *testing.T, beadsDir string) map[string]bool {
	t.Helper()
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	rows, err := db.QueryContext(t.Context(), "SELECT id FROM issues")
	if err != nil {
		t.Fatalf("read issues: %v", err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan issue id: %v", err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read issues: %v", err)
	}
	return ids
}
