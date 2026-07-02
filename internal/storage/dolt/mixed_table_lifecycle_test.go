package dolt

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestDemoteToWispPreservesInboundDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-demote-src")
	createPerm(t, ctx, store, "mixed-demote-target")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-demote-src",
		DependsOnID: "mixed-demote-target",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency before demote: %v", err)
	}

	if err := store.UpdateIssue(ctx, "mixed-demote-target", map[string]interface{}{
		"no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("demote target to no-history wisp: %v", err)
	}

	deps, err := store.GetDependencies(ctx, "mixed-demote-src")
	if err != nil {
		t.Fatalf("GetDependencies after demote: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "mixed-demote-target" || !deps[0].NoHistory {
		t.Fatalf("dependencies after demote = %+v, want no-history target", deps)
	}

	dependents, err := store.GetDependents(ctx, "mixed-demote-target")
	if err != nil {
		t.Fatalf("GetDependents after demote: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != "mixed-demote-src" {
		t.Fatalf("dependents after demote = %+v, want source", dependents)
	}

	assertDependencyTargetColumns(t, store, "dependencies", "mixed-demote-src", "mixed-demote-target", false)
}

func TestDemoteToWispRemovesCopiedOutboundDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-demote-outbound")
	createPerm(t, ctx, store, "mixed-demote-outbound-blocker")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-demote-outbound",
		DependsOnID: "mixed-demote-outbound-blocker",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency before demote: %v", err)
	}

	if err := store.UpdateIssue(ctx, "mixed-demote-outbound", map[string]interface{}{
		"no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("demote source to no-history wisp: %v", err)
	}

	assertNoRowsForIssue(t, store, "dependencies", "mixed-demote-outbound")

	deps, err := store.GetDependencies(ctx, "mixed-demote-outbound")
	if err != nil {
		t.Fatalf("GetDependencies after demote: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "mixed-demote-outbound-blocker" {
		t.Fatalf("dependencies after demote = %+v, want demoted wisp to keep outbound dependency", deps)
	}
}

func TestDemoteToWispRollsBackWhenAuxiliaryCopyFails(t *testing.T) {
	cases := []struct {
		name      string
		dropTable string
		wantErr   string
		issueID   string
	}{
		{"labels", "wisp_labels", "copy labels for demoted issue", "mixed-demote-labels-fail"},
		{"dependencies", "wisp_dependencies", "copy dependencies for demoted issue", "mixed-demote-dependencies-fail"},
		{"events", "wisp_events", "copy events for demoted issue", "mixed-demote-events-fail"},
		{"comments", "wisp_comments", "copy comments for demoted issue", "mixed-demote-comments-fail"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			createPerm(t, ctx, store, tc.issueID)

			if _, err := store.db.ExecContext(ctx, "DROP TABLE "+tc.dropTable); err != nil {
				t.Fatalf("drop %s: %v", tc.dropTable, err)
			}

			err := store.UpdateIssue(ctx, tc.issueID, map[string]interface{}{
				"no_history": true,
			}, "tester")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("UpdateIssue error = %v, want %q", err, tc.wantErr)
			}

			assertRowCountForIssue(t, store, "issues", tc.issueID, 1)
			assertRowCountForIssue(t, store, "wisps", tc.issueID, 0)
		})
	}
}

func TestDemoteToWispLeavesIgnoredWispTablesUnstaged(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-demote-ignored-stage")
	if err := store.UpdateIssue(ctx, "mixed-demote-ignored-stage", map[string]interface{}{
		"no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("demote to no-history wisp: %v", err)
	}

	var stagedIgnored int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('wisps', 'wisp_labels', 'wisp_dependencies', 'wisp_events', 'wisp_comments')
		  AND staged = TRUE
	`).Scan(&stagedIgnored); err != nil {
		t.Fatalf("query ignored staged status: %v", err)
	}
	if stagedIgnored != 0 {
		t.Fatalf("demotion staged %d ignored wisp table(s)", stagedIgnored)
	}

	var committedWisp int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisps AS OF 'HEAD' WHERE id = ?`,
		"mixed-demote-ignored-stage",
	).Scan(&committedWisp); err != nil {
		t.Fatalf("count committed wisp: %v", err)
	}
	if committedWisp != 0 {
		t.Fatalf("demotion committed ignored wisp row, count = %d", committedWisp)
	}
}

