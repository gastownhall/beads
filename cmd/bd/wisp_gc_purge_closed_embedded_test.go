//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdShowSucceeds reports whether "bd show <id> --json" succeeds. Non-fatal
// so a cascade sweep reports every deleted step instead of aborting on the
// first one.
func bdShowSucceeds(t *testing.T, bd, dir, id string) bool {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("bd show %s failed (expected for deleted): %v\n%s", id, err, out)
		return false
	}
	return true
}

// TestEmbeddedWispGCPurgeClosedDoesNotCascadeIntoLiveSteps is the regression test for
// the patrol self-destruct bug: `bd mol wisp gc --closed --force` on a
// molecule that has closed its first step deleted the ENTIRE molecule.
//
// purgeClosed handed cascade=true to deleteBatch, and deleteMany(Cascade)
// expands every closed wisp to ALL transitive dependents. In a linear
// molecule DAG, step 1's dependents are every other step, so the first
// closed step dragged all 26 open steps of mol-deacon-patrol into deletion
// (plus its dependency links and events). Wisps live in an ignored
// (non-versioned) table, so the deletion was permanent.
//
// The closed set is complete: every closed, non-pinned, non-infra wisp is
// already a GC candidate. Cascade can only ever add NON-closed dependents to
// the batch — exactly the live steps it must not touch.
func TestEmbeddedWispGCPurgeClosedDoesNotCascadeIntoLiveSteps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc")

	// Deacon patrol shape: an open mol root (the husk that survives) plus a
	// linear chain of 26 steps. Each step depends on its predecessor (the
	// first step is dependency-free — the patrol closes it, so it cannot be
	// blocked on an open root), and step 1's transitive dependents are every
	// other step.
	root := bdCreate(t, bd, dir, "patrol molecule", "--ephemeral", "--type", "molecule")

	const steps = 26
	stepIDs := make([]string, steps)
	prev := ""
	for i := 0; i < steps; i++ {
		id := bdCreate(t, bd, dir, fmt.Sprintf("patrol step %d", i+1), "--ephemeral").ID
		stepIDs[i] = id
		if prev != "" {
			bdDepAdd(t, bd, dir, id, prev) // step N depends on step N-1
		}
		prev = id
	}

	// Patrol cycle: the completed first step is closed, then the formula's GC
	// step purges closed wisps. The other 25 steps are live work (open) and
	// must survive the purge.
	bdClose(t, bd, dir, stepIDs[0])
	// --age 0s: the step closed moments ago, and closed GC now keeps wisps
	// updated within --age (GH#8202); this test is about cascade, not age.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")

	// The closed step itself is gone.
	bdShowFail(t, bd, dir, stepIDs[0])

	// Every live step survives.
	for i := 1; i < steps; i++ {
		if bdShowSucceeds(t, bd, dir, stepIDs[i]) {
			continue
		}
		t.Errorf("live step %s (index %d) was cascade-deleted by purgeClosed; only the first closed step should be removed", stepIDs[i], i)
	}

	// The open molecule root survives too (the husk must keep its steps).
	if bdShowSucceeds(t, bd, dir, root.ID) {
		return
	}
	t.Error("open molecule root was deleted by purgeClosed")
}

// TestEmbeddedWispGCPurgeClosedStillPurgesFullyClosedMolecules guards the other side
// of the fix: when EVERY step is closed, gc --closed --force must still remove
// the whole molecule. Restricting cascade must not break complete cleanup.
func TestEmbeddedWispGCPurgeClosedStillPurgesFullyClosedMolecules(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc2")

	const steps = 5
	stepIDs := make([]string, steps)
	prev := ""
	for i := 0; i < steps; i++ {
		id := bdCreate(t, bd, dir, fmt.Sprintf("finished step %d", i+1), "--ephemeral").ID
		stepIDs[i] = id
		if prev != "" {
			bdDepAdd(t, bd, dir, id, prev)
		}
		prev = id
	}
	for _, id := range stepIDs {
		bdClose(t, bd, dir, id)
	}

	// --age 0s: the steps closed moments ago (see GH#8202).
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")

	for _, id := range stepIDs {
		bdShowFail(t, bd, dir, id)
	}
}

// TestEmbeddedWispGCPurgeClosedKeepsRecentlyClosedWisps pins GH#8202 end to
// end: closed GC honors --age (default 1h), so a wisp closed moments ago, here
// a patrol step carrying its report in its notes, is neither deleted nor
// counted, and --age 0s still purges it at once. Wisps are Dolt-ignored, so
// that deletion cannot be recovered.
func TestEmbeddedWispGCPurgeClosedKeepsRecentlyClosedWisps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc4")

	fresh := bdCreate(t, bd, dir, "patrol report step", "--ephemeral").ID
	bdUpdate(t, bd, dir, fresh, "--notes", "patrol report: 0 reaped / 0 purged / 0 anomalies")
	bdClose(t, bd, dir, fresh)

	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
	if !strings.Contains(out, `"deleted_count": 0`) {
		t.Errorf("default gc --closed --force counted a wisp closed moments ago as deleted:\n%s", out)
	}
	if !bdShowSucceeds(t, bd, dir, fresh) {
		t.Fatalf("default gc --closed --force deleted wisp %s, and its report notes, moments after it closed", fresh)
	}

	// --age 0s is the explicit purge-all escape hatch.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")
	bdShowFail(t, bd, dir, fresh)
}

