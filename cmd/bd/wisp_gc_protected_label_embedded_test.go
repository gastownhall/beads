//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedWispGCProtectedLabel is the end-to-end regression for the data
// loss behind wisp.protected_labels: `bd mol wisp gc --age` deleted unread mail
// and open escalations an orchestration layer stored as ephemeral beads. Such a
// record sits in plain open status for as long as nobody acts on it, and open
// is age-reclaimable by design, so the longer it went unread the more certainly
// GC deleted it.
//
// It runs against a throwaway store from bdInit, never a live one: this command
// deletes, without a digest.
func TestEmbeddedWispGCProtectedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcl")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	openPlain := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID
	closedPlain := bdCreate(t, bd, dir, "spent wisp", "--ephemeral").ID
	otherLabel := bdCreate(t, bd, dir, "unrelated label", "--ephemeral", "--label", "thread").ID
	unreadMail := bdCreate(t, bd, dir, "unread mail", "--ephemeral", "--label", "keep:mail").ID
	readMail := bdCreate(t, bd, dir, "read mail", "--ephemeral", "--label", "thread,keep:mail").ID
	bdCommand(t, bd, dir, "close", closedPlain, readMail)

	// --age 0s makes every wisp old enough (see TestEmbeddedWispGCProtectsActiveWisps).
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--all", "--force")

	for _, id := range []string{unreadMail, readMail} {
		if !bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("wisp %s carries protected label keep:mail and must survive wisp gc --all", id)
		}
	}
	for _, id := range []string{openPlain, closedPlain, otherLabel} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("wisp %s carries no protected label and must be reclaimed by wisp gc --all", id)
		}
	}
}

// TestEmbeddedWispGCProtectedLabelCascade drives the cascade site end to end:
// without --all the parent scan passes over closed wisps, so only the cascade
// from an abandoned parent can reach its closed steps, and a step carrying a
// protected label must not go down with the parent.
func TestEmbeddedWispGCProtectedLabelCascade(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcc")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	parent := bdCreate(t, bd, dir, "abandoned parent", "--ephemeral").ID
	plainStep := bdCreate(t, bd, dir, "plain step", "--ephemeral").ID
	keptStep := bdCreate(t, bd, dir, "kept step", "--ephemeral", "--label", "keep:mail").ID
	// Closed before the edges are added: a step blocked on its open parent
	// cannot be closed without --force.
	bdCommand(t, bd, dir, "close", plainStep, keptStep)
	bdDepAdd(t, bd, dir, plainStep, parent)
	bdDepAdd(t, bd, dir, keptStep, parent)

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--force")

	if !bdShowSucceeds(t, bd, dir, keptStep) {
		t.Errorf("closed step %s carries protected label keep:mail and must survive its parent's GC", keptStep)
	}
	for _, id := range []string{parent, plainStep} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("unprotected wisp %s must be reclaimed (abandoned parent or its closed step)", id)
		}
	}
}

// TestEmbeddedWispGCClosedPurgeProtectedLabel pins that `wisp gc --closed`
// honors the setting on the direct route: the preview names only the
// unlabeled wisps and reports the skip, and --force deletes only those.
func TestEmbeddedWispGCClosedPurgeProtectedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcp")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	readMail := bdCreate(t, bd, dir, "read mail", "--ephemeral", "--label", "keep:mail").ID
	spent := bdCreate(t, bd, dir, "spent wisp", "--ephemeral").ID
	other := bdCreate(t, bd, dir, "spent thread", "--ephemeral", "--label", "thread").ID
	bdCommand(t, bd, dir, "close", readMail, spent, other)

	preview := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed")
	if !strings.Contains(preview, "Found 2 closed wisp(s) to delete") {
		t.Errorf("closed purge preview should find exactly the 2 unlabeled wisps; got:\n%s", preview)
	}
	if !strings.Contains(preview, "Skipping 1 wisp(s) carrying a protected label") {
		t.Errorf("closed purge preview must report the protected-label skip; got:\n%s", preview)
	}

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	if !bdShowSucceeds(t, bd, dir, readMail) {
		t.Errorf("closed wisp %s carries protected label keep:mail and must survive wisp gc --closed --force", readMail)
	}
	for _, id := range []string{spent, other} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("closed wisp %s carries no protected label and must be purged", id)
		}
	}
}

// TestEmbeddedWispGCExcludeLabel pins --exclude-label end to end: it adds to
// the stored setting for one run, so a wisp carrying either label survives,
// while a wisp carrying only bd:protected — a label nothing configured — is
// reclaimed like any other.
func TestEmbeddedWispGCExcludeLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcx")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	mail := bdCreate(t, bd, dir, "unread mail", "--ephemeral", "--label", "keep:mail").ID
	alert := bdCreate(t, bd, dir, "open escalation", "--ephemeral", "--label", "keep:alert").ID
	defaultLabel := bdCreate(t, bd, dir, "no default label", "--ephemeral", "--label", "bd:protected").ID
	plain := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--exclude-label", "keep:alert", "--force")

	for _, id := range []string{mail, alert} {
		if !bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("wisp %s carries a label protected by the setting or --exclude-label and must survive", id)
		}
	}
	for _, id := range []string{defaultLabel, plain} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("wisp %s carries no protected label and must be reclaimed", id)
		}
	}
}

