package main

import "testing"

func fixNames(fixes []doctorCheck) []string {
	out := make([]string, len(fixes))
	for i, f := range fixes {
		out[i] = f.Name
	}
	return out
}

// TestOrderDoctorFixes_BlockedStateRunsAfterGraphFixes guards bd-6dnrw.37: the
// full is_blocked recompute ("Blocked State") must be applied after every
// graph-mutating doctor fix so it recomputes from the corrected graph. The
// regression it pins: removing a child→parent dependency under
// `bd doctor --fix --fix-child-parent` unblocks the child, so a Blocked State
// recompute ordered before that cleanup would leave is_blocked stale.
func TestOrderDoctorFixes_BlockedStateRunsAfterGraphFixes(t *testing.T) {
	// Append order that previously mis-ordered the recompute: Blocked State is
	// appended before the dependency-graph fixes that can unblock issues.
	fixes := []doctorCheck{
		{Name: "Dependency Keys"},
		{Name: "Blocked State"},
		{Name: "Orphaned Dependencies"},
		{Name: "Child-Parent Dependencies"},
		{Name: "Cross-Table Duplicates"},
	}
	orderDoctorFixes(fixes)

	pos := make(map[string]int, len(fixes))
	for i, f := range fixes {
		pos[f.Name] = i
	}
	for _, name := range []string{
		"Dependency Keys",
		"Orphaned Dependencies",
		"Child-Parent Dependencies",
		"Cross-Table Duplicates",
	} {
		if pos["Blocked State"] < pos[name] {
			t.Errorf("Blocked State (pos %d) must run after %s (pos %d); order=%v",
				pos["Blocked State"], name, pos[name], fixNames(fixes))
		}
	}
	if got := fixes[len(fixes)-1].Name; got != "Blocked State" {
		t.Errorf("Blocked State must be last among graph fixes, got last=%q order=%v",
			got, fixNames(fixes))
	}
}

// TestOrderDoctorFixes_BlockedStateStaysLastWhenGraphFixAppendedAfter pins the
// robustness that terminal-priority ordering buys over relying on check-append
// order: a graph-mutating fix appended *after* Blocked State (the original
// latent bug) must still run before the recompute.
func TestOrderDoctorFixes_BlockedStateStaysLastWhenGraphFixAppendedAfter(t *testing.T) {
	fixes := []doctorCheck{
		{Name: "Blocked State"},
		{Name: "Future Graph Fix"}, // unlisted, shares the default priority
		{Name: "Child-Parent Dependencies"},
	}
	orderDoctorFixes(fixes)
	if got := fixes[len(fixes)-1].Name; got != "Blocked State" {
		t.Errorf("Blocked State must remain last even when a graph fix is appended after it; got last=%q order=%v",
			got, fixNames(fixes))
	}
}

// TestOrderDoctorFixes_EarlyFixesStillSortFirst is a guardrail that pinning
// Blocked State terminal did not disturb the explicitly-ordered early fixes.
func TestOrderDoctorFixes_EarlyFixesStillSortFirst(t *testing.T) {
	fixes := []doctorCheck{
		{Name: "Blocked State"},
		{Name: "Permissions"},
		{Name: "Gitignore"},
	}
	orderDoctorFixes(fixes)
	if fixes[0].Name != "Gitignore" || fixes[1].Name != "Permissions" {
		t.Errorf("early ordered fixes must sort ahead of Blocked State; order=%v", fixNames(fixes))
	}
}

// TestOrderDoctorFixes_StatusBlockedDriftRunsAfterBlockedState pins
// gastownhall/beads#6565 review R2: the status=blocked drift repair shares
// Blocked State's membership test, whose parent-child and gate legs read the
// stored is_blocked column, so it must run after the is_blocked recompute. It
// is appended both before and after Blocked State so that neither append
// order decides the result.
func TestOrderDoctorFixes_StatusBlockedDriftRunsAfterBlockedState(t *testing.T) {
	for _, fixes := range [][]doctorCheck{
		{{Name: "Status Blocked Drift"}, {Name: "Blocked State"}, {Name: "Dependency Keys"}},
		{{Name: "Blocked State"}, {Name: "Dependency Keys"}, {Name: "Status Blocked Drift"}},
	} {
		orderDoctorFixes(fixes)
		pos := make(map[string]int, len(fixes))
		for i, f := range fixes {
			pos[f.Name] = i
		}
		if pos["Status Blocked Drift"] < pos["Blocked State"] {
			t.Errorf("Status Blocked Drift (pos %d) must run after Blocked State (pos %d); order=%v",
				pos["Status Blocked Drift"], pos["Blocked State"], fixNames(fixes))
		}
	}
}
