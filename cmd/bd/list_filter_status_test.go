package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestApplyStatusFilter(t *testing.T) {
	t.Run("single built-in trims spaces", func(t *testing.T) {
		var filter types.IssueFilter
		if err := applyStatusFilter(&filter, " open ", nil); err != nil {
			t.Fatalf("applyStatusFilter: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.StatusOpen {
			t.Fatalf("Status = %v, want %q", filter.Status, types.StatusOpen)
		}
	})

	t.Run("multiple statuses use OR filter", func(t *testing.T) {
		var filter types.IssueFilter
		if err := applyStatusFilter(&filter, "open, closed", nil); err != nil {
			t.Fatalf("applyStatusFilter: %v", err)
		}
		want := []types.Status{types.StatusOpen, types.StatusClosed}
		if !reflect.DeepEqual(filter.Statuses, want) {
			t.Fatalf("Statuses = %v, want %v", filter.Statuses, want)
		}
	})

	t.Run("configured custom status", func(t *testing.T) {
		var filter types.IssueFilter
		if err := applyStatusFilter(&filter, "in_review", []string{"in_review"}); err != nil {
			t.Fatalf("applyStatusFilter: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.Status("in_review") {
			t.Fatalf("Status = %v, want %q", filter.Status, "in_review")
		}
	})

	t.Run("invalid status errors", func(t *testing.T) {
		var filter types.IssueFilter
		err := applyStatusFilter(&filter, "open,not-a-status", nil)
		if err == nil {
			t.Fatal("applyStatusFilter unexpectedly succeeded")
		}
		if !strings.Contains(err.Error(), "invalid status") || !strings.Contains(err.Error(), "not-a-status") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