func TestPromoteFromEphemeralPreservesInboundDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-promote-src")
	createWisp(t, ctx, store, "mixed-promote-target")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-promote-src",
		DependsOnID: "mixed-promote-target",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency before promote: %v", err)
	}

	if err := store.PromoteFromEphemeral(ctx, "mixed-promote-target", "tester"); err != nil {
		t.Fatalf("PromoteFromEphemeral: %v", err)
	}

	deps, err := store.GetDependencies(ctx, "mixed-promote-src")
	if err != nil {
		t.Fatalf("GetDependencies after promote: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "mixed-promote-target" || deps[0].Ephemeral {
		t.Fatalf("dependencies after promote = %+v, want permanent target", deps)
	}

	dependents, err := store.GetDependents(ctx, "mixed-promote-target")
	if err != nil {
		t.Fatalf("GetDependents after promote: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != "mixed-promote-src" {
		t.Fatalf("dependents after promote = %+v, want source", dependents)
	}

	assertDependencyTargetColumns(t, store, "dependencies", "mixed-promote-src", "mixed-promote-target", true)
}

func TestPromoteFromEphemeralRejectsCrossTypedTargetCollision(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-promote-collision-source")
	createWisp(t, ctx, store, "mixed-promote-collision-target")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-promote-collision-source",
		DependsOnID: "mixed-promote-collision-target",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency before promote: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by, metadata)
		VALUES (UUID(), ?, ?, ?, NOW(), ?, ?)
	`, "mixed-promote-collision-source", "mixed-promote-collision-target", types.DepRelated, "tester", "{}"); err != nil {
		t.Fatalf("seed external collision: %v", err)
	}

	err := store.PromoteFromEphemeral(ctx, "mixed-promote-collision-target", "tester")
	if err == nil || !strings.Contains(err.Error(), "collides with existing dependency target") {
		t.Fatalf("PromoteFromEphemeral error = %v, want collision", err)
	}

	assertRowCountForIssue(t, store, "wisps", "mixed-promote-collision-target", 1)
	assertRowCountForIssue(t, store, "issues", "mixed-promote-collision-target", 0)
	var wispTargetRows, externalTargetRows int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dependencies
		WHERE issue_id = ? AND depends_on_wisp_id = ?
	`, "mixed-promote-collision-source", "mixed-promote-collision-target").Scan(&wispTargetRows); err != nil {
		t.Fatalf("count wisp target rows after failed promote: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dependencies
		WHERE issue_id = ? AND depends_on_external = ?
	`, "mixed-promote-collision-source", "mixed-promote-collision-target").Scan(&externalTargetRows); err != nil {
		t.Fatalf("count external target rows after failed promote: %v", err)
	}
	if wispTargetRows != 1 || externalTargetRows != 1 {
		t.Fatalf("dependency rows after failed promote: wisp=%d external=%d, want 1 each", wispTargetRows, externalTargetRows)
	}
}

func TestPromoteFromEphemeralRemovesCopiedOutboundWispDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createWisp(t, ctx, store, "mixed-promote-outbound")
	createPerm(t, ctx, store, "mixed-promote-outbound-blocker")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-promote-outbound",
		DependsOnID: "mixed-promote-outbound-blocker",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency before promote: %v", err)
	}

	if err := store.PromoteFromEphemeral(ctx, "mixed-promote-outbound", "tester"); err != nil {
		t.Fatalf("PromoteFromEphemeral: %v", err)
	}

	assertNoRowsForIssue(t, store, "wisp_dependencies", "mixed-promote-outbound")

	deps, err := store.GetDependencies(ctx, "mixed-promote-outbound")
	if err != nil {
		t.Fatalf("GetDependencies after promote: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "mixed-promote-outbound-blocker" {
		t.Fatalf("dependencies after promote = %+v, want promoted issue to keep outbound dependency", deps)
	}
}

func TestPromoteFromEphemeralCommitsAuxiliaryTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createWisp(t, ctx, store, "mixed-promote-aux")
	createPerm(t, ctx, store, "mixed-promote-blocker")

	if err := store.AddLabel(ctx, "mixed-promote-aux", "keep", "tester"); err != nil {
		t.Fatalf("AddLabel before promote: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, "mixed-promote-aux", "tester", "retain this context"); err != nil {
		t.Fatalf("AddIssueComment before promote: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-promote-aux",
		DependsOnID: "mixed-promote-blocker",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency before promote: %v", err)
	}

	if err := store.PromoteFromEphemeral(ctx, "mixed-promote-aux", "tester"); err != nil {
		t.Fatalf("PromoteFromEphemeral: %v", err)
	}

	var dirty int
	err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			  AND s.table_name LIKE di.pattern
		)
	`).Scan(&dirty)
	if err != nil {
		t.Fatalf("query dolt_status after promote: %v", err)
	}
	if dirty != 0 {
		t.Fatalf("promotion left %d committable table(s) dirty", dirty)
	}

	assertNoRowsForIssue(t, store, "wisp_labels", "mixed-promote-aux")
	assertNoRowsForIssue(t, store, "wisp_dependencies", "mixed-promote-aux")
	assertNoRowsForIssue(t, store, "wisp_comments", "mixed-promote-aux")
}

