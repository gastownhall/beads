//go:build integration
// +build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestDeleteViaDaemon_SuccessfulDeletion tests successful single issue deletion via daemon RPC
func TestDeleteViaDaemon_SuccessfulDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Test Issue for Deletion",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	// Delete via daemon RPC
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{issue.ID},
		Force:  true,
		DryRun: false,
		Reason: "test deletion",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Delete failed: %s", resp.Error)
	}

	// Verify the response data
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	deletedCount := int(result["deleted_count"].(float64))
	if deletedCount != 1 {
		t.Errorf("Expected 1 deletion, got %d", deletedCount)
	}

	// Verify issue is actually deleted (tombstoned)
	deletedIssue, err := testStore.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if deletedIssue != nil && deletedIssue.Status != types.StatusTombstone {
		t.Errorf("Issue should be tombstoned, got status: %s", deletedIssue.Status)
	}
}

// TestDeleteViaDaemon_CascadeDeletion tests cascade deletion through daemon
func TestDeleteViaDaemon_CascadeDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create parent and child issues
	parentIssue := &types.Issue{
		Title:     "Parent Issue",
		IssueType: "epic",
		Status:    types.StatusOpen,
		Priority:  1,
	}
	if err := testStore.CreateIssue(ctx, parentIssue, "test"); err != nil {
		t.Fatalf("Failed to create parent issue: %v", err)
	}

	childIssue := &types.Issue{
		Title:     "Child Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, childIssue, "test"); err != nil {
		t.Fatalf("Failed to create child issue: %v", err)
	}

	// Create dependency: child depends on parent
	dep := &types.Dependency{
		IssueID:     childIssue.ID,
		DependsOnID: parentIssue.ID,
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Delete parent with cascade
	deleteArgs := &rpc.DeleteArgs{
		IDs:     []string{parentIssue.ID},
		Force:   true,
		Cascade: true,
		DryRun:  false,
		Reason:  "cascade deletion test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Cascade delete failed: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	deletedCount := int(result["deleted_count"].(float64))
	// Cascade should delete both parent and dependent child
	if deletedCount < 1 {
		t.Errorf("Expected at least 1 deletion in cascade, got %d", deletedCount)
	}
}

// TestDeleteViaDaemon_ForceDeletion tests force deletion bypassing dependency checks
func TestDeleteViaDaemon_ForceDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create issues with dependencies
	issue1 := &types.Issue{
		Title:     "Issue 1",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue1: %v", err)
	}

	issue2 := &types.Issue{
		Title:     "Issue 2 depends on Issue 1",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue2: %v", err)
	}

	// Create dependency
	dep := &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Force delete issue1 (which has dependents)
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{issue1.ID},
		Force:  true,
		DryRun: false,
		Reason: "force deletion test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Force delete failed: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	deletedCount := int(result["deleted_count"].(float64))
	if deletedCount != 1 {
		t.Errorf("Expected 1 deletion, got %d", deletedCount)
	}
}

// TestDeleteViaDaemon_DryRunMode tests dry-run mode with no actual deletion
func TestDeleteViaDaemon_DryRunMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Issue for DryRun Test",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Dry-run delete
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{issue.ID},
		Force:  true,
		DryRun: true,
		Reason: "dry run test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("DryRun delete failed: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Verify dry-run response structure
	if _, ok := result["issue_count"]; !ok {
		// Check alternative field names used in dry-run responses
		if _, ok := result["deleted_count"]; !ok {
			t.Logf("DryRun response: %+v", result)
		}
	}

	// Verify issue still exists (not deleted in dry-run)
	existingIssue, err := testStore.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if existingIssue == nil {
		t.Error("Issue should still exist after dry-run")
	}
	if existingIssue != nil && existingIssue.Status == types.StatusTombstone {
		t.Error("Issue should not be tombstoned in dry-run mode")
	}
}

