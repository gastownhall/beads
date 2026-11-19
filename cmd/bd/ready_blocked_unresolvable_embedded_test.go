//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bd-uschhs (#5850) AC-8: the lane's whole change is DISCLOSURE — the combined
// read, the detail records, the renders. The invariant it must not disturb:
// `bd ready` membership, `bd blocked` membership, and the blocked flag answer
// exactly what they answered before, under each configuration.
//
// Three targets an issue can carry a blocks edge to, three pre-existing
// outcomes the lane must leave alone:
//
//   - a REAL local issue blocks on every surface: withheld from ready, named
//     by the listing, flagged true both decorated and raw;
//   - an explicit `external:` reference blocks through the external-dependency
//     policy (unsatisfied ref), but not through the raw stored column, which
//     has never known about other projects;
//   - a BARE id in another repository's prefix blocks NOWHERE: the policy
//     reads only `external:`-prefixed refs (externaldeps.isExternalReference),
//     and the stored column has no row to join — the issue stays READY, so
//     the ghost edge a listing would drop (GH#5850's seam) was never hiding
//     work from anyone.
//
// The reader-level half is pinned by RunBlockingAnnotatorCountsAnUnresolvable
// BlockerAsOpen, run against the dolt, uow, embeddeddolt and served-httpclient
// configurations (the decorator dimension: uow wraps the store the proxied
// route reads through, and the served client carries the contract over the
// wire). This test pins the CLI outcomes those roles feed, on the embedded
// backend; the proxied variant below covers the httpclient/uow route. A ghost
// that started blocking here — say, by feeding the new combined read into the
// flag — is the exact regression AC-8 exists to catch.
func TestEmbeddedReadyBlockedUnresolvableOutcomesUnchanged(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ru")

	const scope = "gh5850-ac8"
	newIssue := func(title string) *types.Issue {
		return bdCreate(t, bd, dir, title, "--type", "task", "--label", scope)
	}

	unblocked := newIssue("Ready baseline")
	ghost := newIssue("Blocks-edge to a bare cross-repo id with no row here")
	ext := newIssue("Blocks-edge to an external reference")
	real := newIssue("Blocks-edge to a real local issue")
	localBlocker := bdCreate(t, bd, dir, "The local blocker", "--type", "task")

	bdDep(t, bd, dir, "add", ghost.ID, "liveop-gone", "--type", "blocks")
	bdDep(t, bd, dir, "add", ext.ID, "external:liveop:gone-too", "--type", "blocks")
	bdDep(t, bd, dir, "add", real.ID, localBlocker.ID, "--type", "blocks")

	// MEMBERSHIP, ready: the unblocked issue and the bare-ghost issue are
	// ready work; the external ref and the real blocker withhold theirs.
	cmd := exec.Command(bd, "ready", "--json", "--label", scope)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var readyRows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &readyRows); err != nil {
		t.Fatalf("parse ready JSON: %v\n%s", err, stdout.String())
	}
	ready := make(map[string]bool, len(readyRows))
	for _, r := range readyRows {
		ready[r.ID] = true
	}
	if !ready[unblocked.ID] || !ready[ghost.ID] {
		t.Errorf("ready membership lost ready work (unblocked=%v ghost=%v): %v",
			ready[unblocked.ID], ready[ghost.ID], ready)
	}
	if ready[ext.ID] || ready[real.ID] {
		t.Errorf("ready membership admitted blocked work (ext=%v real=%v): %v",
			ready[ext.ID], ready[real.ID], ready)
	}

	// MEMBERSHIP, the listing: the external ref and the real blocker are
	// named; the bare ghost is absent, exactly as before the lane.
	blocked := idSet(bdBlockedJSON(t, bd, dir))
	if !blocked[ext.ID] || !blocked[real.ID] {
		t.Errorf("blocked listing membership lost blocked work (ext=%v real=%v): %v",
			blocked[ext.ID], blocked[real.ID], blocked)
	}
	if blocked[unblocked.ID] || blocked[ghost.ID] {
		t.Errorf("blocked listing admitted work that is not blocked (unblocked=%v ghost=%v): %v",
			blocked[unblocked.ID], blocked[ghost.ID], blocked)
	}

	// THE FLAG, under both decorator legs. The default pass runs the
	// decorated annotator (external-dependency policy included); --raw
	// answers the stored column beneath it.
	flags := func(raw bool) map[string]bool {
		t.Helper()
		args := []string{"blocked", "--flags", "--json"}
		if raw {
			args = append(args, "--raw")
		}
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader(strings.Join([]string{unblocked.ID, ghost.ID, ext.ID, real.ID}, "\n"))
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd blocked --flags failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		var rows []blockedFlagRow
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &rows); err != nil {
			t.Fatalf("parse blocked --flags JSON: %v\n%s", err, stdout.String())
		}
		got := make(map[string]bool, len(rows))
		for _, r := range rows {
			got[r.ID] = r.IsBlocked
		}
		return got
	}

	decorated := flags(false)
	if !decorated[ext.ID] || !decorated[real.ID] {
		t.Errorf("decorated blocked flag lost a blocker (ext=%v real=%v): %v",
			decorated[ext.ID], decorated[real.ID], decorated)
	}
	if decorated[unblocked.ID] || decorated[ghost.ID] {
		t.Errorf("decorated blocked flag set on unblocked work (unblocked=%v ghost=%v): %v",
			decorated[unblocked.ID], decorated[ghost.ID], decorated)
	}

	// The stored column beneath the decoration: only the real local blocker
	// sets it — the external ref and the bare ghost are the decoration's
	// business (and only the external ref's), which is the split AC-8 pins:
	// the lane must not have moved either ghost shape into the column.
	rawFlags := flags(true)
	if !rawFlags[real.ID] || rawFlags[unblocked.ID] || rawFlags[ext.ID] || rawFlags[ghost.ID] {
		t.Errorf("raw stored flag = %v, want only the real blocker set (real=true, others=false)", rawFlags)
	}
}
