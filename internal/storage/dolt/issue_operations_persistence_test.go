package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestMoveIssuePersistenceInTxMovesAggregateAndReportsTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "persist-move", Title: "move", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"label"}}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddIssueComment(ctx, issue.ID, "tester", "comment"); err != nil {
		t.Fatal(err)
	}
	depender := &types.Issue{ID: "persist-depender", Title: "depender", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, depender, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: depender.ID, DependsOnID: issue.ID, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)`, issue.ID, 4); err != nil {
		t.Fatal(err)
	}
	var moved issueops.PersistenceMoveResult
	if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		moved, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModeEphemeral, "test-actor")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !moved.Changed || !moved.ChangedTables["issues"] || !moved.ChangedTables["wisps"] || !moved.ChangedTables["wisp_comments"] || !moved.ChangedTables["wisp_labels"] || !moved.ChangedTables["dependencies"] {
		t.Fatalf("move result = %#v, want changed aggregate tables", moved)
	}
	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ephemeral || got.NoHistory || len(got.Labels) != 1 || got.Labels[0] != "label" {
		t.Fatalf("moved issue = %#v", got)
	}
	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Text != "comment" {
		t.Fatalf("comments = %#v", comments)
	}
	var durableTarget, wispTarget *string
	if err := store.db.QueryRowContext(ctx, `SELECT depends_on_issue_id, depends_on_wisp_id FROM dependencies WHERE issue_id = ?`, depender.ID).Scan(&durableTarget, &wispTarget); err != nil {
		t.Fatal(err)
	}
	if durableTarget != nil || wispTarget == nil || *wispTarget != issue.ID {
		t.Fatalf("inbound target = (%v,%v), want (nil,%q)", durableTarget, wispTarget, issue.ID)
	}
	var sourceEvents, targetEvents, sourceCounters, targetCounters int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE issue_id = ?`, issue.ID).Scan(&sourceEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, issue.ID).Scan(&targetEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM child_counters WHERE parent_id = ?`, issue.ID).Scan(&sourceCounters); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ? AND last_child = 4`, issue.ID).Scan(&targetCounters); err != nil {
		t.Fatal(err)
	}
	if sourceEvents != 0 || targetEvents == 0 || sourceCounters != 0 || targetCounters != 1 {
		t.Fatalf("moved event/counter rows = events(%d,%d) counters(%d,%d)", sourceEvents, targetEvents, sourceCounters, targetCounters)
	}
}

func TestMoveIssuePersistenceInTxSameModeIsNoop(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "persist-noop", Title: "noop", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		result, err := issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModePersistent, "test-actor")
		if err != nil {
			return err
		}
		if result.Changed {
			t.Fatal("same persistence mode changed issue")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMoveIssuePersistenceInTxTransitions(t *testing.T) {
	for _, transition := range []struct {
		name string
		from types.PersistenceMode
		to   types.PersistenceMode
	}{
		{"persistent-to-ephemeral", types.PersistenceModePersistent, types.PersistenceModeEphemeral},
		{"persistent-to-no-history", types.PersistenceModePersistent, types.PersistenceModeNoHistory},
		{"ephemeral-to-persistent", types.PersistenceModeEphemeral, types.PersistenceModePersistent},
		{"ephemeral-to-no-history", types.PersistenceModeEphemeral, types.PersistenceModeNoHistory},
		{"no-history-to-persistent", types.PersistenceModeNoHistory, types.PersistenceModePersistent},
		{"no-history-to-ephemeral", types.PersistenceModeNoHistory, types.PersistenceModeEphemeral},
	} {
		t.Run(transition.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx := context.Background()
			ephemeral, noHistory, _, err := types.NormalizePersistenceMode(types.Issue{}, transition.from)
			if err != nil {
				t.Fatal(err)
			}
			issue := &types.Issue{ID: "transition-" + transition.name, Title: transition.name, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral, NoHistory: noHistory}
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatal(err)
			}
			if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
				current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
				if err != nil {
					return err
				}
				_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, transition.to, "test-actor")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantEphemeral, wantNoHistory, _, err := types.NormalizePersistenceMode(types.Issue{Ephemeral: ephemeral, NoHistory: noHistory}, transition.to)
			if err != nil {
				t.Fatal(err)
			}
			if got.Ephemeral != wantEphemeral || got.NoHistory != wantNoHistory {
				t.Fatalf("persistence = (%t,%t), want (%t,%t)", got.Ephemeral, got.NoHistory, wantEphemeral, wantNoHistory)
			}
		})
	}
}

