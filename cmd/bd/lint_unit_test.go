package main

import (
	"errors"
	"testing"

	"github.com/jonbaldie/beads/internal/types"
)

func TestRunLintWarningsDoNotFailByDefault(t *testing.T) {
	t.Parallel()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	issues := []*types.Issue{{
		ID:          "bd-feat",
		Title:       "Add dark mode",
		Description: "Ship the toggle",
		IssueType:   types.TypeFeature,
		Status:      types.StatusOpen,
	}}

	if err := runLint(issues, false); err != nil {
		t.Fatalf("runLint without --strict should exit 0 on warnings, got %v", err)
	}
}

func TestRunLintStrictFailsOnWarnings(t *testing.T) {
	t.Parallel()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	issues := []*types.Issue{{
		ID:          "bd-feat",
		Title:       "Add dark mode",
		Description: "Ship the toggle",
		IssueType:   types.TypeFeature,
		Status:      types.StatusOpen,
	}}

	err := runLint(issues, true)
	if err == nil {
		t.Fatal("runLint --strict should fail when warnings exist")
	}
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("runLint --strict error = %v, want SilentExit code 1", err)
	}
}

func TestRunLintCleanIsSuccess(t *testing.T) {
	t.Parallel()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	issues := []*types.Issue{{
		ID:          "bd-chore",
		Title:       "Bump deps",
		Description: "Routine",
		IssueType:   types.TypeChore,
		Status:      types.StatusOpen,
	}}

	if err := runLint(issues, true); err != nil {
		t.Fatalf("clean lint --strict should succeed, got %v", err)
	}
}
