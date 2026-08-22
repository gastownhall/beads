//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// TestEventsTailReportsTruncationToTheCLI is the end-to-end guard for the
// retention boundary as a consumer actually meets it: through the binary, not
// through the store API.
//
// It exists because the store returning a typed error is only half the
// contract — the CLI has two read call sites (the one-shot read and the
// --follow poll) and wiring only one of them still lets `bd events export`
// present a pruned journal as a complete history. A store-level test cannot see
// that; this one fails if either path regresses. It also proves the whole
// config chain end to end: BD_EVENTS_JOURNAL turns the journal on, and the two
// BD_EVENTS_JOURNAL_RETAIN_* floors reach the prune.
func TestEventsTailReportsTruncationToTheCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "evt", "--skip-hooks", "--skip-agents")

	// Disable both retention floors so the prune can actually reach the rows;
	// the shipped defaults are non-zero precisely to prevent this by accident.
	env := append(bdEnv(dir),
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

	for _, title := range []string{"one", "two", "three", "four", "five"} {
		if out, err := run("create", title); err != nil {
			t.Fatalf("create %s: %v\n%s", title, err, out)
		}
	}
	if out, err := run("events", "prune", "--before", "4"); err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}

	// Both read commands must fail rather than serve the surviving suffix.
	for _, args := range [][]string{
		{"events", "export", "--json"},
		{"events", "tail", "--since", "0", "--json"},
	} {
		out, err := run(args...)
		if err == nil {
			t.Fatalf("%v succeeded on a pruned-past checkpoint; want a truncation failure\n%s", args, out)
		}
		var got struct {
			Code  string `json:"code"`
			Since int64  `json:"since"`
			Floor int64  `json:"floor"`
			Head  int64  `json:"head"`
		}
		if decErr := json.Unmarshal([]byte(firstJSONObject(out)), &got); decErr != nil {
			t.Fatalf("%v: output is not a JSON object: %v\n%s", args, decErr, out)
		}
		if got.Code != storage.EventsJournalTruncatedCode {
			t.Errorf("%v: code = %q, want %q\n%s", args, got.Code, storage.EventsJournalTruncatedCode, out)
		}
		if got.Since != 0 || got.Floor != 4 || got.Head != 5 {
			t.Errorf("%v: since/floor/head = %d/%d/%d, want 0/4/5\n%s", args, got.Since, got.Floor, got.Head, out)
		}
	}

	// Resuming from the retained floor-1 still works and returns the surviving
	// records — a truncation must not be a dead end.
	out, err := run("events", "tail", "--since", "3")
	if err != nil {
		t.Fatalf("resume from floor-1: %v\n%s", err, out)
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 2 {
		t.Errorf("resume from floor-1 emitted %d records, want 2\n%s", lines, out)
	}

	// A negative checkpoint is refused rather than silently serving everything:
	// `seq > -5` matches every row, so a consumer whose cursor arithmetic
	// underflowed would re-deliver the whole journal and call it a resume.
	out, err = run("events", "tail", "--since", "-5")
	if err == nil {
		t.Errorf("--since -5 succeeded; want a refusal\n%s", out)
	}
	if !strings.Contains(out, "--since must be zero or a positive sequence number") {
		t.Errorf("--since -5 error does not say what is wrong:\n%s", out)
	}
}

