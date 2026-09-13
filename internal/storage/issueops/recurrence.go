package issueops

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
)

// RecurrenceSpawnActor is the actor recorded on a successor bead a close
// spawned, when the closer's own actor is unavailable.
const RecurrenceSpawnActor = "bd-recurrence"

// nextSeriesOccurrence is nextOccurrencePast walked from a series anchor
// rather than from an instance's current due date: it steps the pattern from
// anchor and returns the first occurrence strictly after now, bounded by the
// later of anchor and now plus RepeatHorizon. Walking from the recorded
// anchor keeps a close's answer independent of the due sweep, which re-dates
// an open overdue recurring bead on every ready-front read — anchoring on
// that swept date instead would drop the occurrence the sweep had teed up
// whenever a read fell between the deadline and the close.
func nextSeriesOccurrence(probe *types.Issue, anchor, now time.Time) (time.Time, bool) {
	repeat, err := probe.Repeat()
	if err != nil {
		return time.Time{}, false
	}
	from := anchor
	if !repeat.IsInterval() && now.After(from) {
		from = now
	}
	horizon := from
	if now.After(horizon) {
		horizon = now
	}
	return walkOccurrences(probe, from, now, horizon.Add(timeparsing.RepeatHorizon))
}

// walkOccurrences steps a series forward from `from`, returning the first
// occurrence strictly after `now` and within `horizon`, or ok=false when the
// series cannot supply one: it has ended (repeat_end), its pattern is
// unusable, or the next occurrence falls beyond the horizon.
func walkOccurrences(probe *types.Issue, from, now, horizon time.Time) (time.Time, bool) {
	for {
		next, ok, err := probe.NextOccurrence(from)
		if err != nil || !ok || !next.After(from) || next.After(horizon) {
			return time.Time{}, false
		}
		if next.After(now) {
			return next, true
		}
		from = next
	}
}

// SpawnResult reports what a recurrence spawn created. ID is "" when nothing
// was spawned, and ChangedTables is then empty.
//
// ChangedTables exists because a spawn writes MORE than the issues row: the
// successor's labels live in their own table, and a close that stages only
// {issues, events} would leave them dirty-but-uncommitted in the working set —
// present locally, absent from every clone. Callers that stage a fixed table
// list must union this in. See RecurrenceSpawnTables for the closed set.
type SpawnResult struct {
	ID            string
	ChangedTables map[string]bool
}

// RecurrenceSpawnTables is every table a recurrence spawn can write. It is a
// fixed set — the successor is one issues row, its audit events, its labels,
// and the parent-child edge it inherits — so a close path that stages a static
// list can simply include it. Staging a table a spawn did not touch is free:
// DOLT_ADD on a clean table stages nothing, and the empty-commit guard already
// skips a commit with nothing staged.
func RecurrenceSpawnTables() []string { return []string{"issues", "events", "labels", "dependencies"} }

