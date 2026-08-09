//go:build cgo

package main

import (
	"os/exec"
	"strings"
	"testing"
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
}
