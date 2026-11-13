//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func TestContextWithReaderEOF(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	ctx, cancel := contextWithReaderEOF(context.Background(), reader)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("context canceled before the stdin sentinel closed")
	case <-time.After(25 * time.Millisecond):
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin sentinel: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context remained live after the stdin sentinel reached EOF")
	}
}

func TestEventsTailExitOnStdinEOFRequiresFollow(t *testing.T) {
	cmd := eventsTailCmd
	resetCommandFlags(cmd)
	t.Cleanup(func() { resetCommandFlags(cmd) })
	// A live root context: without one, a run that got past the refusal
	// would panic on a nil parent in contextWithReaderEOF, and the test would
	// fail on the panic instead of on its own assertions.
	oldRootCtx, oldJSON := rootCtx, jsonOutput
	t.Cleanup(func() { rootCtx, jsonOutput = oldRootCtx, oldJSON })
	rootCtx, jsonOutput = context.Background(), false

	if err := cmd.ParseFlags([]string{"--exit-on-stdin-eof"}); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { err = cmd.RunE(cmd, nil) })
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("RunE = %v, want exit 1: --exit-on-stdin-eof requires --follow\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "--exit-on-stdin-eof requires --follow") {
		t.Errorf("the refusal does not say what is wrong:\n%s", stderr)
	}
}

// stopOnReadStore is a DoltStorage stand-in for runEventsTail's follower:
// each ReadEventsJournal call runs the next scripted read, so a test can land
// the follower's stop (Ctrl-C, or stdin EOF under --exit-on-stdin-eof) on a
// chosen read. It embeds a nil DoltStorage for the type and implements only
// the journal accessor that journalAccessor asserts for.
type stopOnReadStore struct {
	storage.DoltStorage
	reads []func(context.Context) ([]storage.EventsJournalRow, error)
	calls int
}

func (s *stopOnReadStore) ReadEventsJournal(ctx context.Context, _ int64, _ int) ([]storage.EventsJournalRow, error) {
	if s.calls >= len(s.reads) {
		return nil, fmt.Errorf("unscripted journal read %d", s.calls+1)
	}
	read := s.reads[s.calls]
	s.calls++
	return read(ctx)
}

func (s *stopOnReadStore) PruneEventsJournal(context.Context, int64, int, int) (int64, error) {
	return 0, errors.New("stopOnReadStore does not prune")
}

func (s *stopOnReadStore) JournalHead(context.Context) (int64, int64, error) {
	return 0, 0, errors.New("stopOnReadStore does not report a head")
}