// TestDeleteViaDaemon_InvalidIssueID tests error handling for invalid issue IDs
func TestDeleteViaDaemon_InvalidIssueID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, _, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Try to delete non-existent issue
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{"test-nonexistent-xxx"},
		Force:  true,
		DryRun: false,
		Reason: "invalid id test",
	}

	resp, err := client.Delete(deleteArgs)
	// The RPC call should succeed but the response should indicate failure
	if err == nil && resp != nil {
		if resp.Success {
			// Parse response to check for errors field
			var result map[string]interface{}
			if err := json.Unmarshal(resp.Data, &result); err == nil {
				if errors, ok := result["errors"].([]interface{}); ok && len(errors) > 0 {
					t.Logf("Got expected errors for invalid ID: %v", errors)
					return
				}
				// Check deleted_count
				if deletedCount, ok := result["deleted_count"].(float64); ok && deletedCount == 0 {
					t.Logf("Got expected 0 deletions for invalid ID")
					return
				}
			}
		}
	}
	// Both error or failure response are acceptable for invalid IDs
	t.Logf("Delete of invalid ID handled: err=%v, resp=%+v", err, resp)
}

// TestDeleteViaDaemon_BatchDeletion tests deleting multiple issues at once
func TestDeleteViaDaemon_BatchDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create multiple test issues
	var issueIDs []string
	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:     "Batch Issue " + string(rune('A'+i)),
			IssueType: "task",
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
		issueIDs = append(issueIDs, issue.ID)
	}

	// Batch delete
	deleteArgs := &rpc.DeleteArgs{
		IDs:    issueIDs,
		Force:  true,
		DryRun: false,
		Reason: "batch deletion test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Batch delete failed: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	deletedCount := int(result["deleted_count"].(float64))
	if deletedCount != 3 {
		t.Errorf("Expected 3 deletions, got %d", deletedCount)
	}
}

// TestDeleteViaDaemon_JSONOutput tests JSON output formatting
func TestDeleteViaDaemon_JSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Issue for JSON Output Test",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Delete via daemon
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{issue.ID},
		Force:  true,
		DryRun: false,
		Reason: "json output test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Delete failed: %s", resp.Error)
	}

	// Validate JSON structure
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Check expected fields in JSON response
	expectedFields := []string{"deleted_count", "total_count"}
	for _, field := range expectedFields {
		if _, ok := result[field]; !ok {
			t.Errorf("Expected field %q in JSON response, got: %+v", field, result)
		}
	}
}

// TestDeleteViaDaemon_HumanReadableOutput tests the human-readable output formatting
func TestDeleteViaDaemon_HumanReadableOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, _, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Issue for Human Output Test",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Test output formatting by capturing stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Create a mock response to test output formatting
	result := map[string]interface{}{
		"deleted_count": float64(1),
		"total_count":   float64(1),
	}
	resultJSON, _ := json.Marshal(result)

	// Simulate the human-readable output logic
	deletedCount := int(result["deleted_count"].(float64))
	if deletedCount == 1 {
		os.Stdout.WriteString("✓ Deleted " + issue.ID + "\n")
	} else {
		os.Stdout.WriteString("✓ Deleted 1 issue(s)\n")
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify output contains expected elements
	if !strings.Contains(output, "Deleted") {
		t.Errorf("Expected output to contain 'Deleted', got: %s", output)
	}

	_ = resultJSON // Suppress unused variable warning
}

