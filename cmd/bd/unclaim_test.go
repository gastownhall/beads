//go:build cgo

package main

import (
	"testing"
)

func TestUnclaimCommand_Structure(t *testing.T) {
	// Test that the unclaim command is properly registered
	if unclaimCmd == nil {
		t.Fatal("unclaimCmd should not be nil")
	}

	// Test command properties
	if unclaimCmd.Use != "unclaim [id...]" {
		t.Errorf("expected Use to be 'unclaim [id...]', got %q", unclaimCmd.Use)
	}

	if unclaimCmd.GroupID != "issues" {
		t.Errorf("expected GroupID to be 'issues', got %q", unclaimCmd.GroupID)
	}

	if unclaimCmd.Short != "Release a claimed issue" {
		t.Errorf("expected Short to be 'Release a claimed issue', got %q", unclaimCmd.Short)
	}

	// Test that command requires at least one argument
	if unclaimCmd.Args == nil {
		t.Fatal("Args should not be nil")
	}
}

func TestUnclaimCommand_Flags(t *testing.T) {
	// Test that the reason flag is properly defined
	reasonFlag := unclaimCmd.Flags().Lookup("reason")
	if reasonFlag == nil {
		t.Fatal("reason flag should be defined")
	}

	if reasonFlag.Shorthand != "r" {
		t.Errorf("expected shorthand 'r', got %q", reasonFlag.Shorthand)
	}

	if reasonFlag.DefValue != "" {
		t.Errorf("expected default value '', got %q", reasonFlag.DefValue)
	}

	// The --force flag bypasses the claim-ownership check (admin/reaper use).
	forceFlag := unclaimCmd.Flags().Lookup("force")
	if forceFlag == nil {
		t.Fatal("force flag should be defined")
	}
	if forceFlag.DefValue != "false" {
		t.Errorf("expected force default value 'false', got %q", forceFlag.DefValue)
	}

	// The --if-assignee flag makes the release an atomic compare-and-swap.
	ifAssigneeFlag := unclaimCmd.Flags().Lookup("if-assignee")
	if ifAssigneeFlag == nil {
		t.Fatal("if-assignee flag should be defined")
	}
	if ifAssigneeFlag.DefValue != "" {
		t.Errorf("expected if-assignee default value '', got %q", ifAssigneeFlag.DefValue)
	}
}
