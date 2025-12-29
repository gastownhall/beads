package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

func TestFindOrphanedIssues_ConvertsDoctorOutput(t *testing.T) {
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(path string) ([]doctor.OrphanIssue, error) {
		if path != "/tmp/repo" {
			t.Fatalf("unexpected path %q", path)
		}
		return []doctor.OrphanIssue{{
			IssueID:             "bd-123",
			Title:               "Fix login",
			Status:              "open",
			LatestCommit:        "abc123",
			LatestCommitMessage: "(bd-123) implement fix",
		}}, nil
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	result, err := findOrphanedIssues("/tmp/repo")
	if err != nil {
		t.Fatalf("findOrphanedIssues returned error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(result))
	}
	orphan := result[0]
	if orphan.IssueID != "bd-123" || orphan.Title != "Fix login" || orphan.Status != "open" {
		t.Fatalf("unexpected orphan output: %#v", orphan)
	}
	if orphan.LatestCommit != "abc123" || !strings.Contains(orphan.LatestCommitMessage, "implement") {
		t.Fatalf("commit metadata not preserved: %#v", orphan)
	}
}

func TestFindOrphanedIssues_ErrorWrapped(t *testing.T) {
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(string) ([]doctor.OrphanIssue, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	_, err := findOrphanedIssues("/tmp/repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unable to find orphaned issues") {
		t.Fatalf("expected wrapped error message, got %v", err)
	}
}

func TestCloseIssue_UsesRunner(t *testing.T) {
	orig := closeIssueRunner
	defer func() { closeIssueRunner = orig }()

	called := false
	closeIssueRunner = func(issueID string) error {
		called = true
		if issueID != "bd-999" {
			t.Fatalf("unexpected issue id %q", issueID)
		}
		return nil
	}

	if err := closeIssue("bd-999"); err != nil {
		t.Fatalf("closeIssue returned error: %v", err)
	}
	if !called {
		t.Fatal("closeIssueRunner was not invoked")
	}
}

func TestCloseIssue_PropagatesError(t *testing.T) {
	orig := closeIssueRunner
	closeIssueRunner = func(string) error { return errors.New("nope") }
	t.Cleanup(func() { closeIssueRunner = orig })

	err := closeIssue("bd-1")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected delegated error, got %v", err)
	}
}
