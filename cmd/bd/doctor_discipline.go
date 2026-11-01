package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/seats"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// disciplineMissThreshold mirrors the audit's chronic-miss bar: at ten misses
// a bead has nagged for days and the number is a signal, not a countdown.
const disciplineMissThreshold = 10

// disciplinePriorityShare flags a store where escalation inflation has made
// P0/P1 the majority; past this share the priority field no longer ranks work.
const disciplinePriorityShare = 0.5

// runDisciplineCheck reports the bead-discipline metrics the house audits
// established (identity, claims, due, priority, duplicates) as doctor checks.
// Every finding names its remediation and whether THE-FM may auto-fix it or a
// human must decide. The check is advisory: it reports, it never mutates.
func runDisciplineCheck(path string) error {
	if err := ensureDirectMode("discipline check requires direct mode"); err != nil {
		return HandleError("%v", err)
	}

	issues := liveIssuesForDiscipline()
	reg := registryForDiscipline()
	checks := disciplineChecks(issues, reg, time.Now().UTC())

	if jsonOutput {
		overallOK := true
		for _, c := range checks {
			if c.Status != statusOK {
				overallOK = false
				break
			}
		}
		return outputJSON(struct {
			Path      string        `json:"path"`
			Checks    []doctorCheck `json:"checks"`
			OverallOK bool          `json:"overall_ok"`
		}{Path: path, Checks: checks, OverallOK: overallOK})
	}

	fmt.Println()
	fmt.Println(ui.RenderCategory("Discipline"))
	for _, c := range checks {
		var icon string
		switch c.Status {
		case statusOK:
			icon = ui.RenderPassIcon()
		case statusWarning:
			icon = ui.RenderWarnIcon()
		}
		fmt.Printf("  %s  %s%s\n", icon, c.Name, ui.RenderMuted(" "+c.Message))
		if c.Detail != "" {
			fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(c.Detail))
		}
		if c.Fix != "" {
			fmt.Printf("     %s\n", ui.RenderMuted("Fix: "+c.Fix))
		}
	}
	return nil
}

// liveIssuesForDiscipline fetches every live issue through the open store.
func liveIssuesForDiscipline() []*types.Issue {
	if store == nil {
		return nil
	}
	rows, err := store.SearchIssues(rootCtx, "", workapi.BuildDisciplineFilter())
	if err != nil {
		return nil
	}
	return rows
}

// registryForDiscipline loads the seat registry from the workspace's notify
// dir; an unreadable registry degrades identity metrics to unknown rather
// than failing the whole check.
func registryForDiscipline() seats.Registry {
	o, err := openNotifyOutbox()
	if err != nil {
		return nil
	}
	reg, err := o.Registry()
	if err != nil {
		return nil
	}
	return reg
}