// TestEventsTailFollowStopMidRead pins how a follower classifies a read that
// fails on the same turn as its stop, in both of runEventsTail's read sites:
// the catch-up read and the poll. A read the stop canceled is a clean stop,
// exit 0, however the cancellation surfaces. A truncation the read had
// already found is not a stop: the consumer still gets the report and a
// non-zero exit, because a clean exit would tell it the stream had no gap.
// A read that fails while the follower is still running is no stop at all.
//
// It reads stdout the way the follower's consumer does, line by line: a
// failure is one compact JSON line after the records already emitted,
// whichever read it lands on and in either --json mode. A failed read is
// bd's usual --json error object, a truncation the events_journal_truncated
// line.
func TestEventsTailFollowStopMidRead(t *testing.T) {
	t.Setenv("BD_EVENTS_JOURNAL", "1") // no disabled-journal notice
	oldStore, oldJSON, oldProxied := store, jsonOutput, proxiedServerMode
	t.Cleanup(func() { store, jsonOutput, proxiedServerMode = oldStore, oldJSON, oldProxied })
	jsonOutput, proxiedServerMode = true, false

	truncated := &storage.EventsJournalTruncatedError{Since: 0, Floor: 4, Head: 5}
	canceled := func(ctx context.Context) error { return fmt.Errorf("journal: read since 0: %w", ctx.Err()) }
	badConn := func(context.Context) error { return errors.New("journal: read since 0: driver: bad connection") }
	for _, tc := range []struct {
		name string
		// poll lands the stop on the first poll; otherwise on the catch-up read.
		poll bool
		// readErr is what the read the stop landed on returns.
		readErr func(ctx context.Context) error
		// live leaves the follower running: the read fails on its own, with
		// no stop, so it is a real failure rather than a clean stop.
		live          bool
		wantTruncated bool
		// envelope runs the follower with BD_JSON_ENVELOPE=1.
		envelope bool
	}{
		{name: "catch-up read canceled", readErr: canceled},
		// A canceled read can also surface without the context error: the
		// transaction was rolled back under it.
		{name: "catch-up read rolled back", readErr: func(context.Context) error {
			return errors.Join(errors.New("journal: read since 0: driver: bad connection"), sql.ErrTxDone)
		}},
		{name: "catch-up read truncated", readErr: func(context.Context) error { return truncated }, wantTruncated: true},
		{name: "catch-up read truncated, envelope", envelope: true, wantTruncated: true, readErr: func(context.Context) error { return truncated }},
		{name: "catch-up read fails without a stop", live: true, readErr: badConn},
		{name: "poll canceled", poll: true, readErr: canceled},
		{name: "poll truncated", poll: true, wantTruncated: true, readErr: func(context.Context) error {
			return errors.Join(truncated, sql.ErrTxDone)
		}},
		{name: "poll fails without a stop", poll: true, live: true, readErr: badConn},
		{name: "poll fails without a stop, envelope", poll: true, live: true, envelope: true, readErr: badConn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := ""
			if tc.envelope {
				envelope = "1"
			}
			t.Setenv("BD_JSON_ENVELOPE", envelope)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &stopOnReadStore{}
			records := 0
			if tc.poll {
				// The catch-up read emits a record, so the poll fails on a
				// stream that already carries one.
				records = 1
				fake.reads = append(fake.reads, func(context.Context) ([]storage.EventsJournalRow, error) {
					return []storage.EventsJournalRow{{Seq: 1, Op: "create", IssueID: "bd-1"}}, nil
				})
			}
			fake.reads = append(fake.reads, func(ctx context.Context) ([]storage.EventsJournalRow, error) {
				if !tc.live {
					cancel()
				}
				return nil, tc.readErr(ctx)
			})
			store = fake

			var err error
			out := captureStdout(t, func() error {
				err = runEventsTail(ctx, 0, 0, true)
				return nil
			})
			if fake.calls != len(fake.reads) {
				t.Fatalf("follower made %d journal read(s), want %d", fake.calls, len(fake.reads))
			}
			lines := followerStdoutLines(t, out)
			if !tc.live && !tc.wantTruncated {
				if err != nil {
					t.Fatalf("runEventsTail = %v, want nil: a read the stop canceled is a clean stop\nstdout:\n%s", err, out)
				}
				if len(lines) != records {
					t.Errorf("stdout has %d line(s), want only the %d record(s) emitted before the stop\nstdout:\n%s", len(lines), records, out)
				}
				return
			}
			why := "a truncation found on the stop's turn must still fail the follower"
			if tc.live {
				why = "a read that fails with no stop is not a clean stop"
			}
			if code, ok := exitCodeFromError(err); !ok || code != 1 {
				t.Fatalf("runEventsTail = %v, want exit 1: %s\nstdout:\n%s", err, why, out)
			}
			if len(lines) != records+1 {
				t.Fatalf("stdout has %d line(s), want %d record(s) and then the failure on one line\nstdout:\n%s", len(lines), records, out)
			}
			for i, rec := range lines[:records] {
				if rec["seq"] != float64(i+1) {
					t.Errorf("stdout line %d = %v, want the record with seq %d\nstdout:\n%s", i+1, rec, i+1, out)
				}
			}
			last := lines[records]
			if tc.wantTruncated {
				// The stream's own framing, envelope or not.
				if last["code"] != storage.EventsJournalTruncatedCode || last["floor"] != float64(truncated.Floor) {
					t.Errorf("failure line = %v, want the %s report with floor %d\nstdout:\n%s",
						last, storage.EventsJournalTruncatedCode, truncated.Floor, out)
				}
				return
			}
			// bd's usual --json error object: error and schema_version, with the
			// error inside data under the envelope.
			if last["schema_version"] != float64(JSONSchemaVersion) {
				t.Errorf("failure line has schema_version %v, want %d\nstdout:\n%s", last["schema_version"], JSONSchemaVersion, out)
			}
			payload := last
			if tc.envelope {
				payload, _ = last["data"].(map[string]any)
			}
			if msg, _ := payload["error"].(string); !strings.HasPrefix(msg, "reading events journal: ") || !strings.Contains(msg, "bad connection") {
				t.Errorf("failure line = %v, want the error reading events journal: ... bad connection\nstdout:\n%s", last, out)
			}
		})
	}
}

// followerStdoutLines decodes a follower's stdout the way its consumer reads
// it, one line at a time, failing the test on any line that is not a whole
// JSON object by itself: an indented object is valid JSON as a document and
// still unreadable to a line reader.
func followerStdoutLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	if out == "" {
		return nil
	}
	var objs []map[string]any
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("stdout line %d is not a JSON object on its own, so a line reader cannot parse it: %v\nline: %q\nstdout:\n%s", i+1, err, line, out)
		}
		objs = append(objs, obj)
	}
	return objs
}

