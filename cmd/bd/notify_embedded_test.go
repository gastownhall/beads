//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func bdNotify(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"notify"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd notify %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdNotifyJSON runs a notify subcommand with --json and decodes the array.
func bdNotifyJSON(t *testing.T, bd, dir string, args ...string) []map[string]any {
	t.Helper()
	out := bdNotify(t, bd, dir, append(args, "--json")...)
	start := strings.Index(out, "[")
	if start < 0 {
		return nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &rows); err != nil {
		t.Fatalf("parse notify JSON: %v\n%s", err, out)
	}
	return rows
}

func TestEmbeddedNotifyOutbox(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "nt")
	past := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")

	owned := bdCreate(t, bd, dir, "Overdue for alice", "--type", "task", "--due", past, "--assignee", "alice")
	unowned := bdCreate(t, bd, dir, "Overdue unowned", "--type", "task", "--due", past)

	// The sweep is the producer: firing a due date queues a row addressed to
	// the bead's assignee, and unowned work still gets a seat rather than
	// being dropped.
	bdDueSweep(t, bd, dir)

	pending := bdNotifyJSON(t, bd, dir, "pending")
	if len(pending) != 2 {
		t.Fatalf("pending rows = %d, want 2: %+v", len(pending), pending)
	}
	seats := map[string]string{}
	for _, row := range pending {
		seats[row["seat"].(string)] = row["issue_id"].(string)
	}
	if seats["alice"] != owned.ID {
		t.Errorf("seat alice holds %q, want %s", seats["alice"], owned.ID)
	}
	if seats["unassigned"] != unowned.ID {
		t.Errorf("seat unassigned holds %q, want %s", seats["unassigned"], unowned.ID)
	}

	t.Run("a_successful_exec_delivers_and_acks", func(t *testing.T) {
		out := bdNotify(t, bd, dir, "drain", "--seat", "alice",
			"--exec", `echo "GOT $BD_NOTIFY_SEAT $BD_NOTIFY_ID $BD_NOTIFY_KIND"`)
		if !strings.Contains(out, "GOT alice "+owned.ID+" due") {
			t.Errorf("transport did not receive the row in its environment:\n%s", out)
		}
		if rows := bdNotifyJSON(t, bd, dir, "pending", "--seat", "alice"); len(rows) != 0 {
			t.Errorf("alice still has %d pending after a successful drain", len(rows))
		}
	})

	// The whole point of a durable outbox: a transport that is down must not
	// consume the row. Otherwise the one notification nobody received is also
	// the one nobody can retry.
	t.Run("a_failing_exec_leaves_the_row_pending", func(t *testing.T) {
		cmd := exec.Command(bd, "notify", "drain", "--seat", "unassigned", "--exec", "exit 3")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		_, _, _ = runCommandBuffers(t, cmd) // a failing transport is reported, not fatal here

		rows := bdNotifyJSON(t, bd, dir, "pending", "--seat", "unassigned")
		if len(rows) != 1 {
			t.Fatalf("pending after a failed drain = %d, want the row still queued", len(rows))
		}
		if rows[0]["issue_id"] != unowned.ID {
			t.Errorf("pending row = %v, want %s", rows[0]["issue_id"], unowned.ID)
		}
	})

	t.Run("no_ack_preserves_the_row", func(t *testing.T) {
		bdNotify(t, bd, dir, "drain", "--seat", "unassigned", "--no-ack")
		if rows := bdNotifyJSON(t, bd, dir, "pending", "--seat", "unassigned"); len(rows) != 1 {
			t.Errorf("--no-ack consumed the row: %d pending", len(rows))
		}
	})

	t.Run("ack_clears_the_seat", func(t *testing.T) {
		rows := bdNotifyJSON(t, bd, dir, "pending", "--seat", "unassigned")
		seq := int64(rows[0]["seq"].(float64))
		bdNotify(t, bd, dir, "ack", "--seat", "unassigned", "--upto", strconv.FormatInt(seq, 10))
		if rows := bdNotifyJSON(t, bd, dir, "pending", "--seat", "unassigned"); len(rows) != 0 {
			t.Errorf("ack left %d pending", len(rows))
		}
	})
}
