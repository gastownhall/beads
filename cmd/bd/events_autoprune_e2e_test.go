//go:build cgo && unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestAutoPruneBoundsTheJournalAfterAMutatingCommand is the feature as a user
// meets it: enable the journal, write, and the journal stays inside its floors
// without anyone running `bd events prune`.
//
// End-to-end through the binary is the only way to see it. The trigger lives in
// the root command's post-run maintenance region, which no store-level or
// in-process test reaches — and the failure mode of getting it wrong is silence
// in both directions: a trigger that never fires looks exactly like a workspace
// with nothing to prune, and one that fires in the wrong place takes records a
// consumer was promised.
//
// The test also pins the OFF switch, in the same run: the backlog is built with
// BD_EVENTS_JOURNAL_AUTO_PRUNE=0, and every record survives it. That ordering
// is not incidental — it is what makes the pruning pass below deterministic,
// because the throttle's persisted watermark is never stamped while auto-prune
// is off, so the next command with it on is the first pass this workspace has
// ever run.
func TestAutoPruneBoundsTheJournalAfterAMutatingCommand(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "apr", "--skip-hooks", "--skip-agents")

	// A rows floor of 2 and no age floor, so the retained window is small
	// enough to observe. The floors reach both prune paths through the same
	// config keys.
	base := append(autoPruneTestEnv(dir),
		"BD_EVENTS_JOURNAL=1",
		"BD_EVENTS_JOURNAL_RETAIN_DAYS=0",
		"BD_EVENTS_JOURNAL_RETAIN_ROWS=2",
	)
	runWith := func(env []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	noAutoPrune := append(append([]string{}, base...), "BD_EVENTS_JOURNAL_AUTO_PRUNE=0")

	for i := range 5 {
		if out, err := runWith(noAutoPrune, "create", fmt.Sprintf("backlog %d", i)); err != nil {
			t.Fatalf("create %d: %v\n%s", i, err, out)
		}
	}

	// Auto-prune off means off: five mutations past a floor of two, and the
	// whole journal is still readable from the beginning.
	out, err := runWith(noAutoPrune, "events", "export")
	if err != nil {
		t.Fatalf("export with auto-prune disabled failed — nothing should have been pruned: %v\n%s", err, out)
	}
	backlog := decodeEventRecords(t, out)
	if len(backlog) < 5 {
		t.Fatalf("journal holds %d records after 5 creates with auto-prune off, want them all", len(backlog))
	}

	// One more mutation, with auto-prune at its default. The command itself
	// must still succeed and say nothing about maintenance.
	if out, err := runWith(base, "create", "the trigger"); err != nil {
		t.Fatalf("create with auto-prune enabled: %v\n%s", err, out)
	}

	// The prefix the floor does not protect is gone, and a consumer that asks
	// for it is TOLD so rather than served a suffix as if it were the whole
	// history.
	out, err = runWith(base, "events", "export", "--json")
	if err == nil {
		t.Fatalf("export from seq 0 succeeded after auto-prune; the prefix should be gone\n%s", out)
	}
	var trunc struct {
		Code  string `json:"code"`
		Floor int64  `json:"floor"`
		Head  int64  `json:"head"`
	}
	if decErr := json.Unmarshal([]byte(firstJSONObject(out)), &trunc); decErr != nil {
		t.Fatalf("export failure is not a JSON object: %v\n%s", decErr, out)
	}
	if trunc.Code != storage.EventsJournalTruncatedCode {
		t.Fatalf("code = %q, want %q\n%s", trunc.Code, storage.EventsJournalTruncatedCode, out)
	}
	if trunc.Floor <= 1 {
		t.Fatalf("floor = %d: nothing was pruned by the post-command trigger", trunc.Floor)
	}
	if retained := trunc.Head - trunc.Floor + 1; retained != 2 {
		t.Errorf("journal retained %d records, want the 2 the rows floor protects (floor %d, head %d)",
			retained, trunc.Floor, trunc.Head)
	}

	// A consumer inside the retained window is untouched: it resumes cleanly
	// and gets exactly the records above its checkpoint.
	out, err = runWith(base, "events", "tail", "--since", fmt.Sprint(trunc.Floor-1))
	if err != nil {
		t.Fatalf("a consumer at the floor was refused: %v\n%s", err, out)
	}
	if got := decodeEventRecords(t, out); len(got) != 2 || got[0].Seq != trunc.Floor {
		t.Fatalf("resume from the floor returned %+v, want the 2 retained records from seq %d", got, trunc.Floor)
	}
}

// TestAutoPruneLeavesAnUnboundedJournalAlone. Both floors at 0 is the documented
// way to keep every record, and it is also the configuration the truncation
// tests run under — so a maintenance pass that ignored it would quietly delete
// the history those tests (and any consumer that chose an unbounded ledger)
// depend on.
func TestAutoPruneLeavesAnUnboundedJournalAlone(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "aub", "--skip-hooks", "--skip-agents")
	env := append(autoPruneTestEnv(dir),
		"BD_EVENTS_JOURNAL=1",
		"BD_EVENTS_JOURNAL_RETAIN_DAYS=0",
		"BD_EVENTS_JOURNAL_RETAIN_ROWS=0",
	)
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for i := range 4 {
		if out, err := run("create", fmt.Sprintf("kept %d", i)); err != nil {
			t.Fatalf("create %d: %v\n%s", i, err, out)
		}
	}
	out, err := run("events", "export")
	if err != nil {
		t.Fatalf("export on an unbounded journal: %v\n%s", err, out)
	}
	if records := decodeEventRecords(t, out); len(records) < 4 {
		t.Fatalf("journal holds %d records with both floors disabled, want every one\n%s", len(records), out)
	}
}

