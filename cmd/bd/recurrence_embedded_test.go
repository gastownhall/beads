//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bdCreateFail runs "bd create" expecting a refusal, and returns the output so
// the caller can pin WHICH refusal it was.
func bdCreateRecurrenceFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"create"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd create %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdIssuesByTitle returns every issue in the workspace whose title matches,
// which is how a spawned successor is found: it carries its predecessor's
// title and gets a fresh id.
func bdIssuesByTitle(t *testing.T, bd, dir, title string) []*types.Issue {
	t.Helper()
	cmd := exec.Command(bd, "list", "--json", "--all", "--limit", "200")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var all []*types.Issue
	if err := json.Unmarshal([]byte(s[start:]), &all); err != nil {
		t.Fatalf("parse list JSON: %v\n%s", err, s)
	}
	var matched []*types.Issue
	for _, issue := range all {
		if issue != nil && issue.Title == title {
			matched = append(matched, issue)
		}
	}
	return matched
}

func TestEmbeddedRecurrenceCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rc")

	t.Run("repeat_fields_round_trip_through_storage", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Weekly standup notes", "--type", "chore",
			"--due", "+1d", "--repeat", "+1w",
			"--repeat-start", "2026-01-01", "--repeat-end", "2030-01-01")
		got := bdShow(t, bd, dir, issue.ID)
		if got.RepeatPattern != "+1w" {
			t.Errorf("repeat_pattern = %q, want +1w", got.RepeatPattern)
		}
		if got.RepeatStart == nil || got.RepeatEnd == nil {
			t.Fatalf("repeat bounds did not persist: start=%v end=%v", got.RepeatStart, got.RepeatEnd)
		}
		if got.RepeatStart.Year() != 2026 || got.RepeatEnd.Year() != 2030 {
			t.Errorf("repeat bounds = %v..%v, want 2026..2030", got.RepeatStart, got.RepeatEnd)
		}
		if got.DueAt == nil {
			t.Error("due_at did not persist")
		}
	})

	t.Run("cron_pattern_round_trips", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Monday review", "--type", "chore", "--repeat", "0 9 * * 1")
		got := bdShow(t, bd, dir, issue.ID)
		if got.RepeatPattern != "0 9 * * 1" {
			t.Errorf("repeat_pattern = %q, want the cron expression", got.RepeatPattern)
		}
	})

	// A --repeat with no --due dates the first instance from the rule itself,
	// so a recurring bead never has to restate its own schedule.
	t.Run("repeat_without_due_takes_its_due_from_the_pattern", func(t *testing.T) {
		before := time.Now().UTC()
		issue := bdCreate(t, bd, dir, "Daily sweep", "--type", "chore", "--repeat", "+1d")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DueAt == nil {
			t.Fatal("recurring bead created with no due date")
		}
		if !got.DueAt.After(before) {
			t.Errorf("derived due %v is not in the future (now %v)", got.DueAt, before)
		}
	})

	t.Run("explicit_due_wins_over_the_pattern", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Explicit due wins", "--type", "task",
			"--due", "2027-05-05", "--repeat", "+1d")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DueAt == nil || got.DueAt.Year() != 2027 {
			t.Errorf("due_at = %v, want the explicit 2027 date", got.DueAt)
		}
	})

	t.Run("unparseable_pattern_is_refused", func(t *testing.T) {
		out := bdCreateRecurrenceFail(t, bd, dir, "Bad rule", "--repeat", "every other tuesday")
		if !strings.Contains(out, "repeat") {
			t.Errorf("refusal did not mention --repeat:\n%s", out)
		}
	})

	t.Run("bounds_without_a_pattern_are_refused", func(t *testing.T) {
		out := bdCreateRecurrenceFail(t, bd, dir, "Orphan bound", "--repeat-end", "2030-01-01")
		if !strings.Contains(out, "--repeat") {
			t.Errorf("refusal did not name --repeat:\n%s", out)
		}
	})

	t.Run("end_before_start_is_refused", func(t *testing.T) {
		out := bdCreateRecurrenceFail(t, bd, dir, "Inverted bounds", "--repeat", "+1d",
			"--repeat-start", "2030-01-01", "--repeat-end", "2029-01-01")
		if !strings.Contains(out, "repeat-end") {
			t.Errorf("refusal did not name the inverted bound:\n%s", out)
		}
	})

	t.Run("update_can_set_and_stop_a_series", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Becomes recurring", "--type", "task", "--due", "+1d")
		bdUpdate(t, bd, dir, issue.ID, "--repeat", "+2w")
		got := bdShow(t, bd, dir, issue.ID)
		if got.RepeatPattern != "+2w" {
			t.Fatalf("repeat_pattern after update = %q, want +2w", got.RepeatPattern)
		}
		// An empty --repeat stops the series; the bead keeps its own due date.
		bdUpdate(t, bd, dir, issue.ID, "--repeat", "")
		got = bdShow(t, bd, dir, issue.ID)
		if got.RepeatPattern != "" {
			t.Errorf("repeat_pattern after clearing = %q, want empty", got.RepeatPattern)
		}
		if got.DueAt == nil {
			t.Error("stopping the series cleared the bead's own due date")
		}
	})

	// Stopping a series clears its bounds with it: a row holding repeat_end
	// and no pattern is one export/import would refuse.
	t.Run("stopping_a_series_clears_its_bounds", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Bounded series", "--type", "chore", "--due", "+1d",
			"--repeat", "+1w", "--repeat-start", "2026-01-01", "--repeat-end", "2030-01-01")
		bdUpdate(t, bd, dir, issue.ID, "--repeat", "")
		got := bdShow(t, bd, dir, issue.ID)
		if got.RepeatPattern != "" || got.RepeatStart != nil || got.RepeatEnd != nil {
			t.Errorf("after --repeat \"\": pattern=%q start=%v end=%v, want all cleared",
				got.RepeatPattern, got.RepeatStart, got.RepeatEnd)
		}
	})

	// The update path validates the triple it would LAND, so it refuses the
	// same shapes create does: a bound without a pattern, and an end before
	// the stored start.
	t.Run("update_refuses_an_incoherent_recurrence_triple", func(t *testing.T) {
		plain := bdCreate(t, bd, dir, "Plain task", "--type", "task", "--due", "+1d")
		out := bdUpdateFail(t, bd, dir, plain.ID, "--repeat-end", "2030-01-01")
		if !strings.Contains(out, "repeat_pattern") {
			t.Errorf("refusal did not name the missing pattern:\n%s", out)
		}
		if got := bdShow(t, bd, dir, plain.ID); got.RepeatEnd != nil {
			t.Errorf("a refused update still wrote repeat_end = %v", got.RepeatEnd)
		}

		bounded := bdCreate(t, bd, dir, "Series with a start", "--type", "chore", "--due", "+1d",
			"--repeat", "+1w", "--repeat-start", "2028-01-01")
		out = bdUpdateFail(t, bd, dir, bounded.ID, "--repeat-end", "2027-01-01")
		if !strings.Contains(out, "before repeat_start") {
			t.Errorf("refusal did not name the inverted bounds:\n%s", out)
		}
	})

	t.Run("update_refuses_an_unparseable_pattern", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reject bad update", "--type", "task")
		cmd := exec.Command(bd, "update", issue.ID, "--repeat", "sometimes")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("expected the update to be refused, got:\n%s", out)
		}
	})
}

