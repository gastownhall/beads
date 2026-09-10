//go:build cgo && unix

package main

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
)

// TestEventsJournalProxiedServer is the end-to-end guard for bd's SECOND write
// plumbing. In proxied-server mode no store is ever opened: commands dispatch
// through the unit-of-work provider, whose repositories reach the same issueops
// emit helpers over their own pinned transactions.
//
// Emission at that seam is only half of coverage — the provider must also bind
// journal activation to the transaction the mutation runs in, and until this
// slice it had no activation path at all. That failure is invisible from the
// outside: every command succeeds, every write lands, and the journal is simply
// empty. Only an end-to-end read can see it, which is why this test drives the
// real binary against a real proxied server rather than asserting on wiring.
func TestEventsJournalProxiedServer(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "pjr")

	journalEnv := []string{"BD_EVENTS_JOURNAL=1"}
	run := func(args ...string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, journalEnv, args...)
		if err != nil {
			t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		return stdout
	}

	run("create", "first", "--silent")
	run("create", "second", "--silent")

	records := decodeEventRecords(t, run("events", "export"))
	if len(records) < 2 {
		t.Fatalf("proxied mutations journaled %d records, want at least 2 — the uow plumbing is not scoping journal activation\n%v",
			len(records), records)
	}
	var prev int64
	for i, r := range records {
		if r.Seq <= prev {
			t.Errorf("record %d seq %d not strictly greater than prev %d", i, r.Seq, prev)
		}
		prev = r.Seq
		if r.TS == "" {
			t.Errorf("record %d carries no timestamp", i)
		}
	}
	creates := 0
	for _, r := range records {
		if r.Op == "create" {
			creates++
		}
	}
	if creates != 2 {
		t.Errorf("journaled %d create records, want 2: %+v", creates, records)
	}

	// tail --since resumes from a checkpoint, the operation a consumer actually
	// performs on every poll.
	tailed := decodeEventRecords(t, run("events", "tail", "--since", itoa(records[0].Seq)))
	if len(tailed) != len(records)-1 || tailed[0].Seq != records[0].Seq+1 {
		t.Fatalf("tail --since %d = %+v, want the records after it", records[0].Seq, tailed)
	}

	// prune runs through the provider's own transaction (an ephemeral commit,
	// since the journal table is dolt_ignored) and honors the floors.
	head := records[len(records)-1].Seq
	pruneEnv := append(append([]string{}, journalEnv...),
		"BD_EVENTS_JOURNAL_RETAIN_DAYS=0", "BD_EVENTS_JOURNAL_RETAIN_ROWS=0")
	stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, pruneEnv,
		"events", "prune", "--before", itoa(head), "--json")
	if err != nil {
		t.Fatalf("bd events prune: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var pruned struct {
		Pruned int64 `json:"pruned"`
	}
	if decErr := json.Unmarshal([]byte(firstJSONObject(stdout)), &pruned); decErr != nil {
		t.Fatalf("prune output is not a JSON object: %v\n%s", decErr, stdout)
	}
	if want := int64(len(records) - 1); pruned.Pruned != want {
		t.Errorf("prune deleted %d records below seq %d, want %d", pruned.Pruned, head, want)
	}

	// And the truncation contract reaches the proxied read path too: a
	// checkpoint below the surviving floor fails typed rather than presenting
	// the remaining suffix as a whole history.
	stdout, stderr, err = bdProxiedRunBuffersWithEnv(t, bd, p.dir, journalEnv, "events", "export", "--json")
	if err == nil {
		t.Fatalf("export from a pruned-past checkpoint succeeded; want a truncation failure\nstdout:\n%s", stdout)
	}
	var trunc struct {
		Code string `json:"code"`
	}
	if decErr := json.Unmarshal([]byte(firstJSONObject(stdout+stderr)), &trunc); decErr != nil {
		t.Fatalf("truncation output is not a JSON object: %v\nstdout:\n%s\nstderr:\n%s", decErr, stdout, stderr)
	}
	if trunc.Code != storage.EventsJournalTruncatedCode {
		t.Errorf("code = %q, want %q\nstdout:\n%s\nstderr:\n%s", trunc.Code, storage.EventsJournalTruncatedCode, stdout, stderr)
	}
}

// TestEventsJournalProxiedServerOffByDefault pins the default: a proxied
// workspace that never asked for the journal writes no rows, so the feature
// costs an ordinary team nothing.
func TestEventsJournalProxiedServerOffByDefault(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "pjo")

	// Explicitly unset, not merely absent: a developer with BD_EVENTS_JOURNAL
	// exported would otherwise turn this assertion into a false green.
	clean := envWithout(bdProxiedEnv(p.dir), "BD_EVENTS_JOURNAL")
	mustRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = p.dir
		cmd.Env = clean
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	mustRun("create", "untracked work", "--silent")

	if got := strings.TrimSpace(mustRun("events", "export")); got != "" {
		t.Errorf("journal recorded %q with events-journal unset; it must be off by default", got)
	}
}

func decodeEventRecords(t *testing.T, out string) []eventsjournal.Record {
	t.Helper()
	var records []eventsjournal.Record
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var rec eventsjournal.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode journal record %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