// TestEmbeddedWispGCProtectedLabelWriteRefused pins the write gate end to end:
// a value wisp gc would refuse to read is refused by `bd config set` and by
// `bd config set-many`, whose batch must write nothing when one pair is
// refused, so a malformed list never reaches the sweep through the CLI.
func TestEmbeddedWispGCProtectedLabelWriteRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcw")
	run := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out), 0
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out), exitErr.ExitCode()
	}

	bad := []string{`["keep:mail"`, `[null,"keep:mail"]`, `["keep:mail",1]`, "keep mail", ","}
	for _, value := range bad {
		if out, code := run("config", "set", "wisp.protected_labels", value); code == 0 {
			t.Errorf("bd config set wisp.protected_labels %q succeeded:\n%s\nwant the malformed value refused", value, out)
		}
		if out, code := run("config", "set-many", "wisp.protected_labels="+value, "pl.untouched=1"); code == 0 {
			t.Errorf("bd config set-many wisp.protected_labels=%q succeeded:\n%s\nwant the malformed value refused", value, out)
		}
	}
	if out, code := run("config", "get", "pl.untouched"); code != 0 || !strings.Contains(out, "pl.untouched (not set)") {
		t.Errorf("bd config get pl.untouched = %q (exit %d) after a refused set-many; want the batch to write nothing", out, code)
	}
	if out, code := run("config", "get", "wisp.protected_labels"); code != 0 || !strings.Contains(out, "wisp.protected_labels (not set)") {
		t.Errorf("bd config get wisp.protected_labels = %q (exit %d) after refused writes; want it unset", out, code)
	}
}

// TestEmbeddedWispGCProtectedLabelReporting pins what an operator sees. The
// key is recognized (no "not a recognized config key" warning on the set). An
// age preview names only eligible wisps and says how many a protected label
// held back — counting a wisp the age sweep reached directly and one only the
// cascade reached — while --json carries only the eligible candidates. A
// wisp.protected_labels key in config.yaml is ignored, and says so.
func TestEmbeddedWispGCProtectedLabelReporting(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gcr")
	combined := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	if out := combined("config", "set", "wisp.protected_labels", "keep:mail"); strings.Contains(out, "not a recognized config key") {
		t.Errorf("bd config set wisp.protected_labels warned that the key is unrecognized:\n%s", out)
	}

	mail := bdCreate(t, bd, dir, "unread mail", "--ephemeral", "--label", "keep:mail").ID
	parent := bdCreate(t, bd, dir, "abandoned parent", "--ephemeral").ID
	keptStep := bdCreate(t, bd, dir, "kept step", "--ephemeral", "--label", "keep:mail").ID
	plain := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID
	bdCommand(t, bd, dir, "close", keptStep)
	bdDepAdd(t, bd, dir, keptStep, parent)

	preview := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--dry-run")
	for _, want := range []string{"would clean 2 abandoned wisp(s)", "Skipping 2 wisp(s) carrying a protected label"} {
		if !strings.Contains(preview, want) {
			t.Errorf("age preview should contain %q; got:\n%s", want, preview)
		}
	}
	for _, id := range []string{mail, keptStep} {
		if strings.Contains(preview, id) {
			t.Errorf("age preview lists protected wisp %s; got:\n%s", id, preview)
		}
	}

	var got WispGCResult
	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--dry-run", "--json")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal wisp gc --json: %v\n%s", err, out)
	}
	ids := map[string]bool{}
	for _, id := range got.CleanedIDs {
		ids[id] = true
	}
	if got.Candidates != 2 || len(ids) != 2 || !ids[parent] || !ids[plain] {
		t.Errorf("wisp gc --dry-run --json = %+v, want exactly the eligible %s and %s", got, parent, plain)
	}
	if got.CleanedCount != 0 || !got.DryRun {
		t.Errorf("wisp gc --dry-run --json = %+v, want dry_run and cleaned_count 0 in a preview", got)
	}

	yaml, err := os.OpenFile(filepath.Join(beadsDir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open config.yaml: %v", err)
	}
	if _, err := yaml.WriteString("\nwisp:\n  protected_labels: keep:alert\n"); err != nil {
		t.Fatalf("append config.yaml: %v", err)
	}
	if err := yaml.Close(); err != nil {
		t.Fatalf("close config.yaml: %v", err)
	}
	if out := combined("mol", "wisp", "gc", "--age", "0s", "--dry-run"); !strings.Contains(out, "wisp.protected_labels in config.yaml (keep:alert) is ignored") {
		t.Errorf("wisp gc should warn that the config.yaml key is ignored; got:\n%s", out)
	}
}
