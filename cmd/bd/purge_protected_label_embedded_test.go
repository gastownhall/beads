//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The end-to-end pins for be-edf: `bd purge --force` honors
// wisp.protected_labels, the guard `bd mol wisp gc` already honored.
//
// WHY THIS NEEDED ITS OWN TEST rather than resting on the workapi unit tests
// and the Sweeper contract. Those two prove the filter holds a labeled row back
// and that each backend hydrates the labels it filters on. Neither of them can
// see whether `bd purge` ever RESOLVES the config key and puts it on the
// request — and that read is the whole of what this command was missing. A
// purge that sent an empty ProtectedLabels would pass every other test in this
// change and delete exactly the records the guard exists to keep.
//
// Both cases run against a throwaway store from bdInit, never a live one.

// createAndCloseLabeledEphemeral creates a labeled ephemeral bead and closes
// it, which is the state `bd purge` selects: closed, ephemeral tier.
func createAndCloseLabeledEphemeral(t *testing.T, bd, dir, title, labels string) string {
	t.Helper()
	args := []string{title, "--ephemeral"}
	if labels != "" {
		args = append(args, "--label", labels)
	}
	issue := bdCreate(t, bd, dir, args...)
	cmd := exec.Command(bd, "close", issue.ID)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("close %s failed: %v\n%s", issue.ID, err, out)
	}
	return issue.ID
}

// purgeSurvivors returns the ids still resolvable after a purge, so an
// assertion is about ROWS rather than about what the command printed.
func purgeSurvivors(t *testing.T, bd, dir string, ids []string) map[string]bool {
	t.Helper()
	alive := make(map[string]bool, len(ids))
	for _, id := range ids {
		cmd := exec.Command(bd, "show", id, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if err := cmd.Run(); err == nil {
			alive[id] = true
		}
	}
	return alive
}

// TestPurgeProtectsLabeledBeads is be-edf's regression test.
//
// `bd purge --force` and `bd mol wisp gc --closed --force` delete the same
// rows, and docs/workflows/wisps.md presents them as interchangeable. Before
// this change a workspace that configured wisp.protected_labels got the guard
// on one of them and not the other, with no warning from either — so an
// operator who had verified gc was safe would run purge and lose the records.
func TestPurgeProtectsLabeledBeads(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ppl")

	// A configured value REPLACES the built-in default, so name every label
	// this store wants protected.
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "gt:message,gt:escalation")

	plain := createAndCloseLabeledEphemeral(t, bd, dir, "idle wisp", "")
	otherLabel := createAndCloseLabeledEphemeral(t, bd, dir, "unrelated label", "gt:thread")
	mail := createAndCloseLabeledEphemeral(t, bd, dir, "read mail", "gt:message")
	escalation := createAndCloseLabeledEphemeral(t, bd, dir, "resolved escalation", "gt:escalation")
	// Protection is per-label, not per-bead.
	mixed := createAndCloseLabeledEphemeral(t, bd, dir, "mail in a thread", "gt:thread,gt:message")
	// An ad-hoc label named only on the command line adds to the configured set.
	adHoc := createAndCloseLabeledEphemeral(t, bd, dir, "ad-hoc protected", "keep:me")

	out := bdPurge(t, bd, dir, "--force", "--exclude-label", "keep:me")

	alive := purgeSurvivors(t, bd, dir, []string{plain, otherLabel, mail, escalation, mixed, adHoc})

	for _, tc := range []struct{ id, what string }{
		{mail, "read mail"},
		{escalation, "resolved escalation"},
		{mixed, "mail carrying an extra unprotected label"},
		{adHoc, "bead protected by --exclude-label"},
	} {
		if !alive[tc.id] {
			t.Errorf("%s (%s) was PURGED; wisp.protected_labels must protect it. output:\n%s", tc.what, tc.id, out)
		}
	}

	// The negative control, and it is the half that keeps this honest: a guard
	// that protected everything would satisfy the loop above completely, while
	// making `bd purge` silently unable to purge anything.
	for _, tc := range []struct{ id, what string }{
		{plain, "unlabeled"},
		{otherLabel, "unprotected-label"},
	} {
		if alive[tc.id] {
			t.Errorf("%s bead (%s) survived; the guard must not over-protect. output:\n%s", tc.what, tc.id, out)
		}
	}

	if !strings.Contains(out, "Protected label (skipped): 4") {
		t.Errorf("purge output does not report 4 label-protected skips:\n%s", out)
	}
}

// TestPurgeJSONReportsLabeledSkips pins the machine-readable half. The skip is
// reported the way the pinned count is, so a scheduled purge can SEE that its
// sweep was narrowed rather than inferring it from a smaller number than
// expected.
func TestPurgeJSONReportsLabeledSkips(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pjs")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "gt:message")

	createAndCloseLabeledEphemeral(t, bd, dir, "plain", "")
	createAndCloseLabeledEphemeral(t, bd, dir, "mail", "gt:message")

	out := bdPurge(t, bd, dir, "--force", "--json")

	var stats map[string]any
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("purge --json is not JSON: %v\n%s", err, out)
	}
	if got := stats["labeled_skipped"]; got != float64(1) {
		t.Errorf("labeled_skipped = %v, want 1; full payload: %s", got, out)
	}
	if got := stats["purged_count"]; got != float64(1) {
		t.Errorf("purged_count = %v, want 1 — only the unlabeled bead; full payload: %s", got, out)
	}
}

// TestPurgeWithoutConfiguredLabelsStillProtectsTheBuiltInDefault pins the
// precedence tail: with nothing configured the built-in "bd:protected" applies,
// so the extension point is usable before anyone has configured it — and a
// workspace that never set the key is not silently unguarded.
func TestPurgeWithoutConfiguredLabelsStillProtectsTheBuiltInDefault(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pdf")

	plain := createAndCloseLabeledEphemeral(t, bd, dir, "plain", "")
	defaulted := createAndCloseLabeledEphemeral(t, bd, dir, "protected", "bd:protected")

	out := bdPurge(t, bd, dir, "--force")
	alive := purgeSurvivors(t, bd, dir, []string{plain, defaulted})

	if !alive[defaulted] {
		t.Errorf("bd:protected bead (%s) was purged with no config set; the built-in default must apply. output:\n%s", defaulted, out)
	}
	if alive[plain] {
		t.Errorf("unlabeled bead (%s) survived; the default must not over-protect. output:\n%s", plain, out)
	}
}