func TestPromoteFromEphemeralRollsBackWhenAuxiliaryCopyFails(t *testing.T) {
	cases := []struct {
		name      string
		dropTable string
		wantErr   string
		wispID    string
	}{
		{"labels", "labels", "copy labels for promoted wisp", "mixed-promote-labels-fail"},
		{"dependencies", "dependencies", "copy dependencies for promoted wisp", "mixed-promote-dependencies-fail"},
		{"events", "events", "copy events for promoted wisp", "mixed-promote-events-fail"},
		{"comments", "comments", "copy comments for promoted wisp", "mixed-promote-comments-fail"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			createWisp(t, ctx, store, tc.wispID)

			if _, err := store.db.ExecContext(ctx, "DROP TABLE "+tc.dropTable); err != nil {
				t.Fatalf("drop %s: %v", tc.dropTable, err)
			}

			err := store.PromoteFromEphemeral(ctx, tc.wispID, "tester")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("PromoteFromEphemeral error = %v, want %q", err, tc.wantErr)
			}

			assertRowCountForIssue(t, store, "wisps", tc.wispID, 1)
			assertRowCountForIssue(t, store, "issues", tc.wispID, 0)
		})
	}
}

func TestGetReadyWorkIncludesNoHistoryWispsByDefault(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	noHistory := &types.Issue{
		ID:        "mixed-ready-no-history",
		Title:     "no-history ready work",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history: %v", err)
	}
	createWisp(t, ctx, store, "mixed-ready-ephemeral")

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := issueIDs(ready)
	if !containsID(ids, "mixed-ready-no-history") {
		t.Fatalf("GetReadyWork default omitted no-history wisp; got %v", ids)
	}
	if containsID(ids, "mixed-ready-ephemeral") {
		t.Fatalf("GetReadyWork default included ephemeral wisp; got %v", ids)
	}
}

func TestGetReadyWorkExcludesBlockedNoHistoryWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	noHistory := &types.Issue{
		ID:        "mixed-ready-blocked-no-history",
		Title:     "blocked no-history ready work",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history: %v", err)
	}
	createPerm(t, ctx, store, "mixed-ready-blocker")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-ready-blocked-no-history",
		DependsOnID: "mixed-ready-blocker",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency blocker: %v", err)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := issueIDs(ready)
	if containsID(ids, "mixed-ready-blocked-no-history") {
		t.Fatalf("GetReadyWork returned blocked no-history wisp; got %v", ids)
	}
}

