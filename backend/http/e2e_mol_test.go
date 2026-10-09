//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestE2E_MoleculesOverHTTP drives the molecule workflow a gc worker runs —
// `bd create --parent`, `bd mol current`, `bd mol progress`, `bd close
// --continue` and the molecule auto-close after the last step — through the
// real `bd` binary against an in-process `bd serve` (slices S16 and S19).
//
// Every one of these used to fail or, worse, answer silently over http: the
// child-id mint was a raw GetNextChildID the client stubs, `mol current`
// swallowed a refused edge read into "no molecules", `close --continue`
// needed a raw transaction, and the auto-close skipped itself on the same
// swallowed read.
func TestE2E_MoleculesOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	workspace := t.TempDir()

	bd := func(args ...string) bdResult {
		t.Helper()
		return runBD(t, bin, workspace, nil, append([]string{"--actor", "alice"}, args...)...)
	}
	mustBD := func(args ...string) bdResult {
		t.Helper()
		r := bd(args...)
		if r.code != 0 {
			t.Fatalf("bd %s failed (exit %d): stdout=%s stderr=%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
		return r
	}
	status := func(id string) string {
		t.Helper()
		return firstJSONField(t, mustBD("show", id, "--json").stdout, "status")
	}

	mustBD("connect", "http://"+addr, "--expect-project-id", e2eProjectID, "--json")

	root := firstJSONField(t, mustBD("create", "Patrol molecule", "-t", "molecule", "--json").stdout, "id")
	if root == "" {
		t.Fatal("bd create (molecule root) produced no id")
	}

	// S19: the server mints <parent>.<n>; the client no longer asks a raw
	// GetNextChildID the http backend does not serve.
	step1 := firstJSONField(t, mustBD("create", "Step one", "--parent", root, "--json").stdout, "id")
	step2 := firstJSONField(t, mustBD("create", "Step two", "--parent", root, "--json").stdout, "id")
	if step1 != root+".1" || step2 != root+".2" {
		t.Fatalf("create --parent minted %q and %q, want %q and %q", step1, step2, root+".1", root+".2")
	}
	// An explicit --id beside --parent is still refused locally, before any mint.
	if r := bd("create", "Both", "--parent", root, "--id", root+".9", "--json"); r.code == 0 {
		t.Errorf("create --parent --id succeeded; want the existing refusal: %s", r.stdout)
	}

	mustBD("dep", "add", step2, step1, "--json")
	mustBD("update", step1, "-s", "in_progress", "-a", "alice", "--json")

	// S16: mol current with an id is a non-empty answer whose current step is
	// the in-progress one.
	current := decodeMolecules(t, mustBD("mol", "current", root, "--json").stdout)
	if len(current) != 1 || current[0].MoleculeID != root {
		t.Fatalf("mol current %s = %+v, want the one molecule %s", root, current, root)
	}
	if current[0].CurrentStep == nil || current[0].CurrentStep.ID != step1 || current[0].Total != 2 {
		t.Fatalf("mol current %s current=%v total=%d, want current %s of 2", root, current[0].CurrentStep, current[0].Total, step1)
	}
	// The step is the FULL row: the raw dependents read over http is getIssue's
	// shallow projection, which zeroes the assignee a worker prompt reads.
	if got := current[0].CurrentStep.Assignee; got != "alice" {
		t.Errorf("mol current %s current step assignee = %q, want alice (full step row)", root, got)
	}

	progress := mustBD("mol", "progress", root, "--json").stdout
	if n := jsonIntField(t, progress, "total"); n != 2 {
		t.Errorf("mol progress total = %d, want 2: %s", n, progress)
	}
	if id := firstJSONField(t, progress, "current_step_id"); id != step1 {
		t.Errorf("mol progress current_step_id = %q, want %q: %s", id, step1, progress)
	}

	t.Run("mol current without an id", func(t *testing.T) {
		r := bd("mol", "current", "--json")
		// Whatever the backend can do, it must not answer an EMPTY list for an
		// agent with a molecule in progress: that was the swallowed refusal.
		if r.code == 0 && len(decodeMolecules(t, r.stdout)) == 0 {
			t.Fatalf("mol current (no id) answered an empty list while %s is in progress: stderr=%s", step1, r.stderr)
		}
		t.Skip("needs S6c (externaldeps reader passthrough): over http the Reader.List behind the in_progress listing " +
			"reaches the SearchIssuesWithCounts stub until S6c lands; remove this skip once S6c is underneath")
		if r.code != 0 {
			t.Fatalf("mol current (no id) failed (exit %d): %s", r.code, r.stderr)
		}
		got := decodeMolecules(t, r.stdout)
		if len(got) != 1 || got[0].MoleculeID != root {
			t.Errorf("mol current (no id) = %+v, want molecule %s", got, root)
		}
	})

	// close --continue: the auto-claim is one guarded Lifecycle update, so it
	// advances to step two over http.
	cont := mustBD("close", step1, "--continue", "--json")
	var contBody struct {
		Continue struct {
			NextStep     *struct{ ID string } `json:"next_step"`
			AutoAdvanced bool                 `json:"auto_advanced"`
			MoleculeID   string               `json:"molecule_id"`
		} `json:"continue"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(cont.stdout)), &contBody); err != nil {
		t.Fatalf("parse close --continue output: %v\n%s", err, cont.stdout)
	}
	if !contBody.Continue.AutoAdvanced || contBody.Continue.NextStep == nil || contBody.Continue.NextStep.ID != step2 ||
		contBody.Continue.MoleculeID != root {
		t.Errorf("close --continue = %s, want auto-advance to %s in %s", cont.stdout, step2, root)
	}
	if got := status(step2); got != "in_progress" {
		t.Errorf("%s status after close --continue = %q, want in_progress", step2, got)
	}
	if got := status(root); got == "closed" {
		t.Fatalf("molecule %s closed with %s still open", root, step2)
	}

	// Closing the last step auto-closes the molecule root.
	last := mustBD("close", step2)
	if strings.Contains(last.stderr, "could not") {
		t.Errorf("close %s warned: %s", step2, last.stderr)
	}
	if got := status(root); got != "closed" {
		t.Errorf("molecule %s status after its last step closed = %q, want closed (auto-close): stdout=%s stderr=%s",
			root, got, last.stdout, last.stderr)
	}
}

type e2eMolecule struct {
	MoleculeID  string `json:"molecule_id"`
	Total       int    `json:"total"`
	CurrentStep *struct {
		ID       string `json:"id"`
		Assignee string `json:"assignee"`
	} `json:"current_step"`
}

func decodeMolecules(t *testing.T, out string) []e2eMolecule {
	t.Helper()
	var got []e2eMolecule
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("parse mol current output: %v\n%s", err, out)
	}
	return got
}