func TestEmbeddedRecurrenceSpawnOnClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rs")

	t.Run("closing_a_recurring_bead_files_the_next_instance", func(t *testing.T) {
		const title = "Water the plants"
		first := bdCreate(t, bd, dir, title, "--type", "chore", "--due", "2027-03-01",
			"--repeat", "+1w", "--labels", "household")
		bdClose(t, bd, dir, first.ID)

		matched := bdIssuesByTitle(t, bd, dir, title)
		if len(matched) != 2 {
			t.Fatalf("found %d beads titled %q, want the closed one plus its successor", len(matched), title)
		}
		var successor *types.Issue
		for _, issue := range matched {
			if issue.ID != first.ID {
				successor = issue
			}
		}
		if successor == nil {
			t.Fatal("no successor was created")
		}
		if successor.Status != types.StatusOpen {
			t.Errorf("successor status = %q, want open", successor.Status)
		}
		if successor.RepeatPattern != "+1w" {
			t.Errorf("successor repeat_pattern = %q, want the series to continue", successor.RepeatPattern)
		}
		if successor.DueAt == nil {
			t.Fatal("successor has no due date")
		}
		// The series advances from the closed instance's own due date, not from
		// the moment it happened to be closed.
		want := time.Date(2027, 3, 8, 0, 0, 0, 0, time.UTC)
		if successor.DueAt.UTC().Format("2006-01-02") != want.Format("2006-01-02") {
			t.Errorf("successor due = %v, want %v (one week past the closed instance's due date)",
				successor.DueAt.UTC(), want)
		}
		// Labels live in their own table; a successor that carried them only in
		// memory would vanish from every label-filtered view.
		persisted := bdShow(t, bd, dir, successor.ID)
		if len(persisted.Labels) != 1 || persisted.Labels[0] != "household" {
			t.Errorf("successor labels = %v, want [household]", persisted.Labels)
		}

		// The lineage is recorded on the CLOSED bead, so the series reads
		// forward from any instance.
		events := bdHistoryJSON(t, bd, dir, first.ID, "--events")
		if !eventsContain(events, "recurrence_spawned", successor.ID) {
			t.Errorf("no recurrence_spawned event naming %s in:\n%+v", successor.ID, events)
		}
	})

	t.Run("closing_a_non_recurring_bead_spawns_nothing", func(t *testing.T) {
		const title = "One-off cleanup"
		issue := bdCreate(t, bd, dir, title, "--type", "task", "--due", "+1d")
		bdClose(t, bd, dir, issue.ID)
		if matched := bdIssuesByTitle(t, bd, dir, title); len(matched) != 1 {
			t.Errorf("found %d beads titled %q, want only the closed one", len(matched), title)
		}
	})

	// repeat_end is what ENDS a series: once the next occurrence would fall
	// past it, closing the last instance files nothing.
	t.Run("an_exhausted_series_spawns_nothing", func(t *testing.T) {
		const title = "Series that has ended"
		issue := bdCreate(t, bd, dir, title, "--type", "chore",
			"--due", "2027-03-01", "--repeat", "+1w", "--repeat-end", "2027-03-05")
		bdClose(t, bd, dir, issue.ID)
		if matched := bdIssuesByTitle(t, bd, dir, title); len(matched) != 1 {
			t.Errorf("found %d beads titled %q, want no successor past repeat_end", len(matched), title)
		}
	})

	// A status update into closed is a close by another name, so it reaches
	// the same spawn: a series must not end silently because the caller typed
	// `bd update --status closed` instead of `bd close`.
	t.Run("a_status_update_into_closed_files_the_next_instance", func(t *testing.T) {
		const title = "Closed by status update"
		first := bdCreate(t, bd, dir, title, "--type", "chore", "--due", "2027-03-01",
			"--repeat", "+1w", "--labels", "household")
		bdUpdate(t, bd, dir, first.ID, "--status", "closed")

		successor := otherThan(t, bdIssuesByTitle(t, bd, dir, title), first.ID)
		if successor.Status != types.StatusOpen {
			t.Errorf("successor status = %q, want open", successor.Status)
		}
		if successor.RepeatPattern != "+1w" {
			t.Errorf("successor repeat_pattern = %q, want the series to continue", successor.RepeatPattern)
		}
		if successor.DueAt == nil || successor.DueAt.UTC().Format("2006-01-02") != "2027-03-08" {
			t.Errorf("successor due = %v, want 2027-03-08", successor.DueAt)
		}
		if persisted := bdShow(t, bd, dir, successor.ID); len(persisted.Labels) != 1 || persisted.Labels[0] != "household" {
			t.Errorf("successor labels = %v, want [household]", persisted.Labels)
		}
		events := bdHistoryJSON(t, bd, dir, first.ID, "--events")
		if !eventsContain(events, "recurrence_spawned", successor.ID) {
			t.Errorf("no recurrence_spawned event naming %s in:\n%+v", successor.ID, events)
		}
	})

	// Recurrence is parent-linked and otherwise flat: the successor stays in
	// its epic, while a peer edge describing one instance's relationship to
	// other work is not copied.
	t.Run("the_successor_keeps_its_parent_and_drops_peer_edges", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Household epic", "--type", "epic")
		peer := bdCreate(t, bd, dir, "Related note", "--type", "task")
		const title = "Recurring child chore"
		child := bdCreate(t, bd, dir, title, "--type", "chore", "--due", "2027-03-01",
			"--repeat", "+1w", "--parent", epic.ID)
		bdDepAdd(t, bd, dir, child.ID, peer.ID, "--type", "related")
		bdClose(t, bd, dir, child.ID)

		successor := otherThan(t, bdIssuesByTitle(t, bd, dir, title), child.ID)
		parents := bdDep(t, bd, dir, "list", successor.ID, "--type", "parent-child")
		if !strings.Contains(parents, epic.ID) {
			t.Errorf("successor %s is not a child of %s:\n%s", successor.ID, epic.ID, parents)
		}
		all := bdDep(t, bd, dir, "list", successor.ID)
		if strings.Contains(all, peer.ID) {
			t.Errorf("successor %s inherited the peer edge to %s:\n%s", successor.ID, peer.ID, all)
		}
	})

	// A bead closed weeks late files exactly one successor, due in the FUTURE,
	// on the schedule's own weekday: the spawn walks whole steps from the
	// original anchor rather than filing an already-overdue instance.
	t.Run("a_late_close_files_one_successor_in_the_future", func(t *testing.T) {
		const title = "Closed five weeks late"
		fiveWeeksAgo := time.Now().UTC().AddDate(0, 0, -35)
		first := bdCreate(t, bd, dir, title, "--type", "chore",
			"--due", fiveWeeksAgo.Format("2006-01-02"), "--repeat", "+1w")
		if first.DueAt == nil {
			t.Fatal("setup: the recurring bead has no due date")
		}
		anchor := first.DueAt.UTC()
		bdClose(t, bd, dir, first.ID)

		successor := otherThan(t, bdIssuesByTitle(t, bd, dir, title), first.ID)
		if successor.DueAt == nil {
			t.Fatal("successor has no due date")
		}
		now := time.Now().UTC()
		if !successor.DueAt.After(now) {
			t.Errorf("successor due %v is not in the future (now %v): born overdue", successor.DueAt.UTC(), now)
		}
		if successor.DueAt.After(now.AddDate(0, 0, 7)) {
			t.Errorf("successor due %v is more than one interval out; the walk overshot", successor.DueAt.UTC())
		}
		if successor.DueAt.UTC().Weekday() != anchor.Weekday() {
			t.Errorf("successor due %v lost the anchor weekday %s", successor.DueAt.UTC(), anchor.Weekday())
		}
		if want := anchor; successor.DueAt.UTC().Sub(want)%(7*24*time.Hour) != 0 {
			t.Errorf("successor due %v is not a whole number of weeks past the anchor %v", successor.DueAt.UTC(), want)
		}
	})

	// Closing the successor keeps the chain going, which is what makes this a
	// series rather than a single extra instance.
	t.Run("the_series_continues_past_the_second_instance", func(t *testing.T) {
		const title = "Chain continues"
		first := bdCreate(t, bd, dir, title, "--type", "chore", "--due", "2027-03-01", "--repeat", "+1w")
		bdClose(t, bd, dir, first.ID)
		second := otherThan(t, bdIssuesByTitle(t, bd, dir, title), first.ID)
		bdClose(t, bd, dir, second.ID)
		if matched := bdIssuesByTitle(t, bd, dir, title); len(matched) != 3 {
			t.Errorf("found %d beads titled %q after two closes, want 3", len(matched), title)
		}
	})
}