func TestMoveIssuePersistenceInTxRefusesUnversionedDemotion(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "unversioned", Title: "unversioned", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, StorageClass: types.StorageClassUnversioned}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModeEphemeral, "test-actor")
		return err
	})
	if err == nil {
		t.Fatal("unversioned demotion succeeded")
	}
	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil || got.Ephemeral || got.NoHistory {
		t.Fatalf("refusal state = %#v, %v", got, err)
	}
}

func TestMoveIssuePersistenceInTxDeletesLeaseOnDemotion(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "persist-lease", Title: "lease", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimIssue(ctx, issue.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE issue_id = ?`, issue.ID).Scan(&before); err != nil || before != 1 {
		t.Fatalf("lease before move = %d, %v", before, err)
	}
	if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModeEphemeral, "test-actor")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE issue_id = ?`, issue.ID).Scan(&after); err != nil || after != 0 {
		t.Fatalf("lease after demotion = %d, %v", after, err)
	}
}

func TestMoveIssuePersistenceInTxTargetCollisionRollsBackSource(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "persist-collision", Title: "source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"source-label"}}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, is_blocked) VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)`, issue.ID, "target", types.StatusOpen, 2, types.TypeTask, true, 0); err != nil {
		t.Fatal(err)
	}
	err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModeEphemeral, "test-actor")
		return err
	})
	if err == nil {
		t.Fatal("move into occupied target succeeded")
	}
	var sourceLabels int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = 'source-label'`, issue.ID).Scan(&sourceLabels); err != nil {
		t.Fatal(err)
	}
	if sourceLabels != 1 {
		t.Fatalf("source aggregate was not rolled back; labels = %d", sourceLabels)
	}
}

func TestMoveIssuePersistenceInTxLateCounterConflictRollsBack(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "persist-late-rollback", Title: "source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"source-label"}}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddIssueComment(ctx, issue.ID, "tester", "source comment"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)`, issue.ID, 3); err != nil {
		t.Fatal(err)
	}
	// wisp_child_counters deliberately allows orphaned rows while a target wisp
	// is absent. This duplicate is reached after the target issue, labels,
	// comments, and events have been inserted, so it proves transaction rollback.
	if _, err := store.db.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO wisp_child_counters (parent_id, last_child) VALUES (?, ?)`, issue.ID, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 1`); err != nil {
		t.Fatal(err)
	}
	err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModeEphemeral, "test-actor")
		return err
	})
	if err == nil {
		t.Fatal("late child-counter conflict did not fail move")
	}
	var sourceIssue, targetIssue, sourceLabels, sourceComments, targetLabels, targetComments, sourceCounter, targetCounter int
	for _, check := range []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM issues WHERE id = ?`, &sourceIssue}, {`SELECT COUNT(*) FROM wisps WHERE id = ?`, &targetIssue},
		{`SELECT COUNT(*) FROM labels WHERE issue_id = ?`, &sourceLabels}, {`SELECT COUNT(*) FROM comments WHERE issue_id = ?`, &sourceComments},
		{`SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?`, &targetLabels}, {`SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?`, &targetComments},
		{`SELECT COUNT(*) FROM child_counters WHERE parent_id = ? AND last_child = 3`, &sourceCounter}, {`SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ? AND last_child = 9`, &targetCounter},
	} {
		if err := store.db.QueryRowContext(ctx, check.query, issue.ID).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
	}
	if sourceIssue != 1 || targetIssue != 0 || sourceLabels != 1 || sourceComments != 1 || targetLabels != 0 || targetComments != 0 || sourceCounter != 1 || targetCounter != 1 {
		t.Fatalf("rollback state issues(%d,%d) labels(%d,%d) comments(%d,%d) counters(%d,%d)", sourceIssue, targetIssue, sourceLabels, targetLabels, sourceComments, targetComments, sourceCounter, targetCounter)
	}
}

// TestMoveIssuePersistenceInTxRepairsFlagsInTheIssuesPlane pins the same-plane
// branch against the row shape that actually reaches it.
//
// That branch runs when no MOVE is needed but the stored flags still disagree
// with the plane the row is in, and the reachable instance is a DURABLE row
// carrying ephemeral = 1 — what the pre-role proxied update produced. It used
// to issue `UPDATE wisps` unconditionally, so the repair matched no row in the
// issues plane and still reported Changed: true: `bd update <id> --persistent`
// printed success and left the flag set.
//
// The assertion is on the STORED ROW rather than on the result, because
// reporting the change is exactly what the bug did correctly.
func TestMoveIssuePersistenceInTxRepairsFlagsInTheIssuesPlane(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	issue := &types.Issue{ID: "persist-repair", Title: "repair", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	// The corruption the changelog certifies exists: durable row, wisp flag.
	if _, err := store.db.ExecContext(ctx, `UPDATE issues SET ephemeral = 1 WHERE id = ?`, issue.ID); err != nil {
		t.Fatal(err)
	}

	var moved issueops.PersistenceMoveResult
	if err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return err
		}
		moved, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModePersistent, "test-actor")
		return err
	}); err != nil {
		t.Fatalf("repairing a durable row with the wisp flag set: %v", err)
	}
	if !moved.Changed || !moved.ChangedTables["issues"] {
		t.Errorf("move result = %#v, want the issues table reported changed", moved)
	}

	var ephemeral bool
	if err := store.db.QueryRowContext(ctx, `SELECT ephemeral FROM issues WHERE id = ?`, issue.ID).Scan(&ephemeral); err != nil {
		t.Fatal(err)
	}
	if ephemeral {
		t.Error("issues.ephemeral is still 1: --persistent reported success and repaired nothing")
	}
	var wisps int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisps WHERE id = ?`, issue.ID).Scan(&wisps); err != nil {
		t.Fatal(err)
	}
	if wisps != 0 {
		t.Errorf("wisps rows = %d, want 0: a same-plane repair must not move the row", wisps)
	}
}