// TestEmbeddedEventsTailFollowExitsOnStdinEOF pins #7086's lifecycle
// contract end to end: with --exit-on-stdin-eof, a quiet follower stays up
// while its caller-owned stdin is open and exits 0 once it closes, whenever
// the EOF lands, without waiting for a journal record to fail a stdout write.
func TestEmbeddedEventsTailFollowExitsOnStdinEOF(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "eof", "--skip-hooks", "--skip-agents")
	env := append(bdEnv(dir), "BD_EVENTS_JOURNAL=1")
	// One journal record, so the follower can show it is through startup and
	// its catch-up read before anything below is timed.
	create := exec.Command(bd, "create", "sentinel")
	create.Dir = dir
	create.Env = env
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	follow := exec.Command(bd, "events", "tail", "--since", "0", "--follow", "--exit-on-stdin-eof")
	follow.Dir = dir
	follow.Env = env
	stdin, err := follow.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var stdout, stderr syncBuffer
	follow.Stdout = &stdout
	follow.Stderr = &stderr
	if err := follow.Start(); err != nil {
		t.Fatalf("start follower: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- follow.Wait() }()
	t.Cleanup(func() {
		if follow.ProcessState == nil {
			_ = follow.Process.Kill()
		}
	})

	// The record on stdout puts the follower in its poll loop, however long
	// its startup took.
	emitted := time.After(30 * time.Second)
	for strings.TrimSpace(stdout.String()) == "" {
		select {
		case err := <-done:
			t.Fatalf("follower exited before emitting the journal record: %v\nstderr:\n%s", err, stderr.String())
		case <-emitted:
			t.Fatalf("follower emitted no journal record within 30s\nstderr:\n%s", stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}

	// A quiet follower must keep polling while its caller-owned sentinel is
	// open, then exit 0 when that pipe closes without needing a journal
	// record to provoke a stdout write.
	select {
	case err := <-done:
		t.Fatalf("follower exited before stdin EOF: %v\nstderr:\n%s", err, stderr.String())
	case <-time.After(2 * eventFollowPollInterval):
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close follower stdin: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follower exit after stdin EOF: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("follower remained alive after stdin EOF\nstderr:\n%s", stderr.String())
	}

	// The EOF can land while a read is in flight: here stdin is the null
	// device, so it is at EOF before the first read finishes, as when the
	// parent dies during bd's startup. That stop must be just as clean.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	early := exec.CommandContext(ctx, bd, "events", "tail", "--since", "0", "--follow", "--exit-on-stdin-eof")
	early.Dir = dir
	early.Env = env
	if out, err := early.CombinedOutput(); err != nil {
		t.Fatalf("follower whose stdin was at EOF from the start: %v\n%s", err, out)
	}
}

// TestEmbeddedEventsTailReportsTruncationToTheCLI is the end-to-end guard for the
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
func TestEmbeddedEventsTailReportsTruncationToTheCLI(t *testing.T) {
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

	// A follower started on the pruned checkpoint finds the truncation on its
	// first read. Its consumer is the line reader every later poll reports to,
	// so the report is the stream's one line, not the indented object above.
	followCtx, cancelFollow := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelFollow()
	follow := exec.CommandContext(followCtx, bd, "events", "tail", "--since", "0", "--follow", "--json")
	follow.Dir = dir
	follow.Env = env
	var followOut, followErr strings.Builder
	follow.Stdout = &followOut
	follow.Stderr = &followErr
	if err := follow.Run(); err == nil {
		t.Fatalf("a follower on a pruned-past checkpoint exited 0; want a truncation failure\nstdout:\n%s", followOut.String())
	} else if followCtx.Err() != nil {
		t.Fatalf("a follower on a pruned-past checkpoint kept polling; want a truncation failure\nstdout:\n%s\nstderr:\n%s",
			followOut.String(), followErr.String())
	}
	var followGot struct {
		Code  string `json:"code"`
		Since int64  `json:"since"`
		Floor int64  `json:"floor"`
		Head  int64  `json:"head"`
	}
	line := strings.TrimSuffix(followOut.String(), "\n")
	if strings.Contains(line, "\n") || json.Unmarshal([]byte(line), &followGot) != nil {
		t.Fatalf("the follower's truncation report is not one JSON line, so a line reader cannot parse it:\n%s", followOut.String())
	}
	if followGot.Code != storage.EventsJournalTruncatedCode || followGot.Since != 0 || followGot.Floor != 4 || followGot.Head != 5 {
		t.Errorf("follower report = %+v, want %s with since/floor/head 0/4/5\n%s", followGot, storage.EventsJournalTruncatedCode, line)
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

// TestEmbeddedEventsTailFollowReportsTruncationOnTheStream covers the OTHER read call
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
func TestEmbeddedEventsTailFollowReportsTruncationOnTheStream(t *testing.T) {
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
	var stdout, stderr syncBuffer
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

// TestEmbeddedEventsPruneHonorsRetainRowsFloorFromConfig proves the shipped defaults
// reach the CLI: a `prune --before <huge>` on a workspace that configured no
// floors of its own must be stopped by the built-in retain-rows floor rather
// than deleting the whole journal and stranding its consumer.
func TestEmbeddedEventsPruneHonorsRetainRowsFloorFromConfig(t *testing.T) {
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
