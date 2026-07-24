package dolt

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestDemoteToWisp_InfraDepMirrorUsesSplitColumns verifies that DemoteToWisp
// correctly mirrors dependencies from `dependencies` to `wisp_dependencies`
// using the split target columns (depends_on_issue_id / depends_on_wisp_id /
// depends_on_external) rather than a physical depends_on_id column.
//
// This is the regression gate for the DemoteToWisp INSERT path that previously
// referenced depends_on_id on both sides of the SELECT ... INSERT. After
// migration 0043 drops depends_on_id from `dependencies` and migrations
// ignored/0003+0005 remove it from `wisp_dependencies`, any code that still
// uses depends_on_id in either position fails with errno 1105.
//
// The historically supported infra issue types are covered: agent, rig, role,
// and message. Across this test and the companion demotion tests below, all
// three split target columns are exercised.
func TestDemoteToWisp_InfraDepMirrorUsesSplitColumns(t *testing.T) {
	infraTypes := []types.IssueType{"agent", "rig", "role", "message"}

	for _, infraType := range infraTypes {
		infraType := infraType
		t.Run(string(infraType), func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			// Seed a target issue the infra issue will depend on.
			target := &types.Issue{
				ID:        fmt.Sprintf("test-target-%s", infraType),
				Title:     "dep target",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, target, "tester"); err != nil {
				t.Fatalf("create target: %v", err)
			}

			// Simulate a legacy infra-type issue that lives in `issues` (before
			// the routing logic moved infra types to wisps). Insert directly so
			// IsInfraTypeCtx routing is bypassed.
			infraID := fmt.Sprintf("test-infra-%s", infraType)
			now := time.Now().UTC()
			if _, err := store.db.ExecContext(ctx, `
				INSERT INTO issues
					(id, title, description, design, acceptance_criteria, notes,
					 status, priority, issue_type, created_at, updated_at, created_by)
				VALUES (?, ?, '', '', '', '', 'open', 2, ?, ?, ?, 'tester')
			`, infraID, fmt.Sprintf("%s issue (legacy)", infraType), string(infraType), now, now); err != nil {
				t.Fatalf("insert legacy infra issue: %v", err)
			}

			// Seed a dependency in `dependencies` using the split columns
			// (post-migration 0041/0043 schema — no physical depends_on_id).
			if _, err := store.db.ExecContext(ctx, `
				INSERT INTO dependencies
					(id, issue_id, depends_on_issue_id, type, created_at, created_by)
				VALUES (UUID(), ?, ?, 'blocks', ?, 'tester')
			`, infraID, target.ID, now); err != nil {
				t.Fatalf("insert dep in dependencies: %v", err)
			}

			// DemoteToWisp via UpdateIssue with no_history=true. This triggers
			// the INSERT IGNORE INTO wisp_dependencies SELECT FROM dependencies
			// path. The INSERT must use split columns (not depend_on_id) or
			// it fails with errno 1105 on the post-migration schema.
			if err := store.UpdateIssue(ctx, infraID, map[string]interface{}{
				"no_history": true,
			}, "tester"); err != nil {
				t.Fatalf("DemoteToWisp: %v — INSERT in wisp_dependencies likely still references depends_on_id", err)
			}

			// The dep must now appear in wisp_dependencies.
			var depIssueID sql.NullString
			var depWispID sql.NullString
			var depExternal sql.NullString
			err := store.db.QueryRowContext(ctx, `
				SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external
				FROM wisp_dependencies
				WHERE issue_id = ?
			`, infraID).Scan(&depIssueID, &depWispID, &depExternal)
			if err != nil {
				t.Fatalf("query wisp_dependencies after demotion: %v", err)
			}
			if !depIssueID.Valid || depIssueID.String != target.ID {
				t.Errorf("depends_on_issue_id = %v, want %q", depIssueID, target.ID)
			}
			if depWispID.Valid {
				t.Errorf("depends_on_wisp_id = %q, want NULL", depWispID.String)
			}
			if depExternal.Valid {
				t.Errorf("depends_on_external = %q, want NULL", depExternal.String)
			}

			// Verify COALESCE expression resolves to the correct target — this
			// is the generated depends_on_id equivalent on the split schema.
			var coalesced string
			if err := store.db.QueryRowContext(ctx, `
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)
				FROM wisp_dependencies
				WHERE issue_id = ?
			`, infraID).Scan(&coalesced); err != nil {
				t.Fatalf("COALESCE query wisp_dependencies: %v", err)
			}
			if coalesced != target.ID {
				t.Errorf("COALESCE target = %q, want %q", coalesced, target.ID)
			}
		})
	}
}

