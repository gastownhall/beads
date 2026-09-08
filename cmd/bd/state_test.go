//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

type stateTestHelper struct {
	s   *dolt.DoltStore
	ctx context.Context
	t   *testing.T
}

func (h *stateTestHelper) createIssue(title string, issueType types.IssueType, priority int) *types.Issue {
	issue := &types.Issue{
		Title:     title,
		Priority:  priority,
		IssueType: issueType,
		Status:    types.StatusOpen,
	}
	if err := h.s.CreateIssue(h.ctx, issue, "test-user"); err != nil {
		h.t.Fatalf("Failed to create issue: %v", err)
	}
	return issue
}

func (h *stateTestHelper) addLabel(issueID, label string) {
	if err := h.s.AddLabel(h.ctx, issueID, label, "test-user"); err != nil {
		h.t.Fatalf("Failed to add label '%s': %v", label, err)
	}
}

func (h *stateTestHelper) removeLabel(issueID, label string) {
	if err := h.s.RemoveLabel(h.ctx, issueID, label, "test-user"); err != nil {
		h.t.Fatalf("Failed to remove label '%s': %v", label, err)
	}
}

func (h *stateTestHelper) getLabels(issueID string) []string {
	labels, err := h.s.GetLabels(h.ctx, issueID)
	if err != nil {
		h.t.Fatalf("Failed to get labels: %v", err)
	}
	return labels
}

// getStateValue extracts the value for a dimension from labels
func (h *stateTestHelper) getStateValue(issueID, dimension string) string {
	labels := h.getLabels(issueID)
	prefix := dimension + ":"
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix)
		}
	}
	return ""
}

// getStates extracts all dimension:value labels as a map
func (h *stateTestHelper) getStates(issueID string) map[string]string {
	labels := h.getLabels(issueID)
	states := make(map[string]string)
	for _, label := range labels {
		if idx := strings.Index(label, ":"); idx > 0 {
			dimension := label[:idx]
			value := label[idx+1:]
			states[dimension] = value
		}
	}
	return states
}

func (h *stateTestHelper) assertStateValue(issueID, dimension, expected string) {
	actual := h.getStateValue(issueID, dimension)
	if actual != expected {
		h.t.Errorf("Expected %s=%s, got %s=%s", dimension, expected, dimension, actual)
	}
}

func (h *stateTestHelper) assertNoState(issueID, dimension string) {
	value := h.getStateValue(issueID, dimension)
	if value != "" {
		h.t.Errorf("Expected no %s state, got %s", dimension, value)
	}
}

func (h *stateTestHelper) assertStateCount(issueID string, expected int) {
	states := h.getStates(issueID)
	if len(states) != expected {
		h.t.Errorf("Expected %d states, got %d: %v", expected, len(states), states)
	}
}

func TestStateQueries(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-state-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &stateTestHelper{s: s, ctx: ctx, t: t}

	t.Run("query state from label", func(t *testing.T) {
		issue := h.createIssue("Role Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "patrol:active")
		h.assertStateValue(issue.ID, "patrol", "active")
	})

	t.Run("query multiple states", func(t *testing.T) {
		issue := h.createIssue("Multi State Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "patrol:active")
		h.addLabel(issue.ID, "mode:normal")
		h.addLabel(issue.ID, "health:healthy")
		h.assertStateValue(issue.ID, "patrol", "active")
		h.assertStateValue(issue.ID, "mode", "normal")
		h.assertStateValue(issue.ID, "health", "healthy")
		h.assertStateCount(issue.ID, 3)
	})

	t.Run("query missing state returns empty", func(t *testing.T) {
		issue := h.createIssue("No State Test", types.TypeTask, 1)
		h.assertNoState(issue.ID, "patrol")
	})

	t.Run("state labels mixed with regular labels", func(t *testing.T) {
		issue := h.createIssue("Mixed Labels Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "patrol:active")
		h.addLabel(issue.ID, "backend") // Not a state label
		h.addLabel(issue.ID, "mode:normal")
		h.addLabel(issue.ID, "urgent") // Not a state label
		h.assertStateValue(issue.ID, "patrol", "active")
		h.assertStateValue(issue.ID, "mode", "normal")
		h.assertStateCount(issue.ID, 2)
	})

	t.Run("state with colon in value", func(t *testing.T) {
		issue := h.createIssue("Colon Value Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "error:code:500")
		value := h.getStateValue(issue.ID, "error")
		if value != "code:500" {
			t.Errorf("Expected 'code:500', got '%s'", value)
		}
	})
}

func TestStateTransitions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-state-transition-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &stateTestHelper{s: s, ctx: ctx, t: t}

	t.Run("change state value", func(t *testing.T) {
		issue := h.createIssue("Transition Test", types.TypeTask, 1)

		// Initial state
		h.addLabel(issue.ID, "patrol:active")
		h.assertStateValue(issue.ID, "patrol", "active")

		// Transition to muted (remove old, add new)
		h.removeLabel(issue.ID, "patrol:active")
		h.addLabel(issue.ID, "patrol:muted")
		h.assertStateValue(issue.ID, "patrol", "muted")
	})

	t.Run("prevent duplicate dimension values", func(t *testing.T) {
		issue := h.createIssue("Duplicate Prevention Test", types.TypeTask, 1)

		// Add initial state
		h.addLabel(issue.ID, "patrol:active")

		// If we add another value without removing, we'd have both
		// This is what the set-state command prevents
		h.addLabel(issue.ID, "patrol:muted")

		// Now we have both - this is the anti-pattern
		labels := h.getLabels(issue.ID)
		count := 0
		for _, l := range labels {
			if strings.HasPrefix(l, "patrol:") {
				count++
			}
		}
		if count != 2 {
			t.Errorf("Expected 2 patrol labels (anti-pattern), got %d", count)
		}

		// The getStateValue only returns the first one found
		// This demonstrates why proper transitions (remove then add) are needed
	})
}

func TestStatePatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-state-patterns-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &stateTestHelper{s: s, ctx: ctx, t: t}

	t.Run("common operational dimensions", func(t *testing.T) {
		issue := h.createIssue("Operations Role", types.TypeTask, 1)

		// Set up typical operational state
		h.addLabel(issue.ID, "patrol:active")
		h.addLabel(issue.ID, "mode:normal")
		h.addLabel(issue.ID, "health:healthy")
		h.addLabel(issue.ID, "sync:current")

		states := h.getStates(issue.ID)
		expected := map[string]string{
			"patrol": "active",
			"mode":   "normal",
			"health": "healthy",
			"sync":   "current",
		}

		for dim, val := range expected {
			if states[dim] != val {
				t.Errorf("Expected %s=%s, got %s=%s", dim, val, dim, states[dim])
			}
		}
	})

	t.Run("degraded mode example", func(t *testing.T) {
		issue := h.createIssue("Degraded Role", types.TypeTask, 1)

		// Start healthy
		h.addLabel(issue.ID, "health:healthy")
		h.addLabel(issue.ID, "mode:normal")

		// Degrade
		h.removeLabel(issue.ID, "mode:normal")
		h.addLabel(issue.ID, "mode:degraded")

		h.assertStateValue(issue.ID, "mode", "degraded")
		h.assertStateValue(issue.ID, "health", "healthy") // Health unchanged
	})
}
