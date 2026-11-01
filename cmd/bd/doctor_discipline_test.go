package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/seats"
	"github.com/steveyegge/beads/internal/types"
)

func disciplineFixtureRegistry() seats.Registry {
	now := time.Now().UTC()
	reg := seats.Registry{}
	live := seats.Endpoint{Session: "s", PaneID: "w1:p1"}
	dead := seats.Endpoint{}
	_ = reg.Register("live-seat", []string{"live-seat"}, live, now)
	_ = reg.Register("dead-seat", []string{"dead-seat"}, dead, now)
	deadRow := reg["dead-seat"]
	deadRow.Liveness = seats.Dead
	reg["dead-seat"] = deadRow
	return reg
}

// TestDisciplineChecks_CountsAndFixes pins every metric: counts, bounded ID
// detail, and the AUTO/DECISION remediation contract THE-FM's monitor reads.
func TestDisciplineChecks_CountsAndFixes(t *testing.T) {
	reg := disciplineFixtureRegistry()
	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	issues := []*types.Issue{
		{ID: "a-claimless", Priority: 2, Title: "Claimed but nobody owns it", Status: types.StatusInProgress},
		{ID: "a-dead", Priority: 2, Title: "Assigned to a dead seat", Status: types.StatusOpen, Assignee: "dead-seat"},
		{ID: "a-ghost", Priority: 2, Title: "Assigned to nothing", Status: types.StatusOpen, Assignee: "ghost-seat"},
		{ID: "a-machine", Priority: 2, Title: "Machine owns work", Status: types.StatusOpen, Assignee: "bd-due-trigger"},
		{ID: "a-live", Priority: 2, Title: "Healthy bead", Status: types.StatusInProgress, Assignee: "live-seat"},
		{ID: "a-overdue", Priority: 2, Title: "Past due no repeat", Status: types.StatusOpen, DueAt: &past},
		{ID: "a-misser", Priority: 2, Title: "Chronic misser", Status: types.StatusOpen, DueMissed: 12},
		{ID: "a-p0", Title: "Escalated long ago", Status: types.StatusOpen, Priority: 0},
		{ID: "b-p0", Title: "Also escalated", Status: types.StatusOpen, Priority: 1},
		{ID: "c-p0", Title: "Escalated too", Status: types.StatusOpen, Priority: 1},
		{ID: "a-owner", Priority: 2, Title: "Seat in owner", Status: types.StatusOpen, Owner: "live-seat"},
		{ID: "dup-1", Priority: 2, Title: "  Same   Duty ", Status: types.StatusOpen},
		{ID: "dup-2", Priority: 2, Title: "same duty", Status: types.StatusOpen},
	}
	for _, it := range issues {
		if it.Status == "" {
			it.Status = types.StatusOpen
		}
	}
	checks := disciplineChecks(issues, reg, now)
	byName := map[string]doctorCheck{}
	for _, c := range checks {
		byName[c.Name] = c
	}

	want := []struct {
		name   string
		status string
		detail string
	}{
		{"discipline.claimless-in-progress", statusWarning, "a-claimless"},
		{"discipline.assignee-dead-seat", statusWarning, "a-dead"},
		{"discipline.assignee-unresolvable", statusWarning, "a-ghost"},
		{"discipline.machine-actor-assignee", statusWarning, "a-machine"},
		{"discipline.overdue-nonrecurring", statusWarning, "a-overdue"},
		{"discipline.high-miss", statusWarning, "a-misser"},
		{"discipline.owner-seat-shaped", statusWarning, ""},
		{"discipline.duplicate-live-titles", statusWarning, "dup-1|dup-2"},
	}
	for _, w := range want {
		c, ok := byName[w.name]
		if !ok {
			t.Fatalf("%s missing from checks", w.name)
		}
		if c.Status != w.status {
			t.Errorf("%s status = %s, want %s", w.name, c.Status, w.status)
		}
		if w.detail != "" && !strings.Contains(c.Detail, w.detail) {
			t.Errorf("%s detail %q missing %q", w.name, c.Detail, w.detail)
		}
		if c.Fix == "" {
			t.Errorf("%s has no remediation; THE-FM's monitor needs the Fix contract", w.name)
		}
		if !strings.Contains(c.Fix, "AUTO") && !strings.Contains(c.Fix, "DECISION") {
			t.Errorf("%s Fix must name AUTO or DECISION: %q", w.name, c.Fix)
		}
	}

	// 3 of 13 live at P0/P1 = 23%: below the inflation bar, so OK.
	if c := byName["discipline.priority-inflation"]; c.Status != statusOK {
		t.Errorf("priority-inflation = %s at 23%% share, want OK", c.Status)
	}

	// A clean store reports all-OK.
	clean := disciplineChecks([]*types.Issue{
		{ID: "ok-1", Title: "Fine", Status: types.StatusInProgress, Assignee: "live-seat", Priority: 2},
	}, reg, now)
	for _, c := range clean {
		if c.Status != statusOK {
			t.Errorf("clean store: %s = %s, want OK", c.Name, c.Status)
		}
	}
}

// TestDisciplineChecks_PriorityInflation pins the systemic bar: a majority at
// P0/P1 flags the store, because escalation without decay kills the signal.
func TestDisciplineChecks_PriorityInflation(t *testing.T) {
	reg := disciplineFixtureRegistry()
	var issues []*types.Issue
	for i := 0; i < 6; i++ {
		issues = append(issues, &types.Issue{ID: "p" + string(rune('a'+i)), Title: "t", Status: types.StatusOpen, Priority: 1})
	}
	issues = append(issues, &types.Issue{ID: "z", Title: "t2", Status: types.StatusOpen, Priority: 3})
	checks := disciplineChecks(issues, reg, time.Now().UTC())
	for _, c := range checks {
		if c.Name == "discipline.priority-inflation" {
			if c.Status != statusWarning {
				t.Errorf("inflation = %s at 86%% share, want warning", c.Status)
			}
			return
		}
	}
	t.Fatal("priority-inflation check missing")
}
