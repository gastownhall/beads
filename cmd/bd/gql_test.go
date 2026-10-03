package main

import "testing"

func TestGQLIsReadOnlyCommand(t *testing.T) {
	if !isReadOnlyCommand("gql") {
		t.Fatal(`isReadOnlyCommand("gql") = false, want true`)
	}
}

func TestGQLCapabilityRowPermitted(t *testing.T) {
	row, ok := LookupCapabilityRow("gql", "")
	if !ok {
		t.Fatal("gql has no capability registry row")
	}
	if row.Rule.Outcome != ProxyOutcomeHonored {
		t.Fatalf("gql outcome = %q, want %q", row.Rule.Outcome, ProxyOutcomeHonored)
	}
}