func TestGetReadyWorkExcludesPermBlockedByNoHistoryWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-ready-blocked-perm")
	noHistory := &types.Issue{
		ID:        "mixed-ready-blocking-no-history",
		Title:     "no-history blocker",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-ready-blocked-perm",
		DependsOnID: "mixed-ready-blocking-no-history",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency blocker: %v", err)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := issueIDs(ready)
	if containsID(ids, "mixed-ready-blocked-perm") {
		t.Fatalf("GetReadyWork returned perm issue blocked by no-history wisp; got %v", ids)
	}
}

func TestGetBlockingInfoForIssuesHandlesCrossClassDeps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-blocking-info-perm")
	createWisp(t, ctx, store, "mixed-blocking-info-wisp")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-blocking-info-wisp",
		DependsOnID: "mixed-blocking-info-perm",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency wisp blocked by perm: %v", err)
	}

	blockedBy, blocks, _, err := store.GetBlockingInfoForIssues(ctx, []string{"mixed-blocking-info-perm", "mixed-blocking-info-wisp"})
	if err != nil {
		t.Fatalf("GetBlockingInfoForIssues: %v", err)
	}
	if got := blockedBy["mixed-blocking-info-wisp"]; len(got) != 1 || got[0] != "mixed-blocking-info-perm" {
		t.Fatalf("blockedBy[wisp] = %v, want perm blocker", got)
	}
	if got := blocks["mixed-blocking-info-perm"]; len(got) != 1 || got[0] != "mixed-blocking-info-wisp" {
		t.Fatalf("blocks[perm] = %v, want wisp dependent", got)
	}
}

func TestGetBlockingInfoForIssuesSkipsClosedCrossClassBlockers(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-blocking-info-blocked")
	createWisp(t, ctx, store, "mixed-blocking-info-closed-wisp")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-blocking-info-blocked",
		DependsOnID: "mixed-blocking-info-closed-wisp",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency perm blocked by wisp: %v", err)
	}
	if err := store.CloseIssue(ctx, "mixed-blocking-info-closed-wisp", "done", "tester", "s1"); err != nil {
		t.Fatalf("CloseIssue wisp blocker: %v", err)
	}

	blockedBy, _, _, err := store.GetBlockingInfoForIssues(ctx, []string{"mixed-blocking-info-blocked"})
	if err != nil {
		t.Fatalf("GetBlockingInfoForIssues: %v", err)
	}
	if got := blockedBy["mixed-blocking-info-blocked"]; len(got) != 0 {
		t.Fatalf("blockedBy[perm] = %v, want no active blockers", got)
	}
}

func TestIsBlockedReportsNoHistoryWispBlocker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-is-blocked-perm")
	noHistory := &types.Issue{
		ID:        "mixed-is-blocked-no-history",
		Title:     "no-history blocker",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-is-blocked-perm",
		DependsOnID: "mixed-is-blocked-no-history",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency perm blocked by wisp: %v", err)
	}

	blocked, blockers, err := store.IsBlocked(ctx, "mixed-is-blocked-perm")
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked {
		t.Fatalf("IsBlocked = false, want true")
	}
	if len(blockers) != 1 || blockers[0] != "mixed-is-blocked-no-history" {
		t.Fatalf("blockers = %v, want [mixed-is-blocked-no-history]", blockers)
	}
}