// eventsContain reports whether any audit event has the given type and mentions
// value somewhere in its payload.
func eventsContain(events []map[string]interface{}, eventType, value string) bool {
	for _, event := range events {
		if fmt.Sprint(event["event_type"]) != eventType {
			continue
		}
		for _, key := range []string{"new_value", "old_value", "comment"} {
			if strings.Contains(fmt.Sprint(event[key]), value) {
				return true
			}
		}
	}
	return false
}

// otherThan returns the single issue in matched whose id is not excludeID.
func otherThan(t *testing.T, matched []*types.Issue, excludeID string) *types.Issue {
	t.Helper()
	var found *types.Issue
	for _, issue := range matched {
		if issue.ID == excludeID {
			continue
		}
		if found != nil {
			t.Fatalf("expected exactly one bead other than %s, found %s and %s", excludeID, found.ID, issue.ID)
		}
		found = issue
	}
	if found == nil {
		t.Fatalf("no bead other than %s", excludeID)
	}
	return found
}

// bdReadyRun runs `bd ready`, the ready-front read the lazy time-based sweeps
// hang off. Its OUTPUT is irrelevant here — running it is the point.
func bdReadyRun(t *testing.T, bd, dir string) {
	t.Helper()
	if out, err := bdRunWithFlockRetry(t, bd, dir, "ready"); err != nil {
		t.Fatalf("bd ready failed: %v\n%s", err, out)
	}
}