// TestEventsTailFollowReportsTruncationOnTheStream covers the OTHER read call
// site: the --follow poll, which the one-shot test above cannot reach.
//
// A follower is the consumer most exposed to a prune — it is the one sitting
// idle at a checkpoint while an operator runs `bd events prune`. Two things
// have to hold when that happens mid-stream, and neither is implied by the
// one-shot path: the poll must FAIL rather than silently skip to the new floor,
// and the failure must arrive as ONE JSON object on a line of the JSONL stream
// it interrupts. The consumer on the other end is a line reader; a
// pretty-printed multi-line object in the middle of its input is unparseable,
// and it would see the stream stop with garbage after the last good record.
func TestEventsTailFollowReportsTruncationOnTheStream(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "flw", "--skip-hooks", "--skip-agents")
	env := append(bdEnv(dir),
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

	// A backlog, and a follower rate-limited to one record per poll. That is
	// what makes this deterministic rather than a race: a follower with no
	// backlog is always caught up between polls, and a caught-up follower
	// CANNOT be stranded — a prune that takes its whole window still leaves it
	// at the head, which is a legitimate empty success. Only a consumer still
	// BELOW the head can be pruned past, so the test has to keep it there.
	const (
		backlog    = 12
		pruneBelow = 10
	)
	for i := 0; i < backlog; i++ {
		if out, err := run("create", fmt.Sprintf("backlog %d", i)); err != nil {
			t.Fatalf("create %d: %v\n%s", i, err, out)
		}
	}

	// --limit 1: one record per poll, so the follower advances at ~1 seq/second
	// and the prune below has the whole backlog as slack.
	follow := exec.Command(bd, "events", "tail", "--since", "0", "--limit", "1", "--follow", "--json")
	follow.Dir = dir
	follow.Env = env
	var stdout, stderr bytes.Buffer
	follow.Stdout = &stdout
	follow.Stderr = &stderr
	if err := follow.Start(); err != nil {
		t.Fatalf("start follower: %v", err)
	}
	t.Cleanup(func() {
		if follow.ProcessState == nil {
			_ = follow.Process.Kill()
		}
	})

	// Wait for the first record — proof the stream is live, so the failure below
	// interrupts an established JSONL stream rather than replacing it — then
	// prune the prefix out from under the follower while it is still down there.
	waitFor(t, 30*time.Second, 50*time.Millisecond, func() bool {
		return strings.TrimSpace(stdout.String()) != ""
	})
	if out, err := run("events", "prune", "--before", strconv.Itoa(pruneBelow)); err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}

	done := make(chan error, 1)
	go func() { done <- follow.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("the follower exited 0 after its window was pruned; want a truncation failure\nstdout:\n%s", stdout.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("the follower kept polling after its window was pruned — it will stall forever on an empty success\nstdout:\n%s\nstderr:\n%s",
			stdout.String(), stderr.String())
	}

	// The final line of the stream is the failure, framed as the stream is.
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	var got struct {
		Code  string `json:"code"`
		Since int64  `json:"since"`
		Floor int64  `json:"floor"`
		Head  int64  `json:"head"`
	}
	if err := json.Unmarshal([]byte(last), &got); err != nil {
		t.Fatalf("the follower's last stdout line is not a single JSON object — a line reader cannot parse it: %v\nline: %q\nfull stdout:\n%s",
			err, last, stdout.String())
	}
	if got.Code != storage.EventsJournalTruncatedCode {
		t.Errorf("code = %q, want %q\nline: %s", got.Code, storage.EventsJournalTruncatedCode, last)
	}
	// The records it did emit must all still parse as records: the failure line
	// must not have corrupted the stream before it.
	for i, line := range lines[:len(lines)-1] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("stream line %d is not a JSON object: %v\nline: %q", i, err, line)
		}
	}
}

// TestEventsPruneHonorsRetainRowsFloorFromConfig proves the shipped defaults
// reach the CLI: a `prune --before <huge>` on a workspace that configured no
// floors of its own must be stopped by the built-in retain-rows floor rather
// than deleting the whole journal and stranding its consumer.
func TestEventsPruneHonorsRetainRowsFloorFromConfig(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "flr", "--skip-hooks", "--skip-agents")

	// Enable the journal through `bd config set` rather than the environment:
	// this is review blocker 6, where the key was accepted and silently ignored
	// because it is read from yaml at startup, not from the database.
	env := bdEnv(dir)
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("config", "set", "events-journal", "true"); err != nil {
		t.Fatalf("config set events-journal: %v\n%s", err, out)
	}

	for _, title := range []string{"one", "two", "three"} {
		if out, err := run("create", title); err != nil {
			t.Fatalf("create %s: %v\n%s", title, err, out)
		}
	}
	out, err := run("events", "export", "--json")
	if err != nil {
		t.Fatalf("export after `bd config set events-journal true`: %v\n%s", err, out)
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 3 {
		t.Fatalf("`bd config set events-journal true` journaled %d records, want 3 — the key was accepted and ignored\n%s", lines, out)
	}

	if out, err := run("events", "prune", "--before", "1000000", "--json"); err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	} else {
		var pruned struct {
			Pruned int64 `json:"pruned"`
		}
		if decErr := json.Unmarshal([]byte(firstJSONObject(out)), &pruned); decErr != nil {
			t.Fatalf("prune output is not a JSON object: %v\n%s", decErr, out)
		}
		if pruned.Pruned != 0 {
			t.Errorf("prune deleted %d records despite the default retain-rows floor, want 0\n%s", pruned.Pruned, out)
		}
	}
	after, err := run("events", "export", "--json")
	if err != nil {
		t.Fatalf("export after prune: %v\n%s", err, after)
	}
	if lines := strings.Count(strings.TrimSpace(after), "\n") + 1; lines != 3 {
		t.Errorf("journal holds %d records after a floored prune, want 3\n%s", lines, after)
	}
}

// firstJSONObject extracts the JSON object from combined output, tolerating any
// non-JSON preamble the binary may log around it.
func firstJSONObject(out string) string {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return out
	}
	return out[start : end+1]
}
