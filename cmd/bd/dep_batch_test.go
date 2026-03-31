package main

import (
	"testing"
)

func TestDepListAcceptsMultipleArgs(t *testing.T) {
	// Verify the cobra command accepts multiple args (MinimumNArgs(1)).
	cmd := depListCmd
	if err := cmd.Args(cmd, []string{"id1", "id2", "id3"}); err != nil {
		t.Fatalf("depListCmd should accept multiple args: %v", err)
	}
}

func TestDepListRequiresAtLeastOneArg(t *testing.T) {
	cmd := depListCmd
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("depListCmd should reject zero args")
	}
}

func TestDepListAcceptsSingleArg(t *testing.T) {
	cmd := depListCmd
	if err := cmd.Args(cmd, []string{"id1"}); err != nil {
		t.Fatalf("depListCmd should accept single arg: %v", err)
	}
}
