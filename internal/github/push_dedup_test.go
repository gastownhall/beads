package github

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPushFieldsEqual is the regression test for gastownhall/beads#4214:
// without a content comparator, GitHub push re-PATCHed every issue on every
// run. PushFieldsEqual must report "no change" when the pushable fields
// (title, body, state, label set) already match GitHub, and "changed"
// otherwise, so the engine can skip redundant updates.
func TestPushFieldsEqual(t *testing.T) {
	config := DefaultMappingConfig()

	ghLabels := func(names ...string) []Label {
		ls := make([]Label, 0, len(names))
		for _, n := range names {
			ls = append(ls, Label{Name: n})
		}
		return ls
	}

	base := &types.Issue{
		Title:       "Fix the thing",
		Description: "Some body text",
		IssueType:   types.IssueType("task"),
		Priority:    2, // -> priority::medium
		Status:      types.StatusOpen,
	}

	tests := []struct {
		name   string
		local  *types.Issue
		remote *Issue
		want   bool
	}{
		{
			name:  "identical, remote labels reordered",
			local: base,
			// GitHub does not preserve label order across a round-trip.
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "open",
				Labels: ghLabels("priority::medium", "type::task")},
			want: true,
		},
		{name: "nil local", local: nil, remote: &Issue{}, want: false},
		{name: "nil remote", local: base, remote: nil, want: false},
		{
			name:  "title differs",
			local: base,
			remote: &Issue{Title: "Different", Body: "Some body text", State: "open",
				Labels: ghLabels("type::task", "priority::medium")},
			want: false,
		},
		{
			name:  "body differs",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Changed", State: "open",
				Labels: ghLabels("type::task", "priority::medium")},
			want: false,
		},
		{
			name:  "state differs",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "closed",
				Labels: ghLabels("type::task", "priority::medium")},
			want: false,
		},
		{
			name:  "extra label on remote",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "open",
				Labels: ghLabels("type::task", "priority::medium", "extra")},
			want: false,
		},
		{
			name:  "priority label differs",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "open",
				Labels: ghLabels("type::task", "priority::high")},
			want: false,
		},
		{
			name: "in_progress adds status label",
			local: &types.Issue{Title: "T", Description: "B", IssueType: "task",
				Priority: 2, Status: types.StatusInProgress},
			remote: &Issue{Title: "T", Body: "B", State: "open",
				Labels: ghLabels("type::task", "priority::medium", "status::in_progress")},
			want: true,
		},
		{
			name: "closed maps to state closed",
			local: &types.Issue{Title: "T", Description: "B", IssueType: "task",
				Priority: 2, Status: types.StatusClosed},
			remote: &Issue{Title: "T", Body: "B", State: "closed",
				Labels: ghLabels("type::task", "priority::medium")},
			want: true,
		},
		{
			name: "non-scoped local labels preserved in comparison",
			local: &types.Issue{Title: "T", Description: "B", IssueType: "task",
				Priority: 2, Status: types.StatusOpen, Labels: []string{"backend"}},
			remote: &Issue{Title: "T", Body: "B", State: "open",
				Labels: ghLabels("type::task", "priority::medium", "backend")},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PushFieldsEqual(tt.local, tt.remote, config); got != tt.want {
				t.Errorf("PushFieldsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}