func TestGetNewlyUnblockedByCloseIncludesNoHistoryWispCandidate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-close-blocker")
	noHistory := &types.Issue{
		ID:        "mixed-close-unblocked-no-history",
		Title:     "no-history candidate",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history candidate: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-close-unblocked-no-history",
		DependsOnID: "mixed-close-blocker",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency wisp blocked by perm: %v", err)
	}

	unblocked, err := store.GetNewlyUnblockedByClose(ctx, "mixed-close-blocker")
	if err != nil {
		t.Fatalf("GetNewlyUnblockedByClose: %v", err)
	}
	if len(unblocked) != 1 || unblocked[0].ID != "mixed-close-unblocked-no-history" {
		t.Fatalf("unblocked = %+v, want no-history candidate", unblocked)
	}
}

func TestGetNewlyUnblockedByCloseKeepsCandidateBlockedByNoHistoryWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-close-primary-blocker")
	createPerm(t, ctx, store, "mixed-close-still-blocked")
	noHistory := &types.Issue{
		ID:        "mixed-close-remaining-no-history",
		Title:     "remaining no-history blocker",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue remaining no-history blocker: %v", err)
	}
	for _, blockerID := range []string{"mixed-close-primary-blocker", "mixed-close-remaining-no-history"} {
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID:     "mixed-close-still-blocked",
			DependsOnID: blockerID,
			Type:        types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency blocked by %s: %v", blockerID, err)
		}
	}

	unblocked, err := store.GetNewlyUnblockedByClose(ctx, "mixed-close-primary-blocker")
	if err != nil {
		t.Fatalf("GetNewlyUnblockedByClose: %v", err)
	}
	if len(unblocked) != 0 {
		t.Fatalf("unblocked = %+v, want none because no-history blocker is still open", unblocked)
	}
}

func TestSearchIssuesPreferWispOnDuplicateID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue in the issues table (title: "perm mixed-search-duplicate")
	createPerm(t, ctx, store, "mixed-search-duplicate")
	// Insert a row with the same ID into wisps with a distinct title
	if _, err := store.execContext(ctx, `
		INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, no_history)
		VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)
	`, "mixed-search-duplicate", "wisp canonical", types.StatusOpen, 2, types.TypeTask, false, true); err != nil {
		t.Fatalf("insert duplicate wisp row: %v", err)
	}

	// SearchIssues must succeed and return the wisp (canonical) copy, not error.
	results, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues error = %v, want no error on cross-table dup", err)
	}
	var found *types.Issue
	for _, r := range results {
		if r.ID == "mixed-search-duplicate" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("SearchIssues: mixed-search-duplicate not found in results")
	}
	if found.Title != "wisp canonical" {
		t.Errorf("SearchIssues: got title %q, want %q (wisp preferred over issues copy)", found.Title, "wisp canonical")
	}
}

func TestDeleteIssuesNonCascadeDetectsNoHistoryWispDependent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-delete-parent")
	noHistory := &types.Issue{
		ID:        "mixed-delete-wisp-child",
		Title:     "wisp child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-delete-wisp-child",
		DependsOnID: "mixed-delete-parent",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency wisp child: %v", err)
	}

	result, err := store.DeleteIssues(ctx, []string{"mixed-delete-parent"}, false, false, false)
	if err == nil {
		t.Fatal("DeleteIssues succeeded, want dependent safety error")
	}
	if result == nil || !containsID(result.OrphanedIssues, "mixed-delete-wisp-child") {
		t.Fatalf("OrphanedIssues = %+v, want no-history wisp child", result)
	}
}

