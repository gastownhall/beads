// update_edge_cases_test.go - Integration tests for bd update edge cases.
//
// Covers scenarios not tested elsewhere:
// - Nonexistent issue IDs (dual mode + CLI)
// - Clearing field values (due date, defer, assignee)
// - Invalid priority values and negative estimates
// - Invalid due date format
// - Very long field values (title limit, large descriptions)
// - Multiple field updates at once
// - Various field updates (estimate, external-ref, spec-id, design, acceptance criteria)
// - Status transitions
// - JSON output mode
// - Last-touched implicit ID

//go:build cgo && integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDualMode_UpdateNonexistentIssue tests that updating a nonexistent ID returns an error.
func TestDualMode_UpdateNonexistentIssue(t *testing.T) {
	RunDualModeTest(t, "update_nonexistent", func(t *testing.T, env *DualModeTestEnv) {
		err := env.UpdateIssue("nonexistent-id-xyz", map[string]interface{}{
			"title": "Should fail",
		})
		if err == nil {
			t.Errorf("[%s] UpdateIssue should fail for nonexistent ID", env.Mode())
		}
	})
}

// TestDualMode_UpdateLongFieldValues tests that very long strings are stored and retrieved correctly.
func TestDualMode_UpdateLongFieldValues(t *testing.T) {
	RunDualModeTest(t, "update_long_values", func(t *testing.T, env *DualModeTestEnv) {
		issue := &types.Issue{
			Title:     "Long value test",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// Max-length title (500 chars)
		longTitle := strings.Repeat("A", 500)
		err := env.UpdateIssue(issue.ID, map[string]interface{}{
			"title": longTitle,
		})
		if err != nil {
			t.Fatalf("[%s] UpdateIssue with 500-char title failed: %v", env.Mode(), err)
		}
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}
		if got.Title != longTitle {
			t.Errorf("[%s] 500-char title not preserved, got length %d want 500", env.Mode(), len(got.Title))
		}

		// Title exceeding limit should fail
		tooLongTitle := strings.Repeat("A", 501)
		err = env.UpdateIssue(issue.ID, map[string]interface{}{
			"title": tooLongTitle,
		})
		if err == nil {
			t.Errorf("[%s] UpdateIssue with 501-char title should have failed", env.Mode())
		}

		// 100KB description (no length limit on descriptions)
		longDesc := strings.Repeat("B", 100000)
		err = env.UpdateIssue(issue.ID, map[string]interface{}{
			"description": longDesc,
		})
		if err != nil {
			t.Fatalf("[%s] UpdateIssue with 100KB description failed: %v", env.Mode(), err)
		}
		got, err = env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue after long desc failed: %v", env.Mode(), err)
		}
		if got.Description != longDesc {
			t.Errorf("[%s] 100KB description not preserved, got length %d want 100000", env.Mode(), len(got.Description))
		}
	})
}

// TestDualMode_UpdateMultipleFields tests updating several fields in a single call.
func TestDualMode_UpdateMultipleFields(t *testing.T) {
	RunDualModeTest(t, "update_multiple_fields", func(t *testing.T, env *DualModeTestEnv) {
		issue := &types.Issue{
			Title:     "Multi update test",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  3,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		updates := map[string]interface{}{
			"title":       "Updated multi",
			"status":      types.StatusInProgress,
			"priority":    1,
			"description": "New description",
		}
		if err := env.UpdateIssue(issue.ID, updates); err != nil {
			t.Fatalf("[%s] UpdateIssue failed: %v", env.Mode(), err)
		}

		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}
		if got.Title != "Updated multi" {
			t.Errorf("[%s] title = %q, want %q", env.Mode(), got.Title, "Updated multi")
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("[%s] status = %q, want %q", env.Mode(), got.Status, types.StatusInProgress)
		}
		if got.Priority != 1 {
			t.Errorf("[%s] priority = %d, want 1", env.Mode(), got.Priority)
		}
		if got.Description != "New description" {
			t.Errorf("[%s] description = %q, want %q", env.Mode(), got.Description, "New description")
		}
	})
}