// disciplineChecks is the pure metric core: issues + registry + clock in,
// findings out. Bounded ID lists (ten) keep the report readable on stores
// with hundreds of offenders; counts stay exact.
func disciplineChecks(issues []*types.Issue, reg seats.Registry, now time.Time) []doctorCheck {
	var checks []doctorCheck
	idSample := func(ids []string) string {
		sort.Strings(ids)
		if len(ids) > 10 {
			return fmt.Sprintf("%s (+%d more)", strings.Join(ids[:10], ", "), len(ids)-10)
		}
		return strings.Join(ids, ", ")
	}
	add := func(name, msg, detail, fix string, warning bool) {
		st := statusOK
		if warning {
			st = statusWarning
		}
		checks = append(checks, doctorCheck{Name: name, Status: st, Message: msg, Detail: detail, Fix: fix, Category: "Discipline"})
	}

	resolve := func(name string) (seats.Seat, bool) {
		if reg == nil || strings.TrimSpace(name) == "" {
			return seats.Seat{}, false
		}
		id, err := reg.Resolve(name)
		if err != nil {
			return seats.Seat{}, false
		}
		return reg[id], true
	}

	var claimless, unresolvable, deadSeat, ownerSeatShaped, machineAssigned, overdue, highMiss int
	var claimlessIDs, unresolvableIDs, deadIDs, machineIDs, overdueIDs, missIDs []string
	p01, total := 0, 0
	titleGroups := map[string][]string{}

	for _, it := range issues {
		total++
		if it.Priority == 0 || it.Priority == 1 {
			p01++
		}
		norm := disciplineNormTitle(it.Title)
		if norm != "" {
			titleGroups[norm] = append(titleGroups[norm], it.ID)
		}

		assignee := it.Assignee
		if assignee == "" && it.AssigneeID != "" {
			assignee = it.AssigneeID
		}
		if it.Status == types.StatusInProgress && assignee == "" {
			claimless++
			claimlessIDs = append(claimlessIDs, it.ID)
		}
		if assignee != "" {
			if seats.MachineActor(assignee) != "" {
				machineAssigned++
				machineIDs = append(machineIDs, it.ID)
			} else if seat, ok := resolve(assignee); ok {
				if seat.Liveness == seats.Dead {
					deadSeat++
					deadIDs = append(deadIDs, it.ID)
				}
			} else {
				unresolvable++
				unresolvableIDs = append(unresolvableIDs, it.ID)
			}
		}
		if o := strings.TrimSpace(it.Owner); o != "" {
			if _, ok := resolve(o); ok {
				ownerSeatShaped++
			}
		}
		if it.DueAt != nil && it.DueAt.Before(now) && it.RepeatPattern == "" && it.Status != types.StatusDeferred {
			overdue++
			overdueIDs = append(overdueIDs, it.ID)
		}
		if it.DueMissed >= disciplineMissThreshold {
			highMiss++
			missIDs = append(missIDs, it.ID)
		}
	}

	add("discipline.claimless-in-progress",
		fmt.Sprintf("%d of %d in_progress beads have no assignee", claimless, countStatus(issues, types.StatusInProgress)),
		idSample(claimlessIDs),
		"DECISION (human): assign an owner or close; an agent must not guess ownership. Then AUTO: bd update <id> --assignee <seat> --reason ...",
		claimless > 0)
	add("discipline.assignee-unresolvable",
		fmt.Sprintf("%d live beads name an assignee no seat resolves", unresolvable),
		idSample(unresolvableIDs),
		"AUTO: re-key with bd seat backfill --apply after the seat registers; DECISION if the name is retired.",
		unresolvable > 0)
	add("discipline.assignee-dead-seat",
		fmt.Sprintf("%d live beads are assigned to a dead seat", deadSeat),
		idSample(deadIDs),
		"DECISION: revive the seat (agent re-mints) or retire the name; delivery holds until then.",
		deadSeat > 0)
	add("discipline.machine-actor-assignee",
		fmt.Sprintf("%d live beads name a machine actor as assignee", machineAssigned),
		idSample(machineIDs),
		"AUTO: bd update <id> --assignee \"\" --reason 'machine actor is never an owner'; then a human picks the owner.",
		machineAssigned > 0)
	add("discipline.owner-seat-shaped",
		fmt.Sprintf("%d live beads carry a seat name in owner (owner is human attribution)", ownerSeatShaped),
		"",
		"DECISION (convention): keep the house convention or convert owners to emails; not auto-fixable.",
		ownerSeatShaped > 0)
	add("discipline.overdue-nonrecurring",
		fmt.Sprintf("%d live non-recurring beads are past due", overdue),
		idSample(overdueIDs),
		"AUTO: re-date (bd update <id> --due ...) when the work is still wanted; DECISION otherwise.",
		overdue > 0)
	add("discipline.high-miss",
		fmt.Sprintf("%d live beads have %d+ misses", highMiss, disciplineMissThreshold),
		idSample(missIDs),
		"AUTO: comment the state, then re-date or close with a reason; chronic missers deserve a decision.",
		highMiss > 0)

	share := 0.0
	if total > 0 {
		share = float64(p01) / float64(total)
	}
	add("discipline.priority-inflation",
		fmt.Sprintf("%.0f%% of live beads sit at P0/P1 (%d of %d)", share*100, p01, total),
		"",
		"DECISION (product): escalation never decays; adopt decay-on-attention or reset-on-work, then bulk-normalize.",
		share >= disciplinePriorityShare)

	var dupeGroups int
	var dupeDetail []string
	for _, ids := range titleGroups {
		if len(ids) > 1 {
			dupeGroups++
			dupeDetail = append(dupeDetail, strings.Join(ids, "|"))
		}
	}
	sort.Strings(dupeDetail)
	add("discipline.duplicate-live-titles",
		fmt.Sprintf("%d normalized titles shared by multiple live beads", dupeGroups),
		idSample(dupeDetail),
		"DECISION: pick the surviving copy, close the rest with per-copy reasons (recurrence-guard bypass).",
		dupeGroups > 0)

	return checks
}

func countStatus(issues []*types.Issue, s types.Status) int {
	n := 0
	for _, it := range issues {
		if it.Status == s {
			n++
		}
	}
	return n
}

var disciplineTitleWS = regexp.MustCompile(`\s+`)

// disciplineNormTitle is the same normalization the recurrence guard uses:
// lowercase, collapsed whitespace, trimmed.
func disciplineNormTitle(t string) string {
	return strings.TrimSpace(disciplineTitleWS.ReplaceAllString(strings.ToLower(t), " "))
}