func TestDeleteIssuesCascadeDeletesNoHistoryWispDependent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-delete-cascade-parent")
	noHistory := &types.Issue{
		ID:        "mixed-delete-cascade-wisp-child",
		Title:     "wisp child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, noHistory, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mixed-delete-cascade-wisp-child",
		DependsOnID: "mixed-delete-cascade-parent",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency wisp child: %v", err)
	}

	dryRun, err := store.DeleteIssues(ctx, []string{"mixed-delete-cascade-parent"}, true, false, true)
	if err != nil {
		t.Fatalf("DeleteIssues dry-run cascade: %v", err)
	}
	if dryRun.DeletedCount != 2 {
		t.Fatalf("dry-run DeletedCount = %d, want parent plus wisp child", dryRun.DeletedCount)
	}

	result, err := store.DeleteIssues(ctx, []string{"mixed-delete-cascade-parent"}, true, false, false)
	if err != nil {
		t.Fatalf("DeleteIssues cascade: %v", err)
	}
	if result.DeletedCount != 2 {
		t.Fatalf("DeletedCount = %d, want parent plus wisp child", result.DeletedCount)
	}
	for _, id := range []string{"mixed-delete-cascade-parent", "mixed-delete-cascade-wisp-child"} {
		if _, err := store.GetIssue(ctx, id); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("GetIssue(%s) after cascade err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestDemoteToWispRecordsOnlyCreateAndDemotionEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mixed-demote-event-contract")
	if err := store.UpdateIssue(ctx, "mixed-demote-event-contract", map[string]interface{}{
		"no_history": true,
		"title":      "demoted without update event",
	}, "tester"); err != nil {
		t.Fatalf("demote issue: %v", err)
	}

	events, err := store.GetEvents(ctx, "mixed-demote-event-contract", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want create plus demotion only: %+v", len(events), events)
	}
	foundDemotion := false
	for _, event := range events {
		if event.NewValue != nil && *event.NewValue == "demoted to wisp" {
			foundDemotion = true
		}
		if event.NewValue != nil && strings.Contains(*event.NewValue, "no_history") {
			t.Fatalf("found intermediate update event in demotion stream: %+v", event)
		}
	}
	if !foundDemotion {
		t.Fatalf("events = %+v, want demotion marker", events)
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func assertDependencyTargetColumns(t *testing.T, store *DoltStore, table, sourceID, targetID string, wantIssueTarget bool) {
	t.Helper()

	ctx, cancel := testContext(t)
	defer cancel()

	var computedTarget, issueTarget, wispTarget sql.NullString
	if err := store.db.QueryRowContext(ctx, `
		SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external), depends_on_issue_id, depends_on_wisp_id
		FROM `+table+`
		WHERE issue_id = ?
	`, sourceID).Scan(&computedTarget, &issueTarget, &wispTarget); err != nil {
		t.Fatalf("query dependency target columns: %v", err)
	}

	if !computedTarget.Valid || computedTarget.String != targetID {
		t.Fatalf("computed dependency target = %+v, want %q", computedTarget, targetID)
	}
	if wantIssueTarget {
		if !issueTarget.Valid || issueTarget.String != targetID {
			t.Fatalf("depends_on_issue_id = %+v, want %q", issueTarget, targetID)
		}
		if wispTarget.Valid {
			t.Fatalf("depends_on_wisp_id = %+v, want NULL", wispTarget)
		}
		return
	}
	if issueTarget.Valid {
		t.Fatalf("depends_on_issue_id = %+v, want NULL", issueTarget)
	}
	if !wispTarget.Valid || wispTarget.String != targetID {
		t.Fatalf("depends_on_wisp_id = %+v, want %q", wispTarget, targetID)
	}
}

func assertNoRowsForIssue(t *testing.T, store *DoltStore, table, issueID string) {
	t.Helper()
	assertRowCountForIssue(t, store, table, issueID, 0)
}

func assertRowCountForIssue(t *testing.T, store *DoltStore, table, issueID string, want int) {
	t.Helper()

	ctx, cancel := testContext(t)
	defer cancel()

	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE issue_id = ?`, issueID).Scan(&count); err != nil {
		if table == "issues" || table == "wisps" {
			if rowErr := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ?`, issueID).Scan(&count); rowErr != nil {
				t.Fatalf("count %s rows: %v", table, rowErr)
			}
		} else {
			t.Fatalf("count %s rows: %v", table, err)
		}
	}
	if count != want {
		t.Fatalf("%s row count for %s = %d, want %d", table, issueID, count, want)
	}
}