// TestDemoteToWisp_InfraDepToWispUsesWispColumn verifies that a dependency
// whose target is a wisp is mirrored with depends_on_wisp_id (not
// depends_on_issue_id) when the source issue is demoted. This ensures the
// FK on depends_on_issue_id (→ issues.id) is not violated by wisp targets.
func TestDemoteToWisp_InfraDepToWispUsesWispColumn(t *testing.T) {
	infraTypes := []types.IssueType{"agent", "rig", "role", "message"}

	for _, infraType := range infraTypes {
		infraType := infraType
		t.Run(string(infraType), func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			// Create a wisp target.
			wispTarget := &types.Issue{
				Title:     "wisp dep target",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Ephemeral: true,
			}
			if err := store.CreateIssue(ctx, wispTarget, "tester"); err != nil {
				t.Fatalf("create wisp target: %v", err)
			}

			// Legacy infra issue in `issues`.
			infraID := fmt.Sprintf("test-wisptar-%s", infraType)
			now := time.Now().UTC()
			if _, err := store.db.ExecContext(ctx, `
				INSERT INTO issues
					(id, title, description, design, acceptance_criteria, notes,
					 status, priority, issue_type, created_at, updated_at, created_by)
				VALUES (?, ?, '', '', '', '', 'open', 2, ?, ?, ?, 'tester')
			`, infraID, fmt.Sprintf("%s wisp-dep", infraType), string(infraType), now, now); err != nil {
				t.Fatalf("insert legacy infra issue: %v", err)
			}

			// Dep in `dependencies` referencing the wisp target via depends_on_wisp_id.
			if _, err := store.db.ExecContext(ctx, `
				INSERT INTO dependencies
					(id, issue_id, depends_on_wisp_id, type, created_at, created_by)
				VALUES (UUID(), ?, ?, 'blocks', ?, 'tester')
			`, infraID, wispTarget.ID, now); err != nil {
				t.Fatalf("insert dep with wisp target: %v", err)
			}

			// Demote; triggers the INSERT IGNORE INTO wisp_dependencies SELECT FROM dependencies.
			if err := store.UpdateIssue(ctx, infraID, map[string]interface{}{
				"no_history": true,
			}, "tester"); err != nil {
				t.Fatalf("DemoteToWisp: %v", err)
			}

			// wisp_dependencies must use depends_on_wisp_id for the wisp target.
			var depWispID string
			var depIssueID sql.NullString
			if err := store.db.QueryRowContext(ctx, `
				SELECT depends_on_wisp_id, depends_on_issue_id
				FROM wisp_dependencies
				WHERE issue_id = ?
			`, infraID).Scan(&depWispID, &depIssueID); err != nil {
				t.Fatalf("query wisp_dependencies: %v", err)
			}
			if depWispID != wispTarget.ID {
				t.Errorf("depends_on_wisp_id = %q, want %q", depWispID, wispTarget.ID)
			}
			if depIssueID.Valid {
				t.Errorf("depends_on_issue_id = %q, want NULL", depIssueID.String)
			}
		})
	}
}

// TestDemoteToWisp_InfraDepToExternalUsesExternalColumn verifies that an
// external dependency is mirrored through depends_on_external while the issue
// and wisp target columns remain NULL.
func TestDemoteToWisp_InfraDepToExternalUsesExternalColumn(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	infraID := "test-external-agent"
	externalTarget := "external:ticket-123"
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO issues
			(id, title, description, design, acceptance_criteria, notes,
			 status, priority, issue_type, created_at, updated_at, created_by)
		VALUES (?, 'agent external dep', '', '', '', '', 'open', 2, 'agent', ?, ?, 'tester')
	`, infraID, now, now); err != nil {
		t.Fatalf("insert legacy infra issue: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies
			(id, issue_id, depends_on_external, type, created_at, created_by)
		VALUES (UUID(), ?, ?, 'blocks', ?, 'tester')
	`, infraID, externalTarget, now); err != nil {
		t.Fatalf("insert external dep in dependencies: %v", err)
	}

	if err := store.UpdateIssue(ctx, infraID, map[string]interface{}{
		"no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("DemoteToWisp: %v", err)
	}

	var depIssueID sql.NullString
	var depWispID sql.NullString
	var depExternal string
	if err := store.db.QueryRowContext(ctx, `
		SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external
		FROM wisp_dependencies
		WHERE issue_id = ?
	`, infraID).Scan(&depIssueID, &depWispID, &depExternal); err != nil {
		t.Fatalf("query wisp_dependencies: %v", err)
	}
	if depIssueID.Valid {
		t.Errorf("depends_on_issue_id = %q, want NULL", depIssueID.String)
	}
	if depWispID.Valid {
		t.Errorf("depends_on_wisp_id = %q, want NULL", depWispID.String)
	}
	if depExternal != externalTarget {
		t.Errorf("depends_on_external = %q, want %q", depExternal, externalTarget)
	}
}