// TestPersistenceRepairKeepsTheRowsOwnParticipation pins the one persistence
// path that mints update-shaped on purpose. A repair (the same-plane branch
// above: an issues-plane row carrying a wisp flag, made persistent in place)
// leaves the row where it is, so the row keeps its own
// participation_generation. With versioned history on, a legacy row stays
// legacy and mints nothing, because a repair is an ordinary write and must
// not promote it (design §16.2b, R2.3). A row created with the flag on mints
// one version, as any update of it would. A move out of the wisps table is
// different: it inserts a new issues row, so it is create-shaped
// (RunParticipationGenerationWispPersistenceMoveBeginsHistory). Both repair
// entry points are covered, because they mint at different sites:
// MoveIssuePersistenceInTx's own repair mint, and the guarded update's one
// end-of-update mint.
func TestPersistenceRepairKeepsTheRowsOwnParticipation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	defer store.SetVersionedHistoryEnabled(false)
	ctx := context.Background()
	ops, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	repairs := map[string]func(id string) error{
		"move": func(id string) error {
			return store.withRetryTx(ctx, func(tx *sql.Tx) error {
				current, err := issueops.GetIssueInTx(ctx, tx, id)
				if err != nil {
					return err
				}
				_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModePersistent, "repairer")
				return err
			})
		},
		"update": func(id string) error {
			_, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "repairer", IssueID: id, Patch: publicops.IssuePatch{
				Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModePersistent},
			}})
			return err
		},
	}
	state := func(id string) (versions int, generation sql.NullInt64) {
		t.Helper()
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, id).Scan(&versions); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT participation_generation FROM issues WHERE id = ?`, id).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return versions, generation
	}

	for _, legacy := range []bool{true, false} {
		for _, via := range []string{"move", "update"} {
			id := "persist-repair-pg-" + via
			if legacy {
				id += "-legacy"
			}
			// A legacy row is created with the flag off; either way the
			// repair itself runs with it on.
			store.SetVersionedHistoryEnabled(!legacy)
			if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
				t.Fatal(err)
			}
			store.SetVersionedHistoryEnabled(true)
			if _, err := store.db.ExecContext(ctx, `UPDATE issues SET ephemeral = 1 WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
			beforeVersions, beforeGeneration := state(id)

			if err := repairs[via](id); err != nil {
				t.Fatalf("%s: repairing %s: %v", via, id, err)
			}

			afterVersions, afterGeneration := state(id)
			if afterGeneration != beforeGeneration {
				t.Errorf("%s: participation_generation for %s went from %v to %v across the repair, want it "+
					"unchanged: the row stays on the issues plane, so its participation is its own",
					via, id, beforeGeneration, afterGeneration)
			}
			wantVersions := beforeVersions + 1
			if legacy {
				wantVersions = beforeVersions
			}
			if afterVersions != wantVersions {
				t.Errorf("%s: version rows for %s (legacy=%v) went from %d to %d across the repair, want %d: "+
					"a repair is update-shaped, so it mints for a participating row and skips a legacy one",
					via, id, legacy, beforeVersions, afterVersions, wantVersions)
			}
		}
	}
}
