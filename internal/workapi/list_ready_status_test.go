package workapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestReadyStatusAndReadyIntersect pins GH#5832: `bd list --status X --ready`
// is the intersection, not the unfiltered ready set. These are builder and
// projection tests so they stay in the CGO_ENABLED=0 lane.
func TestReadyStatusAndReadyIntersect(t *testing.T) {
	t.Run("status X --ready keeps status X", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true, Status: "in_progress"}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.StatusInProgress {
			t.Fatalf("IssueFilter.Status = %v, want %q", filter.Status, types.StatusInProgress)
		}
		wf := ReadyFilterFromIssueFilter(filter)
		if wf.Status != types.StatusInProgress {
			t.Fatalf("WorkFilter.Status = %q, want %q", wf.Status, types.StatusInProgress)
		}
		if len(wf.Statuses) != 0 {
			t.Fatalf("WorkFilter.Statuses = %v, want empty", wf.Statuses)
		}
	})

	t.Run("custom status --ready keeps the custom status", func(t *testing.T) {
		cfg := ListConfig{CustomStatuses: []types.CustomStatus{{Name: "unrefined", Category: types.CategoryWIP}}}
		filter, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true, Status: "unrefined"}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.Status("unrefined") {
			t.Fatalf("IssueFilter.Status = %v, want %q", filter.Status, "unrefined")
		}
		wf := ReadyFilterFromIssueFilter(filter)
		if wf.Status != types.Status("unrefined") {
			t.Fatalf("WorkFilter.Status = %q, want %q", wf.Status, "unrefined")
		}
	})

	t.Run("zero-match status is not rewritten to the open ready set", func(t *testing.T) {
		plain, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter(ready): %v", err)
		}
		if got := ReadyFilterFromIssueFilter(plain).Status; got != types.StatusOpen {
			t.Fatalf("plain --ready WorkFilter.Status = %q, want %q", got, types.StatusOpen)
		}

		filtered, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true, Status: "in_progress"}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter(ready+in_progress): %v", err)
		}
		wf := ReadyFilterFromIssueFilter(filtered)
		if wf.Status != types.StatusInProgress {
			t.Fatalf("WorkFilter.Status = %q, want %q (must not fall back to the unfiltered ready pin)", wf.Status, types.StatusInProgress)
		}
		if wf.Status == ReadyFilterFromIssueFilter(plain).Status {
			t.Fatal("zero-match --status was rewritten to the unfiltered ready status")
		}
	})

	t.Run("plain --ready still pins open", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.StatusOpen {
			t.Fatalf("IssueFilter.Status = %v, want %q", filter.Status, types.StatusOpen)
		}
		if got := ReadyFilterFromIssueFilter(filter).Status; got != types.StatusOpen {
			t.Fatalf("WorkFilter.Status = %q, want %q", got, types.StatusOpen)
		}
	})

	t.Run("status all --ready keeps the open default", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true, Status: "all"}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.StatusOpen {
			t.Fatalf("IssueFilter.Status = %v, want %q", filter.Status, types.StatusOpen)
		}
	})

	t.Run("multi-status --ready copies the OR set", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true, Status: "open,in_progress"}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		want := []types.Status{types.StatusOpen, types.StatusInProgress}
		if !reflect.DeepEqual(filter.Statuses, want) {
			t.Fatalf("IssueFilter.Statuses = %v, want %v", filter.Statuses, want)
		}
		if filter.Status != nil {
			t.Fatalf("IssueFilter.Status = %v, want nil when Statuses is set", filter.Status)
		}
		wf := ReadyFilterFromIssueFilter(filter)
		if wf.Status != "" {
			t.Fatalf("WorkFilter.Status = %q, want empty so Statuses is not ignored", wf.Status)
		}
		if !reflect.DeepEqual(wf.Statuses, want) {
			t.Fatalf("WorkFilter.Statuses = %v, want %v", wf.Statuses, want)
		}
	})

	t.Run("invalid status --ready errors instead of dropping --status", func(t *testing.T) {
		_, err := BuildListFilter(issueops.ListRequest{ReadyFlag: true, Status: "not-a-status"}, ListConfig{})
		if err == nil {
			t.Fatal("BuildListFilter unexpectedly succeeded")
		}
		if !strings.Contains(err.Error(), "invalid status") || !strings.Contains(err.Error(), "not-a-status") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
