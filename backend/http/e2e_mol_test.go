//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestE2E_MoleculesOverHTTP drives the molecule workflow a gc worker runs —
// `bd create --parent`, `bd mol current`, `bd mol progress`, `bd ready --mol`,
// `bd close --continue` and the molecule auto-close after the last step —
// through the real `bd` binary against an in-process `bd serve` (slices S16
// and S19).
//
// Every one of these used to fail or, worse, answer silently over http. Each
// now has one library entry the server runs: the create role mints the child
// id, the molecule view (issueops.ViewMolecule) answers the reads, the
// close runs the auto-close in its own transaction (auto_close_molecule), and
// `close --continue` is the advanceMolecule operation, whose claim sets the
// assignee and never takes a step someone else holds.
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
	step3 := firstJSONField(t, mustBD("create", "Step three", "--parent", root, "--json").stdout, "id")
	if step1 != root+".1" || step2 != root+".2" || step3 != root+".3" {
		t.Fatalf("create --parent minted %q, %q and %q, want %q, %q and %q", step1, step2, step3, root+".1", root+".2", root+".3")
	}
	// An explicit --id beside --parent is still refused locally, before any mint.
	if r := bd("create", "Both", "--parent", root, "--id", root+".9", "--json"); r.code == 0 {
		t.Errorf("create --parent --id succeeded; want the existing refusal: %s", r.stdout)
	}

	mustBD("dep", "add", step2, step1, "--json")
	mustBD("dep", "add", step3, step1, "--json")
	mustBD("update", step1, "-s", "in_progress", "-a", "alice", "--json")
	// Someone else holds step two: the advance must skip it, not take it.
	mustBD("update", step2, "-a", "bob", "--json")

	// S16: mol current with an id is a non-empty answer whose current step is
	// the in-progress one.
	current := decodeMolecules(t, mustBD("mol", "current", root, "--json").stdout)
	if len(current) != 1 || current[0].MoleculeID != root {
		t.Fatalf("mol current %s = %+v, want the one molecule %s", root, current, root)
	}
	if current[0].CurrentStep == nil || current[0].CurrentStep.ID != step1 || current[0].Total != 3 {
		t.Fatalf("mol current %s current=%v total=%d, want current %s of 3", root, current[0].CurrentStep, current[0].Total, step1)
	}
	// The step is the FULL row: the raw dependents read over http is getIssue's
	// shallow projection, which zeroes the assignee a worker prompt reads.
	if got := current[0].CurrentStep.Assignee; got != "alice" {
		t.Errorf("mol current %s current step assignee = %q, want alice (full step row)", root, got)
	}

	progress := mustBD("mol", "progress", root, "--json").stdout
	if n := jsonIntField(t, progress, "total"); n != 3 {
		t.Errorf("mol progress total = %d, want 3: %s", n, progress)
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
		if r.code != 0 {
			t.Fatalf("mol current (no id) failed (exit %d): %s stdout=%s", r.code, r.stderr, r.stdout)
		}
		got := decodeMolecules(t, r.stdout)
		if len(got) != 1 || got[0].MoleculeID != root {
			t.Errorf("mol current (no id) = %+v, want molecule %s", got, root)
		}
	})

	// ready --mol: the step rows are the full rows (the corpus mark
	// mc-ready-mol was the shallow projection dropping the assignee).
	var ready struct {
		Steps []struct {
			Issue struct {
				ID       string `json:"id"`
				Assignee string `json:"assignee"`
			} `json:"issue"`
		} `json:"steps"`
	}
	readyOut := mustBD("ready", "--mol", root, "--json").stdout
	if err := json.Unmarshal([]byte(strings.TrimSpace(readyOut)), &ready); err != nil {
		t.Fatalf("parse ready --mol: %v\n%s", err, readyOut)
	}
	sawStep1 := false
	for _, step := range ready.Steps {
		if step.Issue.ID == step1 {
			sawStep1 = true
			if step.Issue.Assignee != "alice" {
				t.Errorf("ready --mol %s step %s assignee = %q, want alice (full step row)", root, step1, step.Issue.Assignee)
			}
		}
	}
	if !sawStep1 {
		t.Errorf("ready --mol %s did not list the in-progress step %s: %s", root, step1, readyOut)
	}

	// close --continue: step two is first in line but bob holds it, so the
	// advance claims step three for alice — a real claim, assignee set.
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
	if !contBody.Continue.AutoAdvanced || contBody.Continue.NextStep == nil || contBody.Continue.NextStep.ID != step3 ||
		contBody.Continue.MoleculeID != root {
		t.Errorf("close --continue = %s, want auto-advance to %s in %s (skipping %s, held by bob)", cont.stdout, step3, root, step2)
	}
	if got := status(step3); got != "in_progress" {
		t.Errorf("%s status after close --continue = %q, want in_progress", step3, got)
	}
	if got := firstJSONField(t, mustBD("show", step3, "--json").stdout, "assignee"); got != "alice" {
		t.Errorf("%s assignee after close --continue = %q, want alice (a claim, not a status flip)", step3, got)
	}
	if got := firstJSONField(t, mustBD("show", step2, "--json").stdout, "assignee"); got != "bob" || status(step2) != "open" {
		t.Errorf("%s after close --continue = %s/%q, want open and still bob's", step2, status(step2), got)
	}
	if got := status(root); got == "closed" {
		t.Fatalf("molecule %s closed with %s still open", root, step2)
	}

	// Closing the last steps auto-closes the molecule root.
	mustBD("close", step3)
	if got := status(root); got == "closed" {
		t.Fatalf("molecule %s closed with %s still open", root, step2)
	}
	last := mustBD("close", step2, "--force")
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

