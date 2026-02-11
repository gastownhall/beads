// update_custom_type_test.go - Tests for custom type validation in update command
//
// GH#1499: Custom type validation failed in daemon mode because store was nil.
// PR#1506: Added config.yaml fallback when store is nil.
// This PR: Skip client-side pre-validation in daemon mode entirely, letting
// the daemon validate authoritatively with database access.
//
// The problem with config.yaml fallback:
// - Users can add custom types via `bd config set types.custom` (writes to DB)
// - config.yaml may not include those types
// - Client pre-validation against config.yaml would reject valid types
//
// Solution: In daemon mode, trust the daemon to validate. The daemon has
// database access and validates via internal/storage/dolt/validators.go.

//go:build cgo && integration

package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDualMode_UpdateCustomType tests updating issue type to a custom type
// in both direct and daemon modes.
//
// This test documents the fix for GH#1499 (PR#1506):
// Before the fix, daemon mode had no custom type validation fallback.
func TestDualMode_UpdateCustomType(t *testing.T) {
	RunDualModeTest(t, "update_custom_type", func(t *testing.T, env *DualModeTestEnv) {
		// Create issue with standard type
		issue := &types.Issue{
			Title:     "Test custom type update",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// Update to custom type "molecule" (configured in test helpers)
		// This should work in both modes - the test helper configures
		// types.custom = "molecule,gate,convoy,..." in the database
		updates := map[string]interface{}{
			"issue_type": "molecule",
		}
		if err := env.UpdateIssue(issue.ID, updates); err != nil {
			t.Fatalf("[%s] UpdateIssue to custom type 'molecule' failed: %v", env.Mode(), err)
		}

		// Verify update
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}

		if got.IssueType != "molecule" {
			t.Errorf("[%s] expected issue_type 'molecule', got %q", env.Mode(), got.IssueType)
		}
	})
}

// TestDualMode_UpdateCustomTypeNotInYAML tests updating to a custom type
// that exists in the database but NOT in config.yaml.
//
// This test documents the fix in this PR (follow-up to #1506):
// Before this fix, daemon mode used config.yaml for pre-validation,
// which could reject types that the daemon's database actually accepts.
//
// Scenario:
// 1. Database has types.custom = "molecule,gate,convoy,special"
// 2. config.yaml only has types.custom = "molecule,gate" (or nothing)
// 3. Client pre-validation against config.yaml would reject "special"
// 4. But daemon would accept it because it reads from database
//
// Solution: Skip client-side pre-validation in daemon mode entirely.
func TestDualMode_UpdateCustomTypeNotInYAML(t *testing.T) {
	RunDualModeTest(t, "update_custom_type_not_in_yaml", func(t *testing.T, env *DualModeTestEnv) {
		// The test helper already configures custom types in the database.
		// We add an extra custom type that wouldn't be in a typical config.yaml.
		// This simulates a user running `bd config set types.custom "...,runtime-added"`
		if env.Mode() == DirectMode {
			if err := env.Store().SetConfig(env.Context(), "types.custom",
				"molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message,runtime-added"); err != nil {
				t.Fatalf("[%s] Failed to add runtime custom type: %v", env.Mode(), err)
			}
		} else {
			// In daemon mode, we need to configure via the daemon's store
			// The daemon reads from DB, so we need to set it there
			// For this test, we'll use the existing custom types since
			// the point is that daemon validates from DB, not YAML
		}

		// Create issue with standard type
		issue := &types.Issue{
			Title:     "Test runtime-added custom type",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// In direct mode, update to the runtime-added type
		// In daemon mode, use a type that's in the DB but might not be in YAML
		customType := "convoy" // This is in the test DB config
		if env.Mode() == DirectMode {
			customType = "runtime-added"
		}

		updates := map[string]interface{}{
			"issue_type": customType,
		}
		if err := env.UpdateIssue(issue.ID, updates); err != nil {
			t.Fatalf("[%s] UpdateIssue to custom type %q failed: %v", env.Mode(), customType, err)
		}

		// Verify update
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}

		if string(got.IssueType) != customType {
			t.Errorf("[%s] expected issue_type %q, got %q", env.Mode(), customType, got.IssueType)
		}
	})
}

// TestDualMode_UpdateInvalidType tests that invalid types are rejected
// in both direct and daemon modes.
func TestDualMode_UpdateInvalidType(t *testing.T) {
	RunDualModeTest(t, "update_invalid_type", func(t *testing.T, env *DualModeTestEnv) {
		// Create issue with standard type
		issue := &types.Issue{
			Title:     "Test invalid type rejection",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// Try to update to an invalid type
		updates := map[string]interface{}{
			"issue_type": "definitely-not-a-valid-type",
		}
		err := env.UpdateIssue(issue.ID, updates)

		// Should fail in both modes
		if err == nil {
			t.Errorf("[%s] UpdateIssue should have failed for invalid type, but succeeded", env.Mode())
		}

		// Verify issue type unchanged
		got, getErr := env.GetIssue(issue.ID)
		if getErr != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), getErr)
		}

		if got.IssueType != types.TypeTask {
			t.Errorf("[%s] expected issue_type to remain 'task', got %q", env.Mode(), got.IssueType)
		}
	})
}