// TestCLI_UpdateClearDescription tests clearing a description by setting it to empty string.
func TestCLI_UpdateClearDescription(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create issue with description
	out := runBDInProcess(t, tmpDir, "create", "Clear desc test", "-p", "1",
		"--description", "Initial description", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Clear description by setting to empty
	runBDInProcess(t, tmpDir, "update", id, "--description", "")

	// Verify description is cleared
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	desc, _ := updated[0]["description"].(string)
	if desc != "" {
		t.Errorf("Expected empty description after clearing, got: %q", desc)
	}
}

// TestDualMode_UpdateClearFields tests clearing various fields at the storage level.
func TestDualMode_UpdateClearFields(t *testing.T) {
	RunDualModeTest(t, "update_clear_fields", func(t *testing.T, env *DualModeTestEnv) {
		// Create issue with fields populated
		issue := &types.Issue{
			Title:       "Clear fields test",
			IssueType:   types.TypeTask,
			Status:      types.StatusOpen,
			Priority:    2,
			Description: "To be cleared",
			Assignee:    "alice",
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// Clear description
		if err := env.UpdateIssue(issue.ID, map[string]interface{}{
			"description": "",
		}); err != nil {
			t.Fatalf("[%s] clearing description failed: %v", env.Mode(), err)
		}
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}
		if got.Description != "" {
			t.Errorf("[%s] description should be empty, got: %q", env.Mode(), got.Description)
		}

		// Clear assignee
		if err := env.UpdateIssue(issue.ID, map[string]interface{}{
			"assignee": "",
		}); err != nil {
			t.Fatalf("[%s] clearing assignee failed: %v", env.Mode(), err)
		}
		got, err = env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}
		if got.Assignee != "" {
			t.Errorf("[%s] assignee should be empty, got: %q", env.Mode(), got.Assignee)
		}
	})
}

// TestCLI_UpdateInvalidPriority tests that invalid priority values are rejected.
// Uses exec.Command because FatalErrorRespectJSON calls os.Exit(1).
func TestCLI_UpdateInvalidPriority(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := initExecTestDB(t)
	id := createExecTestIssue(t, tmpDir, "Priority test")

	invalidPriorities := []struct {
		name     string
		priority string
	}{
		{"too_high", "5"},
		{"text", "high"},
		{"negative", "-1"},
		{"special_chars", "!@#"},
	}

	for _, tc := range invalidPriorities {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(testBD, "--no-daemon", "update", id, "-p", tc.priority)
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Errorf("Expected error for priority %q, but command succeeded. Output: %s", tc.priority, out)
			}
			if !strings.Contains(string(out), "invalid priority") {
				t.Errorf("Expected 'invalid priority' in error for %q, got: %s", tc.priority, out)
			}
		})
	}
}

// TestCLI_UpdateNonexistentIssueCLI tests CLI behavior when updating a nonexistent issue.
func TestCLI_UpdateNonexistentIssueCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Use runBDInProcessAllowError since this produces stderr output, not os.Exit
	_, stderr, _ := runBDInProcessAllowError(t, tmpDir, "update", "nonexistent-xyz123", "--title", "Should fail")

	// The command should produce an error message about not finding the issue
	if !strings.Contains(stderr, "not found") && !strings.Contains(stderr, "Error") {
		t.Errorf("Expected error message for nonexistent ID, got stderr: %q", stderr)
	}
}

// TestCLI_UpdateEstimate tests setting and updating the time estimate.
func TestCLI_UpdateEstimate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Estimate test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Set estimate
	runBDInProcess(t, tmpDir, "update", id, "--estimate", "60")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	estimate := updated[0]["estimated_minutes"]
	if estimate == nil || estimate.(float64) != 60 {
		t.Errorf("Expected estimated_minutes=60, got: %v", estimate)
	}

	// Update estimate to different value
	runBDInProcess(t, tmpDir, "update", id, "--estimate", "120")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	json.Unmarshal([]byte(out), &updated)
	estimate = updated[0]["estimated_minutes"]
	if estimate == nil || estimate.(float64) != 120 {
		t.Errorf("Expected estimated_minutes=120, got: %v", estimate)
	}
}