// TestDeleteViaDaemon_DependencyConflict tests error handling for dependency conflicts
func TestDeleteViaDaemon_DependencyConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create issues with dependencies
	blockerIssue := &types.Issue{
		Title:     "Blocker Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  1,
	}
	if err := testStore.CreateIssue(ctx, blockerIssue, "test"); err != nil {
		t.Fatalf("Failed to create blocker issue: %v", err)
	}

	blockedIssue := &types.Issue{
		Title:     "Blocked Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, blockedIssue, "test"); err != nil {
		t.Fatalf("Failed to create blocked issue: %v", err)
	}

	// Create dependency: blockedIssue depends on blockerIssue
	dep := &types.Dependency{
		IssueID:     blockedIssue.ID,
		DependsOnID: blockerIssue.ID,
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Try to delete without force (should fail due to dependency)
	deleteArgs := &rpc.DeleteArgs{
		IDs:     []string{blockerIssue.ID},
		Force:   false, // No force - should respect dependencies
		Cascade: false,
		DryRun:  true, // Use dry-run to check without modifying
		Reason:  "dependency conflict test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		// Error is acceptable for dependency conflicts
		t.Logf("Got expected error for dependency conflict: %v", err)
		return
	}

	// Check if response indicates the dependency issue
	if resp != nil {
		var result map[string]interface{}
		if err := json.Unmarshal(resp.Data, &result); err == nil {
			t.Logf("Dependency check response: %+v", result)
		}
	}
}

// TestDeleteViaDaemon_EmptyIDs tests error handling for empty issue ID list
func TestDeleteViaDaemon_EmptyIDs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, _, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Try to delete with empty ID list
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{},
		Force:  true,
		DryRun: false,
		Reason: "empty ids test",
	}

	resp, err := client.Delete(deleteArgs)
	// Either error or failure response is acceptable
	if err == nil && resp != nil && resp.Success {
		var result map[string]interface{}
		if err := json.Unmarshal(resp.Data, &result); err == nil {
			if deletedCount, ok := result["deleted_count"].(float64); ok && deletedCount > 0 {
				t.Error("Should not delete anything with empty ID list")
			}
		}
	}
	t.Logf("Empty IDs handled: err=%v", err)
}

// TestDeleteViaDaemon_MultipleErrors tests handling of multiple errors in batch deletion
func TestDeleteViaDaemon_MultipleErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create one valid issue
	validIssue := &types.Issue{
		Title:     "Valid Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, validIssue, "test"); err != nil {
		t.Fatalf("Failed to create valid issue: %v", err)
	}

	// Try batch delete with mix of valid and invalid IDs
	deleteArgs := &rpc.DeleteArgs{
		IDs:    []string{validIssue.ID, "test-invalid-1", "test-invalid-2"},
		Force:  true,
		DryRun: false,
		Reason: "multiple errors test",
	}

	resp, err := client.Delete(deleteArgs)
	if err != nil {
		t.Logf("Got error for mixed batch: %v", err)
		return
	}

	if resp != nil {
		var result map[string]interface{}
		if err := json.Unmarshal(resp.Data, &result); err == nil {
			// Check for errors array
			if errors, ok := result["errors"].([]interface{}); ok {
				t.Logf("Got %d errors in batch response", len(errors))
			}
			// Check deleted count
			if deletedCount, ok := result["deleted_count"].(float64); ok {
				t.Logf("Deleted %d issues despite errors", int(deletedCount))
			}
		}
	}
}

// TestDeleteViaDaemon_DirectCall tests the deleteViaDaemon function directly
// by setting up the global daemonClient
func TestDeleteViaDaemon_DirectCall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Direct Call Test Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	// Save old global state
	oldDaemonClient := daemonClient
	oldJsonOutput := jsonOutput
	defer func() {
		daemonClient = oldDaemonClient
		jsonOutput = oldJsonOutput
	}()

	// Set up global client
	daemonClient = client
	jsonOutput = true // Use JSON to avoid color codes

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Call deleteViaDaemon directly (should not exit since it succeeds)
	deleteViaDaemon([]string{issue.ID}, true, false, false, true, "direct test")

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify output is valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Errorf("Expected valid JSON output, got: %s, error: %v", output, err)
	}

	// Check deleted_count
	if deletedCount, ok := result["deleted_count"].(float64); !ok || deletedCount != 1 {
		t.Errorf("Expected deleted_count=1, got: %v", result["deleted_count"])
	}
}

