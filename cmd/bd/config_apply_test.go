package main

import (
	"os"
	"testing"
)

func TestApplyHooksNoDrift(t *testing.T) {
	result := applyHooks(false, false)
	if result.Status != applyStatusOK {
		t.Errorf("expected status %q, got %q", applyStatusOK, result.Status)
	}
	if result.Action != "none" {
		t.Errorf("expected action %q, got %q", "none", result.Action)
	}
}

func TestApplyHooksDryRun(t *testing.T) {
	result := applyHooks(true, true)
	if result.Status != applyStatusDryRun {
		t.Errorf("expected status %q, got %q", applyStatusDryRun, result.Status)
	}
	if result.Action != "reinstall" {
		t.Errorf("expected action %q, got %q", "reinstall", result.Action)
	}
}

func TestApplyRemoteNoDrift(t *testing.T) {
	result := applyRemote(false, false)
	if result.Status != applyStatusOK {
		t.Errorf("expected status %q, got %q", applyStatusOK, result.Status)
	}
	if result.Action != "none" {
		t.Errorf("expected action %q, got %q", "none", result.Action)
	}
}

func TestApplyRemoteDryRun(t *testing.T) {
	// When drifted but no beads dir, should skip
	result := applyRemote(true, true)
	if result.Status != applyStatusSkipped && result.Status != applyStatusDryRun {
		t.Errorf("expected status %q or %q, got %q", applyStatusSkipped, applyStatusDryRun, result.Status)
	}
}

func TestApplyServerNoDrift(t *testing.T) {
	result := applyServer(false, false)
	if result.Status != applyStatusOK {
		t.Errorf("expected status %q, got %q", applyStatusOK, result.Status)
	}
	if result.Action != "none" {
		t.Errorf("expected action %q, got %q", "none", result.Action)
	}
}

func TestApplyServerDriftedButNotConfigured(t *testing.T) {
	// Server running but config doesn't say shared-server=true
	// Should skip (not stop the server)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	result := applyServer(true, false)
	// Without a .beads dir or with shared-server not set, should skip
	if result.Status != applyStatusSkipped {
		t.Errorf("expected status %q, got %q", applyStatusSkipped, result.Status)
	}
}

func TestApplyServerDryRun(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	result := applyServer(true, true)
	// Without beads dir, should skip even in dry-run
	if result.Status != applyStatusSkipped {
		t.Errorf("expected status %q, got %q", applyStatusSkipped, result.Status)
	}
}

func TestRunApplyAllOK(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	// In a test environment with no drift, all results should be ok or skipped
	results := runApply(false)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status == applyStatusError {
			t.Errorf("unexpected error for check %q: %s", r.Check, r.Error)
		}
	}
}

func TestRunApplyDryRun(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	results := runApply(true)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	// In dry-run, no actions should be "applied"
	for _, r := range results {
		if r.Status == applyStatusApplied {
			t.Errorf("dry-run should not apply actions, but check %q was applied", r.Check)
		}
	}
}

func TestPrintApplyResults(t *testing.T) {
	// Smoke test — just ensure no panic
	results := []ApplyResult{
		{Check: "hooks", Action: "none", Status: applyStatusOK, Message: "up to date"},
		{Check: "remote", Action: "add_remote", Status: applyStatusApplied, Message: "added"},
		{Check: "server", Action: "start", Status: applyStatusError, Message: "failed", Error: "no dolt"},
		{Check: "hooks", Action: "reinstall", Status: applyStatusDryRun, Message: "would reinstall"},
		{Check: "remote", Action: "none", Status: applyStatusSkipped, Message: "skipped"},
	}
	// Redirect stdout to avoid test noise
	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = old }()
	printApplyResults(results)
	printApplyResults(nil)
}
