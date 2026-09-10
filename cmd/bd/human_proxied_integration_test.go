//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func bdProxiedHuman(t *testing.T, bd, dir string, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"human"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd human %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

func bdProxiedHumanFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"human"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd human %s to fail, but succeeded:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedLabelAdd(t *testing.T, bd, dir, id, label string) {
	t.Helper()
	cmd := exec.Command(bd, "label", "add", id, label)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd label add %s %s failed: %v\n%s", id, label, err, out)
	}
}

// TestProxiedServerHuman covers the bd-m7zzd port of the four `bd human`
// subcommands to proxied-server mode. Before it, each of them called
// ensureStoreActive(), which in proxied mode lazily opened a DIRECT store —
// silently bypassing the proxy, so writes landed outside the proxied commit
// scoping. The dolt_log assertions below are the behavioral check that the
// writes now ride the managed server's own commit plane, one commit with a
// real message per write invocation.
func TestProxiedServerHuman(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("list_and_stats_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm1")
		out, _ := bdProxiedHuman(t, bd, p.dir, "list")
		if !strings.Contains(out, "No human-needed beads found") {
			t.Errorf("expected empty human list message: %s", out)
		}
		statsOut, _ := bdProxiedHuman(t, bd, p.dir, "stats")
		if !strings.Contains(statsOut, "Total:") || !strings.Contains(statsOut, "0") {
			t.Errorf("expected zeroed stats output: %s", statsOut)
		}
	})

	t.Run("respond_journey", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm2")
		issue := bdProxiedCreate(t, bd, p.dir, "Needs a human", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, issue.ID, "human")

		listOut, _ := bdProxiedHuman(t, bd, p.dir, "list")
		if !strings.Contains(listOut, issue.ID) {
			t.Errorf("expected %s in human list:\n%s", issue.ID, listOut)
		}

		db := openProxiedDB(t, p)
		head := readDoltHead(t, db)

		out, _ := bdProxiedHuman(t, bd, p.dir, "respond", issue.ID, "--response", "Use OAuth2")
		if !strings.Contains(out, "closed with response") {
			t.Errorf("expected respond confirmation: %s", out)
		}

		// The close and the comment are visible through the PROXIED read
		// path — the write went through the proxy, not a direct store.
		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(after.Status) != "closed" {
			t.Errorf("expected closed status after respond, got %s", after.Status)
		}
		if after.CloseReason != "Responded" {
			t.Errorf("expected close reason 'Responded', got %q", after.CloseReason)
		}
		commentsOut, err := bdProxiedRun(t, bd, p.dir, "comments", issue.ID)
		if err != nil {
			t.Fatalf("bd comments failed: %v\n%s", err, commentsOut)
		}
		if !strings.Contains(string(commentsOut), "Response: Use OAuth2") {
			t.Errorf("expected response comment:\n%s", commentsOut)
		}

		// Comment + close landed as ONE commit on the managed server, with
		// the invocation's own message.
		if n := readDoltLogCountSince(t, db, head); n != 1 {
			t.Errorf("expected exactly 1 commit for respond, got %d", n)
		}
		if msg := readDoltLogTopMessage(t, db); !strings.Contains(msg, "bd: human respond") {
			t.Errorf("expected 'bd: human respond' commit message, got %q", msg)
		}
	})

	t.Run("dismiss_journey", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm3")
		issue := bdProxiedCreate(t, bd, p.dir, "Dismiss me", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, issue.ID, "human")

		db := openProxiedDB(t, p)
		head := readDoltHead(t, db)

		out, _ := bdProxiedHuman(t, bd, p.dir, "dismiss", issue.ID, "--reason", "Not needed")
		if !strings.Contains(out, "dismissed") {
			t.Errorf("expected dismiss confirmation: %s", out)
		}

		after := bdProxiedShow(t, bd, p.dir, issue.ID)
		if string(after.Status) != "closed" {
			t.Errorf("expected closed status after dismiss, got %s", after.Status)
		}
		if after.CloseReason != "Dismissed: Not needed" {
			t.Errorf("expected close reason 'Dismissed: Not needed', got %q", after.CloseReason)
		}

		if n := readDoltLogCountSince(t, db, head); n != 1 {
			t.Errorf("expected exactly 1 commit for dismiss, got %d", n)
		}
		if msg := readDoltLogTopMessage(t, db); !strings.Contains(msg, "bd: human dismiss") {
			t.Errorf("expected 'bd: human dismiss' commit message, got %q", msg)
		}
	})

	t.Run("respond_without_label_warns", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm4")
		issue := bdProxiedCreate(t, bd, p.dir, "Unlabeled", "--type", "task")
		_, stderr := bdProxiedHuman(t, bd, p.dir, "respond", issue.ID, "--response", "ok")
		if !strings.Contains(stderr, "does not have 'human' label") {
			t.Errorf("expected missing-label warning on stderr: %s", stderr)
		}
	})

	t.Run("respond_refusals", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm5")

		out := bdProxiedHumanFail(t, bd, p.dir, "respond", "hm5-nonexistent", "--response", "ok")
		if !strings.Contains(out, "issue not found") {
			t.Errorf("expected 'issue not found' refusal: %s", out)
		}
		if strings.Contains(out, "storage is nil") {
			t.Errorf("opaque 'storage is nil' error leaked through: %s", out)
		}

		issue := bdProxiedCreate(t, bd, p.dir, "Already closed", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, issue.ID, "human")
		bdProxiedHuman(t, bd, p.dir, "dismiss", issue.ID)
		out = bdProxiedHumanFail(t, bd, p.dir, "respond", issue.ID, "--response", "late")
		if !strings.Contains(out, "already closed") {
			t.Errorf("expected already-closed refusal: %s", out)
		}
	})

	t.Run("stats_counts", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm6")

		pending := bdProxiedCreate(t, bd, p.dir, "Pending", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, pending.ID, "human")
		responded := bdProxiedCreate(t, bd, p.dir, "Responded", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, responded.ID, "human")
		dismissed := bdProxiedCreate(t, bd, p.dir, "Dismissed", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, dismissed.ID, "human")

		bdProxiedHuman(t, bd, p.dir, "respond", responded.ID, "--response", "done")
		bdProxiedHuman(t, bd, p.dir, "dismiss", dismissed.ID)

		out, _ := bdProxiedHuman(t, bd, p.dir, "stats")
		for _, want := range []string{"Total:      3", "Pending:    1", "Responded:  1", "Dismissed:  1"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in stats output:\n%s", want, out)
			}
		}

		// And the closed pair drops out of the open list.
		listOut, _ := bdProxiedHuman(t, bd, p.dir, "list", "--status", "open")
		if !strings.Contains(listOut, pending.ID) {
			t.Errorf("expected %s in open human list:\n%s", pending.ID, listOut)
		}
		if strings.Contains(listOut, responded.ID) || strings.Contains(listOut, dismissed.ID) {
			t.Errorf("closed beads leaked into open human list:\n%s", listOut)
		}
	})

	// A human-labeled bead can be a WISP: `bd human list` shows the whole
	// ephemeral plane, so a bead a person can SEE here must be one they can
	// also ANSWER. Before the wisp branch, respond resolved the wisp fine and
	// then wrote its comment and close against the durable tables, where the
	// row does not exist.
	t.Run("respond_on_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm7")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp question", "--ephemeral", "--labels", "human")

		// No label warning: the label load has to reach the WISP plane too,
		// and a failed load must stay silent rather than claim the label is
		// missing.
		_, stderr := bdProxiedHuman(t, bd, p.dir, "respond", wisp.ID, "wisp answer")
		if strings.Contains(stderr, "does not have 'human' label") {
			t.Errorf("unexpected label warning for a human-labeled wisp:\n%s", stderr)
		}

		db := openProxiedDB(t, p)
		if got := readStatus(t, db, wisp.ID); got != types.StatusClosed {
			t.Errorf("expected closed wisp, got status %q", got)
		}
		var wispComments int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?", wisp.ID).Scan(&wispComments); err != nil {
			t.Fatalf("count wisp_comments: %v", err)
		}
		if wispComments != 1 {
			t.Errorf("wisp_comments count = %d, want 1: the comment must land on the wisp plane", wispComments)
		}
	})

	// The proxied list must apply the SAME filter as the direct one: closed
	// hidden by default, --status=all lifting it, an invalid selector refused
	// rather than silently returning nothing.
	t.Run("list_filters_and_status_selector", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm8")
		open := bdProxiedCreate(t, bd, p.dir, "Open human bead", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, open.ID, "human")
		done := bdProxiedCreate(t, bd, p.dir, "Done human bead", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, done.ID, "human")
		other := bdProxiedCreate(t, bd, p.dir, "Not for humans", "--type", "task")
		bdProxiedHuman(t, bd, p.dir, "dismiss", done.ID)

		out, _ := bdProxiedHuman(t, bd, p.dir, "list")
		if !strings.Contains(out, open.ID) {
			t.Errorf("expected open human bead in list, got:\n%s", out)
		}
		if strings.Contains(out, done.ID) || strings.Contains(out, other.ID) {
			t.Errorf("expected closed and unlabeled beads hidden, got:\n%s", out)
		}

		all, _ := bdProxiedHuman(t, bd, p.dir, "list", "--status=all")
		if !strings.Contains(all, open.ID) || !strings.Contains(all, done.ID) {
			t.Errorf("expected --status=all to show open and closed beads, got:\n%s", all)
		}

		if out := bdProxiedHumanFail(t, bd, p.dir, "list", "--status=colsed"); !strings.Contains(out, "invalid status") {
			t.Errorf("expected invalid-status error, got:\n%s", out)
		}

		jsonOut, _ := bdProxiedHuman(t, bd, p.dir, "list", "--json")
		start := strings.Index(jsonOut, "[")
		if start < 0 {
			t.Fatalf("no JSON array in human list output:\n%s", jsonOut)
		}
		var issues []*types.Issue
		if err := json.Unmarshal([]byte(jsonOut[start:]), &issues); err != nil {
			t.Fatalf("parse human list JSON: %v\nraw: %s", err, jsonOut[start:])
		}
		if len(issues) != 1 || issues[0].ID != open.ID {
			t.Fatalf("expected exactly the open human bead in JSON, got %+v", issues)
		}
		// The list no longer re-fetches labels for the JSON path; the search
		// already hydrated them.
		if !slices.Contains(issues[0].Labels, "human") {
			t.Errorf("expected hydrated labels in JSON, got %v", issues[0].Labels)
		}
	})

	// Positional response/reason text has to reach the proxied backend the
	// same way the flag does — the text sources are resolved BEFORE the
	// proxied dispatch, so there is one reading for both routes.
	t.Run("positional_text_reaches_the_proxy", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hm9")

		answered := bdProxiedCreate(t, bd, p.dir, "Positional respond", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, answered.ID, "human")
		bdProxiedHuman(t, bd, p.dir, "respond", answered.ID, "Use", "OAuth2")
		commentsOut, _, err := bdProxiedRunBuffers(t, bd, p.dir, "comments", "--json", answered.ID)
		if err != nil {
			t.Fatalf("bd comments --json failed: %v\n%s", err, commentsOut)
		}
		var comments []types.Comment
		if err := json.Unmarshal([]byte(commentsOut), &comments); err != nil {
			t.Fatalf("decode comments JSON: %v\nraw: %q", err, commentsOut)
		}
		if len(comments) != 1 || comments[0].Text != "Response: Use OAuth2" {
			t.Fatalf("expected 1 response comment with the joined text, got %+v", comments)
		}

		dropped := bdProxiedCreate(t, bd, p.dir, "Positional dismiss", "--type", "task")
		bdProxiedLabelAdd(t, bd, p.dir, dropped.ID, "human")
		bdProxiedHuman(t, bd, p.dir, "dismiss", dropped.ID, "No", "longer", "applicable")
		if got := bdProxiedShow(t, bd, p.dir, dropped.ID); got.CloseReason != "Dismissed: No longer applicable" {
			t.Errorf("close reason: got %q, want %q", got.CloseReason, "Dismissed: No longer applicable")
		}
	})
}