// TestE2E_MoleculeCloseAgainstAnOlderServer is `bd close` on a molecule step
// against a server that advertises neither issues.close.autoCloseMolecule nor
// issues.advanceMolecule (an older bd serve, or one whose orchestrator owns
// the molecule lifecycle). Neither gap may be silent:
//
//   - close --continue refuses BEFORE closing, so the step is not left closed
//     with the next step unclaimed;
//   - a plain close goes out without the auto-close request and names the
//     molecule it did not auto-close on stderr.
func TestE2E_MoleculeCloseAgainstAnOlderServer(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr, _ := startMaskingProxy(t, startE2EServer(t), func(body map[string]any) {
		caps, _ := body["capabilities"].([]any)
		kept := caps[:0]
		for _, c := range caps {
			if c != "issues.close.autoCloseMolecule" && c != "issues.advanceMolecule" {
				kept = append(kept, c)
			}
		}
		body["capabilities"] = kept
	})
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
	root := firstJSONField(t, mustBD("create", "Old-server molecule", "-t", "molecule", "--json").stdout, "id")
	step := firstJSONField(t, mustBD("create", "Only step", "--parent", root, "--json").stdout, "id")

	cont := bd("close", step, "--continue", "--json")
	if cont.code == 0 {
		t.Fatalf("close --continue succeeded against a server without issues.advanceMolecule: %s", cont.stdout)
	}
	if !strings.Contains(cont.stderr+cont.stdout, "issues.advanceMolecule") || !strings.Contains(cont.stderr+cont.stdout, "nothing was closed") {
		t.Errorf("close --continue refusal does not name the missing operation: stdout=%s stderr=%s", cont.stdout, cont.stderr)
	}
	if got := status(step); got == "closed" {
		t.Fatalf("close --continue refused but %s is closed: the refusal came after the close", step)
	}

	closed := mustBD("close", step, "--json")
	if !strings.Contains(closed.stderr, "belongs to molecule "+root) || !strings.Contains(closed.stderr, "issues.close.autoCloseMolecule") {
		t.Errorf("close of %s against a server without the auto-close gave no notice naming %s: stderr=%s", step, root, closed.stderr)
	}
	if got := status(step); got != "closed" {
		t.Errorf("%s status = %q, want closed", step, got)
	}
	if got := status(root); got == "closed" {
		t.Errorf("molecule %s closed although the server was not asked to auto-close it", root)
	}
}