// SpawnRecurrenceInTx creates the next instance of a recurring bead, and is
// called from the close path so every close — single, checked, or batched —
// reaches it through the one chokepoint.
//
// It returns a zero SpawnResult when nothing was spawned: the bead does not
// repeat, its series has ended, or it lives on the wisp plane (wisps are
// scratch state that is garbage-collected, so respawning one would manufacture
// litter). A genuine write failure IS returned and fails the close, because
// silently losing the successor is how a recurring bead quietly stops
// recurring.
func SpawnRecurrenceInTx(ctx context.Context, tx DBTX, id, actor string) (SpawnResult, error) {
	var result SpawnResult
	issue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return result, fmt.Errorf("spawn recurrence: load %s: %w", id, err)
	}
	if issue == nil || !issue.IsRecurring() || IsWisp(issue) {
		return result, nil
	}

	// The series advances from its recorded anchor, never from the due date a
	// due sweep may have advanced past the deadline: the sweep re-dates an
	// open overdue recurring bead on every ready-front read, and anchoring on
	// that date would make the successor depend on how much read traffic
	// happened between the deadline and this close, silently dropping the
	// occurrence the sweep had teed up. repeat_start is the anchor — create,
	// update, and every successor record it — with the instance's own due
	// date as the fallback for series that predate it. The walk still ends
	// strictly in the future, so a late close never files an overdue bead,
	// and a successor never lands BEFORE the closed instance's own due date:
	// an early close keeps the occurrence after THAT date rather than
	// re-filing a slot the series has already passed.
	if _, err := issue.Repeat(); err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: %w", id, err)
	}
	now := time.Now().UTC()
	anchor := now
	if issue.DueAt != nil {
		anchor = issue.DueAt.UTC()
	}
	if issue.RepeatStart != nil {
		anchor = issue.RepeatStart.UTC()
	}
	next, ok := nextSeriesOccurrence(issue, anchor, now)
	if ok && issue.DueAt != nil && next.Before(issue.DueAt.UTC()) {
		next, ok = nextSeriesOccurrence(issue, issue.DueAt.UTC(), now)
	}
	if !ok {
		return result, nil // series exhausted: repeat_end reached
	}

	if actor == "" {
		actor = RecurrenceSpawnActor
	}
	successor := nextRecurrenceInstance(issue, next, actor)

	prefix, err := ReadConfigPrefix(ctx, tx)
	if err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: read prefix: %w", id, err)
	}
	newID, err := GenerateIssueIDInTable(ctx, tx, "issues", prefix, successor, actor)
	if err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: generate id: %w", id, err)
	}
	successor.ID = newID
	successor.ContentHash = successor.ComputeContentHash()

	if err := insertIssueCreateOnly(ctx, tx, "issues", successor); err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: insert %s: %w", id, newID, err)
	}
	result.ID = newID
	result.ChangedTables = map[string]bool{"issues": true, "events": true}
	// Labels live in their own table, so the row insert above does not carry
	// them. A recurring chore's label set is part of what the work IS — the
	// team that filters on it expects every instance to appear — so the
	// successor gets them through the same helper create uses.
	labelResult, err := PersistLabels(ctx, tx, successor, actor, "events")
	if err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: persist labels on %s: %w", id, newID, err)
	}
	result.ChangedTables = mergeChangedTables(result.ChangedTables, labelResult.ChangedTables)
	if err := carryParentLink(ctx, tx, id, newID, actor, result.ChangedTables); err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: carry parent link to %s: %w", id, newID, err)
	}
	if err := RecordEventInTable(ctx, tx, "events", newID, types.EventCreated, actor, ""); err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: record create event: %w", id, err)
	}
	if err := RecordEventInTx(ctx, tx, EventCreate, newID, actor); err != nil {
		return result, err
	}
	// The lineage link lives on the CLOSED bead, so a reader holding any
	// instance can walk the series forward without a schema column for it.
	if err := RecordFullEventInTable(ctx, tx, "events", id, types.EventRecurrenceSpawned,
		actor, id, newID); err != nil {
		return result, fmt.Errorf("spawn recurrence for %s: record lineage event: %w", id, err)
	}
	return result, nil
}

