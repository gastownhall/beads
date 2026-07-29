package main

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildCreateIssuePreservesLabels(t *testing.T) {
	labels := []string{"gc:wisp", "status:pending"}
	issue := buildCreateIssue(createIssueParams{
		Title:     "labelled create",
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    labels,
	})

	if !reflect.DeepEqual(issue.Labels, labels) {
		t.Fatalf("labels = %v, want %v", issue.Labels, labels)
	}

	labels[0] = "mutated"
	if issue.Labels[0] != "gc:wisp" {
		t.Fatalf("issue labels alias input slice: got %v", issue.Labels)
	}
}

func TestMergeCreateLabelsKeepsUserOrderAndDedupesInherited(t *testing.T) {
	got := mergeCreateLabels(
		[]string{"gc:wisp", "status:pending", "gc:wisp"},
		[]string{"status:pending", "owner:agent"},
	)
	want := []string{"gc:wisp", "status:pending", "owner:agent"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged labels = %v, want %v", got, want)
	}
}