// TestAddWispDep_InfraWispToInfraWisp verifies that when an infra-type wisp
// depends on another infra-type wisp, the dependency row in wisp_dependencies
// uses depends_on_wisp_id (not depends_on_issue_id or a physical depends_on_id).
// This exercises the addWispDependency INSERT path with a wisp target.
func TestAddWispDep_InfraWispToInfraWisp(t *testing.T) {
	infraTypes := []types.IssueType{"agent", "rig", "role", "message"}

	for _, infraType := range infraTypes {
		infraType := infraType
		t.Run(string(infraType), func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			// Register the historical set as infra types and the non-built-in
			// values as custom types. "rig" is no longer an infra default.
			if err := store.SetConfig(ctx, "types.infra", "agent,rig,role,message"); err != nil {
				t.Fatalf("SetConfig types.infra: %v", err)
			}
			if err := store.SetConfig(ctx, "types.custom", "agent,rig,role"); err != nil {
				t.Fatalf("SetConfig types.custom: %v", err)
			}

			// Both source and target are infra-type wisps. CreateIssue routes
			// infra types to wisps automatically (IsInfraTypeCtx).
			source := &types.Issue{
				Title:     fmt.Sprintf("%s source wisp", infraType),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: infraType,
			}
			depTarget := &types.Issue{
				Title:     fmt.Sprintf("%s target wisp", infraType),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: infraType,
			}
			for _, w := range []*types.Issue{source, depTarget} {
				if err := store.CreateIssue(ctx, w, "tester"); err != nil {
					t.Fatalf("create wisp %q: %v", w.Title, err)
				}
			}

			dep := &types.Dependency{
				IssueID:     source.ID,
				DependsOnID: depTarget.ID,
				Type:        types.DepBlocks,
			}
			if err := store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("AddDependency (wisp→wisp): %v", err)
			}

			// wisp_dependencies must use depends_on_wisp_id for a wisp target.
			var depWispID string
			var depIssueID sql.NullString
			if err := store.db.QueryRowContext(ctx, `
				SELECT depends_on_wisp_id, depends_on_issue_id
				FROM wisp_dependencies
				WHERE issue_id = ?
			`, source.ID).Scan(&depWispID, &depIssueID); err != nil {
				t.Fatalf("query wisp_dependencies: %v", err)
			}
			if depWispID != depTarget.ID {
				t.Errorf("depends_on_wisp_id = %q, want %q", depWispID, depTarget.ID)
			}
			if depIssueID.Valid {
				t.Errorf("depends_on_issue_id = %q, want NULL (wisp target must not pollute issue FK column)", depIssueID.String)
			}

			// COALESCE must resolve to the target wisp ID.
			var coalesced string
			if err := store.db.QueryRowContext(ctx, `
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)
				FROM wisp_dependencies
				WHERE issue_id = ?
			`, source.ID).Scan(&coalesced); err != nil {
				t.Fatalf("COALESCE query: %v", err)
			}
			if coalesced != depTarget.ID {
				t.Errorf("COALESCE = %q, want %q", coalesced, depTarget.ID)
			}
		})
	}
}

// TestAddWispDep_InfraWispToIssue verifies that when an infra-type wisp
// depends on a regular issue, depends_on_issue_id is set and depends_on_wisp_id
// is NULL. Ensures issue-target deps do not bleed into the wisp column.
func TestAddWispDep_InfraWispToIssue(t *testing.T) {
	infraTypes := []types.IssueType{"agent", "rig", "role", "message"}

	for _, infraType := range infraTypes {
		infraType := infraType
		t.Run(string(infraType), func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			// Register the historical set as infra types and the non-built-in
			// values as custom types. "rig" is no longer an infra default.
			if err := store.SetConfig(ctx, "types.infra", "agent,rig,role,message"); err != nil {
				t.Fatalf("SetConfig types.infra: %v", err)
			}
			if err := store.SetConfig(ctx, "types.custom", "agent,rig,role"); err != nil {
				t.Fatalf("SetConfig types.custom: %v", err)
			}

			source := &types.Issue{
				Title:     fmt.Sprintf("%s to-issue wisp", infraType),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: infraType,
			}
			issueTarget := &types.Issue{
				Title:     "regular issue target",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			for _, i := range []*types.Issue{source, issueTarget} {
				if err := store.CreateIssue(ctx, i, "tester"); err != nil {
					t.Fatalf("create %q: %v", i.Title, err)
				}
			}

			dep := &types.Dependency{
				IssueID:     source.ID,
				DependsOnID: issueTarget.ID,
				Type:        types.DepBlocks,
			}
			if err := store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("AddDependency (wisp→issue): %v", err)
			}

			var depIssueID string
			var depWispID sql.NullString
			if err := store.db.QueryRowContext(ctx, `
				SELECT depends_on_issue_id, depends_on_wisp_id
				FROM wisp_dependencies
				WHERE issue_id = ?
			`, source.ID).Scan(&depIssueID, &depWispID); err != nil {
				t.Fatalf("query wisp_dependencies: %v", err)
			}
			if depIssueID != issueTarget.ID {
				t.Errorf("depends_on_issue_id = %q, want %q", depIssueID, issueTarget.ID)
			}
			if depWispID.Valid {
				t.Errorf("depends_on_wisp_id = %q, want NULL", depWispID.String)
			}
		})
	}
}