// carryParentLink files the successor under the same parent as the closed
// instance, so a recurring child of an epic stays in that epic.
//
// Recurrence is parent-linked and otherwise flat: peer dependency edges
// (blocks, related, discovered-from, waits-for) describe one instance's
// relationship to other work and are deliberately NOT copied. A successor that
// inherited a `blocks` edge would re-block a bead the closed instance already
// unblocked; a series that needs standing peer edges adds them per instance.
func carryParentLink(ctx context.Context, tx DBTX, prevID, nextID, actor string, changed map[string]bool) error {
	deps, err := GetDependencyRecordsForIssuesInTx(ctx, tx, []string{prevID})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, dep := range deps[prevID] {
		if dep.Type != types.DepParentChild {
			continue
		}
		// The same insert create uses for a --parent edge, on any DBTX: the
		// successor is a brand-new row, so the cycle and hierarchy checks a
		// general dependency add performs have nothing to find.
		edge := &types.Dependency{IssueID: nextID, DependsOnID: dep.DependsOnID, Type: types.DepParentChild}
		kind := ClassifyDepTarget(ctx, tx, edge, types.ExtractPrefix(nextID) != types.ExtractPrefix(dep.DependsOnID))
		//nolint:gosec // G201: the target column comes from DepTargetKind.Column(), a fixed set.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO dependencies (id, issue_id, %s, type, created_by, created_at, metadata, thread_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE type = type
		`, kind.Column()), depid.New(nextID, dep.DependsOnID), nextID, dep.DependsOnID, edge.Type, actor, now, "{}", ""); err != nil {
			return fmt.Errorf("insert parent-child edge %s -> %s: %w", nextID, dep.DependsOnID, err)
		}
		if err := TouchDependencyCoordinationTableInTx(ctx, tx, dep.DependsOnID, "dependencies"); err != nil {
			return err
		}
		if err := RecordDepEventInTx(ctx, tx, EventDepAdd, nextID, string(edge.Type), dep.DependsOnID, "{}", actor); err != nil {
			return err
		}
		changed["dependencies"] = true
	}
	return nil
}

// nextRecurrenceInstance builds the successor bead: the same work, due next.
//
// It copies what DESCRIBES the work and resets what describes this instance of
// it. Deliberately not carried over: status/closure (the successor is open),
// leases (the next occurrence is unclaimed until someone picks it up, though
// it keeps its assignee), external refs and spec ids (they identify one
// instance), and compaction state. Dependency edges are handled by
// carryParentLink: the parent link is kept, peer edges are not.
func nextRecurrenceInstance(prev *types.Issue, due time.Time, actor string) *types.Issue {
	now := time.Now().UTC()
	next := &types.Issue{
		Title:              prev.Title,
		Description:        prev.Description,
		Design:             prev.Design,
		AcceptanceCriteria: prev.AcceptanceCriteria,
		Notes:              prev.Notes,
		Status:             types.StatusOpen,
		Priority:           prev.Priority,
		IssueType:          prev.IssueType,
		Owner:              prev.Owner,
		EstimatedMinutes:   prev.EstimatedMinutes,
		CreatedAt:          now,
		CreatedBy:          actor,
		UpdatedAt:          now,
		DueAt:              &due,
		RepeatPattern:      prev.RepeatPattern,
		RepeatStart:        prev.RepeatStart,
		RepeatEnd:          prev.RepeatEnd,
		SourceRepo:         prev.SourceRepo,
		StorageClass:       prev.StorageClass,
		MolType:            prev.MolType,
		WorkType:           prev.WorkType,
		Metadata:           prev.Metadata,
		Labels:             append([]string(nil), prev.Labels...),
	}
	// The successor inherits the assignee so a recurring chore stays with
	// whoever owns it; an unassigned series stays unassigned.
	next.Assignee = prev.Assignee
	return next
}

// ClearRecurrenceBoundsOnStop makes an empty repeat_pattern also clear both
// bounds, so stopping a series never leaves a row holding repeat_start or
// repeat_end with no pattern — a shape PrepareIssueForInsert refuses on the
// next export/import.
func ClearRecurrenceBoundsOnStop(updates map[string]interface{}) {
	if pattern, ok := updates["repeat_pattern"].(string); ok && pattern == "" {
		updates["repeat_start"] = nil
		updates["repeat_end"] = nil
	}
}

// AnchorRecurrenceUpdate is AnchorRecurrence for the update funnel: an update
// that gives a bead a repeat pattern with no repeat_start, on a bead that has
// (or is being given) a due date, records that due date as the series' start
// so later steps have their anchor. It runs before ValidateRecurrenceUpdate,
// on the landed triple.
func AnchorRecurrenceUpdate(oldIssue *types.Issue, updates map[string]interface{}) {
	pattern, _ := updates["repeat_pattern"].(string)
	if pattern == "" {
		return
	}
	if raw, ok := updates["repeat_start"]; ok {
		if raw != nil {
			return
		}
	} else if oldIssue.RepeatStart != nil {
		return
	}
	due := oldIssue.DueAt
	if raw, ok := updates["due_at"]; ok {
		if due, _ = updateTimeValue("due_at", raw); due == nil {
			return
		}
	}
	if due == nil {
		return
	}
	updates["repeat_start"] = due.UTC()
}

// ValidateRecurrenceUpdate checks the (repeat_pattern, repeat_start,
// repeat_end) triple an update would LAND — the row's current values merged
// with the update — against the same rule every create path applies
// (types.Issue.ValidateRecurrence), so an update cannot leave a row that a
// create would have refused.
func ValidateRecurrenceUpdate(oldIssue *types.Issue, updates map[string]interface{}) error {
	rawPattern, hasPattern := updates["repeat_pattern"]
	rawStart, hasStart := updates["repeat_start"]
	rawEnd, hasEnd := updates["repeat_end"]
	if !hasPattern && !hasStart && !hasEnd {
		return nil
	}
	merged := types.Issue{
		RepeatPattern: oldIssue.RepeatPattern,
		RepeatStart:   oldIssue.RepeatStart,
		RepeatEnd:     oldIssue.RepeatEnd,
	}
	if hasPattern {
		pattern, ok := rawPattern.(string)
		if !ok {
			return fmt.Errorf("%w: invalid repeat pattern %v", storage.ErrValidation, rawPattern)
		}
		merged.RepeatPattern = pattern
	}
	var err error
	if hasStart {
		if merged.RepeatStart, err = updateTimeValue("repeat_start", rawStart); err != nil {
			return err
		}
	}
	if hasEnd {
		if merged.RepeatEnd, err = updateTimeValue("repeat_end", rawEnd); err != nil {
			return err
		}
	}
	if err := merged.ValidateRecurrence(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	return nil
}

// updateTimeValue reads a nullable timestamp as the update funnels carry it:
// nil clears, and either a time.Time or a *time.Time sets.
func updateTimeValue(key string, raw interface{}) (*time.Time, error) {
	switch value := raw.(type) {
	case nil:
		return nil, nil
	case time.Time:
		return &value, nil
	case *time.Time:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: invalid %s value %v", storage.ErrValidation, key, raw)
	}
}

// AnchorRecurrence records where a recurring series was first scheduled: a
// bead created with a repeat pattern and a due date but no repeat_start takes
// its first due date as the start bound. That bound is what later steps read
// as the series' anchor (types.Issue.NextOccurrence), so a monthly rule keeps
// its original day-of-month after a short month has clamped it.
func AnchorRecurrence(issue *types.Issue) {
	if issue == nil || !issue.IsRecurring() || issue.RepeatStart != nil || issue.DueAt == nil {
		return
	}
	start := issue.DueAt.UTC()
	issue.RepeatStart = &start
}
