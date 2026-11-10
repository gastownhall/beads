//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// stateAtomicityCount runs one COUNT query against an embedded workspace's
// database, between bd invocations.
func stateAtomicityCount(t *testing.T, beadsDir, query string, args ...any) int {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var n int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// stateAtomicityRows is what a set-state writes, counted at the tables: event
// rows on both planes, parent-child edges into the subject on both planes, the
// subject's label rows on both planes, and its patrol value as bd state reads
// it.
type stateAtomicityRows struct {
	events, edges, labels int
	patrol                string
}

func readStateAtomicityRows(t *testing.T, bd, dir, beadsDir, subject string) stateAtomicityRows {
	t.Helper()
	return stateAtomicityRows{
		events: stateAtomicityCount(t, beadsDir, `SELECT
			(SELECT COUNT(*) FROM issues WHERE issue_type = 'event') +
			(SELECT COUNT(*) FROM wisps WHERE issue_type = 'event')`),
		edges: stateAtomicityCount(t, beadsDir, `SELECT
			(SELECT COUNT(*) FROM dependencies WHERE type = 'parent-child'
				AND COALESCE(depends_on_issue_id, depends_on_wisp_id) = ?) +
			(SELECT COUNT(*) FROM wisp_dependencies WHERE type = 'parent-child'
				AND COALESCE(depends_on_issue_id, depends_on_wisp_id) = ?)`, subject, subject),
		labels: stateAtomicityCount(t, beadsDir, `SELECT
			(SELECT COUNT(*) FROM labels WHERE issue_id = ?) +
			(SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?)`, subject, subject),
		patrol: strings.TrimSpace(bdState(t, bd, dir, subject, "patrol")),
	}
}

// TestEmbeddedSetStateRefusedLabelPatchWritesNothing is GH#5637 on the
// embedded route: when set-state's label patch is refused, the state-change
// event and its parent edge must not be committed either. A value whose
// dimension:value label overflows the 255-character label limit while the
// event title stays under 500 makes the patch the one write that refuses.
func TestEmbeddedSetStateRefusedLabelPatchWritesNothing(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sa")
	subject := bdCreate(t, bd, dir, "State atomicity subject", "--type", "task")
	bdSetState(t, bd, dir, subject.ID, "patrol=active")

	before := readStateAtomicityRows(t, bd, dir, beadsDir, subject.ID)
	if want := (stateAtomicityRows{events: 1, edges: 1, labels: 1, patrol: "active"}); before != want {
		t.Fatalf("baseline = %+v, want %+v", before, want)
	}

	cmd := exec.Command(bd, "set-state", subject.ID, "patrol="+strings.Repeat("v", 260))
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		t.Fatalf("set-state with a 267-character label succeeded, want the label patch refused\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "setting patrol:") || !strings.Contains(stderr.String(), "exceeds maximum length") {
		t.Errorf("stderr does not report the over-length label patch as `setting patrol:`:\n%s", stderr.String())
	}

	after := readStateAtomicityRows(t, bd, dir, beadsDir, subject.ID)
	if after.events != before.events {
		t.Errorf("event rows %d -> %d across a refused set-state, want no new event", before.events, after.events)
	}
	if after.edges != before.edges {
		t.Errorf("parent-child edges into %s %d -> %d across a refused set-state, want no new edge", subject.ID, before.edges, after.edges)
	}
	if after.labels != before.labels || after.patrol != before.patrol {
		t.Errorf("labels %d (patrol %q) -> %d (patrol %q) across a refused set-state, want them unchanged",
			before.labels, before.patrol, after.labels, after.patrol)
	}
}
