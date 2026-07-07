package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildCreateIssueExternalRefPrelink(t *testing.T) {
	ref := "https://linear.app/team/issue/TEAM-123/fix-login"

	issue := buildCreateIssue(createIssueParams{
		Title:       "Pre-linked Linear issue",
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: ref,
	})

	if issue.ExternalRef == nil {
		t.Fatal("ExternalRef is nil, want pre-linked Linear URL")
	}
	if *issue.ExternalRef != ref {
		t.Fatalf("ExternalRef = %q, want %q", *issue.ExternalRef, ref)
	}
}

func TestBuildCreateIssueEmptyExternalRefIsNil(t *testing.T) {
	issue := buildCreateIssue(createIssueParams{
		Title:     "Local-only issue",
		Priority:  2,
		IssueType: types.TypeTask,
	})

	if issue.ExternalRef != nil {
		t.Fatalf("ExternalRef = %v, want nil", issue.ExternalRef)
	}
}