// TestAutoPruneRespectsBdConfigSet drives the whole retention story through
// `bd config set` rather than the environment.
//
// The four events-journal keys are startup settings read through viper before
// the store opens, so they have to be written to config.yaml rather than the
// database config table — the failure this feature's own review caught, where
// `bd config set events-journal true` reported success and changed nothing.
// A new key is one YamlOnlyKeys entry away from repeating it, and the symptom
// is invisible: retention that silently keeps whatever the default was.
func TestAutoPruneRespectsBdConfigSet(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "acs", "--skip-hooks", "--skip-agents")
	// No BD_EVENTS_JOURNAL* anywhere: config.yaml is the only thing that can
	// turn any of this on.
	env := autoPruneTestEnv(dir)
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	set := func(key, value string) {
		t.Helper()
		if out, err := run("config", "set", key, value); err != nil {
			t.Fatalf("config set %s %s: %v\n%s", key, value, err, out)
		}
	}

	set("events-journal", "true")
	set("events-journal-auto-prune", "false")
	set("events-journal-retain-days", "0")
	set("events-journal-retain-rows", "2")

	for i := range 5 {
		if out, err := run("create", fmt.Sprintf("configured %d", i)); err != nil {
			t.Fatalf("create %d: %v\n%s", i, err, out)
		}
	}
	out, err := run("events", "export")
	if err != nil {
		t.Fatalf("export with auto-prune disabled in config.yaml: %v\n%s", err, out)
	}
	if records := decodeEventRecords(t, out); len(records) < 5 {
		t.Fatalf("`bd config set events-journal-auto-prune false` did not take: %d records survive a floor of 2",
			len(records))
	}

	set("events-journal-auto-prune", "true")
	if out, err := run("create", "after re-enabling"); err != nil {
		t.Fatalf("create after re-enabling: %v\n%s", err, out)
	}
	if out, err := run("events", "export", "--json"); err == nil {
		t.Fatalf("nothing was pruned after `bd config set events-journal-auto-prune true`\n%s", out)
	}
}

// TestAutoPruneDoesNotRunForReadOnlyCommands. The trigger is writer-pays, and
// three separate gates keep it that way — each one covered here by a command
// that would prune the journal without it:
//
//   - `bd list` is the read classification. A read-only classification still
//     opens a writable store, so nothing underneath refuses the delete.
//   - `bd events tail` is a JOURNAL READER. It is now classified read-only in
//     its own right AND exempted with the whole `bd events` family, because a
//     consumer that trims its own backlog on the way out is the least expected
//     thing this feature could do. `bd events export` used to stand in for
//     this case, and only passed through a leaf-name collision with top-level
//     `bd export` — tail is the honest witness.
//   - `bd migrate --dry-run` is a PREVIEW. It promised not to mutate and holds
//     a write-refusing store; maintenance must not be the one write it
//     performs.
func TestAutoPruneDoesNotRunForReadOnlyCommands(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "aro", "--skip-hooks", "--skip-agents")
	base := append(autoPruneTestEnv(dir),
		"BD_EVENTS_JOURNAL=1",
		"BD_EVENTS_JOURNAL_RETAIN_DAYS=0",
		"BD_EVENTS_JOURNAL_RETAIN_ROWS=2",
	)
	runWith := func(env []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	noAutoPrune := append(append([]string{}, base...), "BD_EVENTS_JOURNAL_AUTO_PRUNE=0")

	for i := range 5 {
		if out, err := runWith(noAutoPrune, "create", fmt.Sprintf("backlog %d", i)); err != nil {
			t.Fatalf("create %d: %v\n%s", i, err, out)
		}
	}

	// Reads and previews, with auto-prune fully enabled and a backlog well past
	// the floor. Each one must leave the journal exactly as it found it.
	for _, args := range [][]string{
		{"list"},
		{"ready"},
		{"events", "tail", "--since", "0"},
		// --before 1 resolves to a bound of 1, which is below the first seq, so
		// the MANUAL prune deletes nothing. Anything missing afterwards was
		// taken by an automatic pass this command should never have triggered —
		// which is the only witness for the events-family exemption that the
		// read-only classification does not already cover.
		{"events", "prune", "--before", "1"},
		{"migrate", "--dry-run"},
	} {
		if out, err := runWith(base, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		out, err := runWith(base, "events", "export")
		if err != nil {
			t.Fatalf("`bd %v` pruned the journal: %v\n%s", args, err, out)
		}
		if records := decodeEventRecords(t, out); len(records) < 5 {
			t.Fatalf("journal holds %d records after `bd %v`, want all 5 still there\n%s",
				len(records), args, strings.TrimSpace(out))
		}
	}
}

// autoPruneTestEnv is bdEnv with the WHOLE BD_EVENTS_JOURNAL* family stripped.
//
// envWithout matches one exact name, and these tests turn the journal, its two
// floors and auto-prune on and off independently — so a developer (or a CI
// image) with any one of the four exported would silently override the case
// under test and the suite would fail, or worse pass, for a reason nothing in
// the file mentions. The prefix is the unit that has to be cleared.
func autoPruneTestEnv(dir string) []string {
	env := bdEnv(dir)
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "BD_EVENTS_JOURNAL") {
			continue
		}
		out = append(out, e)
	}
	return out
}