// TestEmbeddedWispGCPurgeClosedKeepsWhatNewerWispsDependOn pins the closed
// GC's hold-back end to end on the direct route (GH#8202). A two-step
// molecule whose first step closed hours ago, and whose second step, which
// depends on it, closed moments ago, straddles --age. The unforced --dry-run
// previews only an unrelated idle wisp and exits 0: selecting the first step
// alone made the deletion role's dependents guard refuse the whole preview.
// --force deletes only the idle wisp, and once the second step ages out too
// the two go together. A chain whose middle step the scan leaves out
// (--exclude-type here, the 5000-wisp scan limit in a backlog) keeps its
// first step while its last step is too new.
func TestEmbeddedWispGCPurgeClosedKeepsWhatNewerWispsDependOn(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pc6")

	first := bdCreate(t, bd, dir, "first step", "--ephemeral").ID
	second := bdCreate(t, bd, dir, "second step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, second, first) // the second step depends on the first
	idle := bdCreate(t, bd, dir, "unrelated idle wisp", "--ephemeral").ID
	for _, id := range []string{first, second, idle} {
		bdClose(t, bd, dir, id)
	}
	backdateEmbeddedWisps(t, beadsDir, first, idle)

	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--dry-run", "--json")
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("gc --closed --dry-run --json printed no JSON object:\n%s", out)
	}
	var preview struct {
		WouldDeleteIDs []string `json:"would_delete_ids"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&preview); err != nil {
		t.Fatalf("parse gc --closed --dry-run --json: %v\n%s", err, out)
	}
	if !slices.Equal(preview.WouldDeleteIDs, []string{idle}) {
		t.Errorf("dry run would delete %v, want only the idle wisp %s:\n%s", preview.WouldDeleteIDs, idle, out)
	}
	out = bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--dry-run")
	if !strings.Contains(out, "Keeping 1 older closed wisp(s) that newer ones depend on") {
		t.Errorf("dry run does not say it kept the first step for the second:\n%s", out)
	}

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	bdShowFail(t, bd, dir, idle)
	for _, id := range []string{first, second} {
		if !bdShowSucceeds(t, bd, dir, id) {
			t.Fatalf("gc --closed --force deleted %s, a step of a molecule whose last step closed moments ago", id)
		}
	}

	backdateEmbeddedWisps(t, beadsDir, second)
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	bdShowFail(t, bd, dir, first)
	bdShowFail(t, bd, dir, second)

	// A step the scan leaves out (here by --exclude-type, as the 5000-wisp
	// scan limit would) still carries a newer step's hold down its chain:
	// the chain's first step stays while its last step is too new for --age.
	cut := bdCreate(t, bd, dir, "first step of a chain the scan cuts", "--ephemeral").ID
	middle := bdCreate(t, bd, dir, "middle step the scan leaves out", "--ephemeral", "--type", "chore").ID
	last := bdCreate(t, bd, dir, "last step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, middle, cut)
	bdDepAdd(t, bd, dir, last, middle)
	for _, id := range []string{cut, middle, last} {
		bdClose(t, bd, dir, id)
	}
	backdateEmbeddedWisps(t, beadsDir, cut, middle)
	out = bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--exclude-type", "chore")
	if !bdShowSucceeds(t, bd, dir, cut) {
		t.Fatalf("gc --closed --force --exclude-type chore deleted %s, though %s closed moments ago and depends on it through %s, which the scan left out:\n%s", cut, last, middle, out)
	}
	if !strings.Contains(out, "Keeping 1 older closed wisp(s) that newer ones depend on") {
		t.Errorf("gc --closed --force --exclude-type chore does not say it kept the first step for the last:\n%s", out)
	}
}

// backdateEmbeddedWisps moves each wisp's updated_at two hours back, past the
// default --age, straight in the embedded database. Wisps are Dolt-ignored,
// so there is nothing to commit.
func backdateEmbeddedWisps(t *testing.T, beadsDir string, ids ...string) {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("close embedded database: %v", err)
		}
	}()
	old := time.Now().UTC().Add(-2 * time.Hour)
	for _, id := range ids {
		res, err := db.ExecContext(t.Context(), "UPDATE wisps SET updated_at = ? WHERE id = ?", old, id)
		if err != nil {
			t.Fatalf("backdate wisp %s: %v", id, err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("backdate wisp %s: %d rows affected (%v), want 1", id, n, err)
		}
	}
}

// TestEmbeddedWispGCPurgeClosedZeroAgeIsPurgeAll guards the escape hatch of
// the GH#8202 age floor: --closed --force with --age 0s — or any non-positive
// duration — still selects every closed ephemeral wisp regardless of age, the
// behavior --closed had before the floor. The floor must not overreach.
func TestEmbeddedWispGCPurgeClosedZeroAgeIsPurgeAll(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc5")

	first := bdCreate(t, bd, dir, "closed moments ago", "--ephemeral").ID
	second := bdCreate(t, bd, dir, "also closed moments ago", "--ephemeral").ID
	bdClose(t, bd, dir, first)
	bdClose(t, bd, dir, second)

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")
	bdShowFail(t, bd, dir, first)
	bdShowFail(t, bd, dir, second)

	// A non-positive duration omits the age bound the same way zero does.
	third := bdCreate(t, bd, dir, "negative age window", "--ephemeral").ID
	bdClose(t, bd, dir, third)
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age=-1s")
	bdShowFail(t, bd, dir, third)
}