// TestCLI_UpdateExternalRef tests setting the external reference field.
func TestCLI_UpdateExternalRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "External ref test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Set external reference
	runBDInProcess(t, tmpDir, "update", id, "--external-ref", "GH#1234")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	ref, _ := updated[0]["external_ref"].(string)
	if ref != "GH#1234" {
		t.Errorf("Expected external_ref='GH#1234', got: %q", ref)
	}
}

// TestCLI_UpdateNegativeEstimate tests that negative estimates are rejected.
// Uses exec.Command because FatalErrorRespectJSON calls os.Exit(1).
func TestCLI_UpdateNegativeEstimate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := initExecTestDB(t)
	id := createExecTestIssue(t, tmpDir, "Estimate test")

	// Try negative estimate
	cmd := exec.Command(testBD, "--no-daemon", "update", id, "--estimate", "-5")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("Expected error for negative estimate, but command succeeded. Output: %s", out)
	}
	if !strings.Contains(string(out), "non-negative") {
		t.Errorf("Expected 'non-negative' in error, got: %s", out)
	}
}

// TestCLI_UpdateSpecID tests setting the spec-id field.
func TestCLI_UpdateSpecID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Spec ID test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Set spec ID
	runBDInProcess(t, tmpDir, "update", id, "--spec-id", "SPEC-001")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	specID, _ := updated[0]["spec_id"].(string)
	if specID != "SPEC-001" {
		t.Errorf("Expected spec_id='SPEC-001', got: %q", specID)
	}
}

// TestCLI_UpdateDesignField tests setting the design field.
func TestCLI_UpdateDesignField(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Design field test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	designText := "## Architecture\n\nUse microservices pattern with gRPC."
	runBDInProcess(t, tmpDir, "update", id, "--design", designText)

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	got, _ := updated[0]["design"].(string)
	if got != designText {
		t.Errorf("Expected design=%q, got: %q", designText, got)
	}
}

// TestCLI_UpdateAcceptanceCriteria tests the --acceptance flag.
func TestCLI_UpdateAcceptanceCriteria(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Acceptance test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Set acceptance criteria using --acceptance
	ac := "- [ ] Tests pass\n- [ ] Code reviewed"
	runBDInProcess(t, tmpDir, "update", id, "--acceptance", ac)

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	got, _ := updated[0]["acceptance_criteria"].(string)
	if got != ac {
		t.Errorf("Expected acceptance_criteria=%q, got: %q", ac, got)
	}
}

// TestCLI_UpdateInvalidDueDate tests that invalid --due format is rejected.
// Uses exec.Command because FatalErrorRespectJSON calls os.Exit(1).
func TestCLI_UpdateInvalidDueDate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := initExecTestDB(t)
	id := createExecTestIssue(t, tmpDir, "Due date test")

	cmd := exec.Command(testBD, "--no-daemon", "update", id, "--due", "not-a-date")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("Expected error for invalid --due, but command succeeded. Output: %s", out)
	}
	if !strings.Contains(string(out), "invalid --due format") {
		t.Errorf("Expected 'invalid --due format' in error, got: %s", out)
	}
}

// initExecTestDB initializes a test database using exec.Command and returns
// the tmpDir. Helper to reduce boilerplate in exec.Command-based tests.
func initExecTestDB(t *testing.T) string {
	t.Helper()
	tmpDir := createTempDirWithCleanup(t)
	initCmd := exec.Command(testBD, "init", "--prefix", "test", "--quiet")
	initCmd.Dir = tmpDir
	initCmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}
	return tmpDir
}

// createExecTestIssue creates a test issue using exec.Command and returns the ID.
func createExecTestIssue(t *testing.T, tmpDir, title string) string {
	t.Helper()
	createCmd := exec.Command(testBD, "--no-daemon", "create", title, "-p", "1", "--json")
	createCmd.Dir = tmpDir
	createCmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := createCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, out)
	}
	jsonStart := strings.Index(string(out), "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON in create output: %s", out)
	}
	var issue map[string]interface{}
	json.Unmarshal(out[jsonStart:], &issue)
	return issue["id"].(string)
}