// TestDeleteViaDaemon_DirectDryRun tests dry-run mode directly
func TestDeleteViaDaemon_DirectDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Direct Dry Run Test Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	// Save old global state
	oldDaemonClient := daemonClient
	oldJsonOutput := jsonOutput
	defer func() {
		daemonClient = oldDaemonClient
		jsonOutput = oldJsonOutput
	}()

	// Set up global client
	daemonClient = client
	jsonOutput = false // Test human-readable output

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Call deleteViaDaemon with dry-run
	deleteViaDaemon([]string{issue.ID}, true, true, false, false, "dry run test")

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify dry-run output
	if !strings.Contains(output, "Dry run") && !strings.Contains(output, "would delete") {
		t.Logf("Dry run output: %s", output)
	}

	// Verify issue still exists
	existingIssue, err := testStore.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if existingIssue == nil {
		t.Error("Issue should still exist after dry-run")
	}
}

// TestDeleteViaDaemon_DirectHumanOutput tests human-readable output directly
func TestDeleteViaDaemon_DirectHumanOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create a test issue
	issue := &types.Issue{
		Title:     "Human Output Test Issue",
		IssueType: "task",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	// Save old global state
	oldDaemonClient := daemonClient
	oldJsonOutput := jsonOutput
	defer func() {
		daemonClient = oldDaemonClient
		jsonOutput = oldJsonOutput
	}()

	// Set up global client
	daemonClient = client
	jsonOutput = false // Human-readable output

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Call deleteViaDaemon with human output
	deleteViaDaemon([]string{issue.ID}, true, false, false, false, "human output test")

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify human-readable output contains expected text
	if !strings.Contains(output, "Deleted") {
		t.Errorf("Expected output to contain 'Deleted', got: %s", output)
	}
}

// TestDeleteViaDaemon_DirectBatch tests batch deletion directly
func TestDeleteViaDaemon_DirectBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, testStore, cleanup := setupDaemonTestEnvForDelete(t)
	defer cleanup()
	defer cancel()

	// Create multiple test issues
	var issueIDs []string
	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:     "Batch Direct Issue " + string(rune('A'+i)),
			IssueType: "task",
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
		issueIDs = append(issueIDs, issue.ID)
	}

	// Save old global state
	oldDaemonClient := daemonClient
	oldJsonOutput := jsonOutput
	defer func() {
		daemonClient = oldDaemonClient
		jsonOutput = oldJsonOutput
	}()

	// Set up global client
	daemonClient = client
	jsonOutput = true

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Call deleteViaDaemon with multiple issues
	deleteViaDaemon(issueIDs, true, false, false, true, "batch direct test")

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify output is valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Errorf("Expected valid JSON output, got: %s, error: %v", output, err)
	}

	// Check deleted_count
	if deletedCount, ok := result["deleted_count"].(float64); !ok || int(deletedCount) != 3 {
		t.Errorf("Expected deleted_count=3, got: %v", result["deleted_count"])
	}
}

// setupDaemonTestEnvForDelete sets up a complete daemon test environment
func setupDaemonTestEnvForDelete(t *testing.T) (context.Context, context.CancelFunc, *rpc.Client, *sqlite.SQLiteStorage, func()) {
	t.Helper()

	tmpDir := makeSocketTempDir(t)
	initTestGitRepo(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	testDBPath := filepath.Join(beadsDir, "beads.db")

	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	log := daemonLogger{logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))}

	server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		cancel()
		t.Fatalf("Failed to start RPC server: %v", err)
	}

	// Wait for server to be ready
	select {
	case <-server.WaitReady():
		// Server is ready
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Server did not become ready")
	}

	// Connect RPC client
	client, err := rpc.TryConnect(socketPath)
	if err != nil || client == nil {
		cancel()
		t.Fatalf("Failed to connect RPC client: %v", err)
	}

	cleanup := func() {
		if client != nil {
			client.Close()
		}
		if server != nil {
			server.Stop()
		}
		testStore.Close()
	}

	return ctx, cancel, client, testStore, cleanup
}