// TestCLI_UpdateJSONOutput tests that --json flag produces valid JSON output.
func TestCLI_UpdateJSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "JSON output test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Update with --json flag
	out = runBDInProcess(t, tmpDir, "update", id, "--title", "Updated title", "--json")

	// Find JSON in output (could be array or object)
	jsonStart := strings.Index(out, "[")
	if jsonStart < 0 {
		jsonStart = strings.Index(out, "{")
	}
	if jsonStart < 0 {
		t.Fatalf("No JSON found in update --json output: %s", out)
	}

	// Verify it's valid JSON
	jsonOut := out[jsonStart:]
	if !json.Valid([]byte(jsonOut)) {
		t.Errorf("Update --json output is not valid JSON: %s", jsonOut)
	}
}

// TestCLI_UpdateLongTitleCLI tests updating with the maximum title length via CLI.
func TestCLI_UpdateLongTitleCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Long title test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Update with max-length title (500 chars)
	longTitle := strings.Repeat("X", 500)
	runBDInProcess(t, tmpDir, "update", id, "--title", longTitle)

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	got, _ := updated[0]["title"].(string)
	if got != longTitle {
		t.Errorf("Max-length title not preserved via CLI, got length %d want 500", len(got))
	}
}

// TestCLI_UpdateStatusTransitions tests common status transitions.
func TestCLI_UpdateStatusTransitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Status transitions test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	transitions := []string{"in_progress", "blocked", "in_progress", "closed"}
	for _, status := range transitions {
		runBDInProcess(t, tmpDir, "update", id, "--status", status)

		out = runBDInProcess(t, tmpDir, "show", id, "--json")
		var updated []map[string]interface{}
		json.Unmarshal([]byte(out), &updated)
		if updated[0]["status"] != status {
			t.Errorf("After transition to %q: got status %v", status, updated[0]["status"])
		}
	}
}

// TestCLI_UpdateFromLastTouched tests that update without an ID uses last-touched issue.
func TestCLI_UpdateFromLastTouched(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create an issue (sets last-touched)
	out := runBDInProcess(t, tmpDir, "create", "Last touched test", "-p", "1", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Write last-touched file so update knows which issue to use
	lastTouchedFile := filepath.Join(tmpDir, ".beads", ".last_touched_id")
	os.WriteFile(lastTouchedFile, []byte(id), 0644)

	// Update without specifying an ID (should use last-touched)
	runBDInProcess(t, tmpDir, "update", "--status", "in_progress")

	// Verify the update was applied to the last-touched issue
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	if updated[0]["status"] != "in_progress" {
		t.Errorf("Expected last-touched issue to be updated to in_progress, got: %v", updated[0]["status"])
	}
}

// TestCLI_UpdateMultipleIssuesExec tests updating multiple issue IDs in a single command.
// Uses exec.Command for clean process isolation.
func TestCLI_UpdateMultipleIssuesExec(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := initExecTestDB(t)
	id1 := createExecTestIssue(t, tmpDir, "First issue")
	id2 := createExecTestIssue(t, tmpDir, "Second issue")

	// Update both at once
	cmd := exec.Command(testBD, "--no-daemon", "update", id1, id2, "--status", "in_progress")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update failed: %v\n%s", err, out)
	}

	// Verify both were updated
	for _, id := range []string{id1, id2} {
		showCmd := exec.Command(testBD, "--no-daemon", "show", id, "--json")
		showCmd.Dir = tmpDir
		showCmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		showOut, err := showCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("show %s failed: %v\n%s", id, err, showOut)
		}
		var details []map[string]interface{}
		json.Unmarshal(showOut, &details)
		if details[0]["status"] != "in_progress" {
			t.Errorf("Issue %s: expected status 'in_progress', got: %v", id, details[0]["status"])
		}
	}
}
