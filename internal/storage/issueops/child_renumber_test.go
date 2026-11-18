package issueops

import (
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestAssignChildRenumbers pins the bd-28sb25 allocator: only minimal roots
// are numbered (above the given floor, in parent/suffix order whatever the
// input order), each carries its local subtree on an exact id boundary, and a
// destination that is taken or too long refuses the whole plan.
func TestAssignChildRenumbers(t *testing.T) {
	r := func(from, to string) storage.ChildRenumber { return storage.ChildRenumber{From: from, To: to} }
	cases := []struct {
		name       string
		collisions []string
		local      []string
		floors     map[string]int
		taken      []string
		want       []storage.ChildRenumber
		wantErr    string
	}{
		{
			name:       "report_shape",
			collisions: []string{"p.72", "p.71"},
			local:      []string{"p", "p.71", "p.72", "p.73"},
			floors:     map[string]int{"p": 73},
			want:       []storage.ChildRenumber{r("p.71", "p.74"), r("p.72", "p.75")},
		},
		{
			name:       "numeric_not_lexical_order",
			collisions: []string{"p.10", "p.9"},
			local:      []string{"p.9", "p.10"},
			floors:     map[string]int{"p": 12},
			want:       []storage.ChildRenumber{r("p.9", "p.13"), r("p.10", "p.14")},
		},
		{
			name:       "subtree_moves_with_root_on_exact_boundary",
			collisions: []string{"p.71", "p.71.1"},
			local:      []string{"p.71", "p.71.1", "p.71.1.2", "p.710", "p.710.1"},
			floors:     map[string]int{"p": 72},
			want: []storage.ChildRenumber{
				r("p.71", "p.73"), r("p.71.1", "p.73.1"), r("p.71.1.2", "p.73.1.2"),
			},
		},
		{
			name:       "multiple_parents_number_independently",
			collisions: []string{"q.3", "p.2"},
			local:      []string{"p.2", "q.3"},
			floors:     map[string]int{"p": 5, "q": 9},
			want:       []storage.ChildRenumber{r("p.2", "p.6"), r("q.3", "q.10")},
		},
		{
			name:       "taken_destination_refuses",
			collisions: []string{"p.1"},
			local:      []string{"p.1"},
			floors:     map[string]int{"p": 1},
			taken:      []string{"p.2"},
			wantErr:    "already used",
		},
		{
			name:       "missing_floor_refuses",
			collisions: []string{"p.1"},
			local:      []string{"p.1"},
			floors:     map[string]int{},
			wantErr:    "no high-water mark",
		},
		{
			name:       "suffix_overflow_refuses",
			collisions: []string{"p.1"},
			local:      []string{"p.1"},
			floors:     map[string]int{"p": 2147483647},
			wantErr:    "no child suffix left",
		},
		{
			name:       "length_limit_refuses",
			collisions: []string{strings.Repeat("x", 253) + ".1"},
			local:      []string{strings.Repeat("x", 253) + ".1"},
			floors:     map[string]int{strings.Repeat("x", 253): 9},
			wantErr:    "character id limit",
		},
		{
			name:       "non_hierarchical_refuses",
			collisions: []string{"p"},
			floors:     map[string]int{},
			wantErr:    "not a hierarchical child id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			taken := map[string]bool{}
			for _, id := range tc.local {
				taken[id] = true
			}
			for _, id := range tc.taken {
				taken[id] = true
			}
			got, err := AssignChildRenumbers(tc.collisions, tc.local, tc.floors, taken)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("AssignChildRenumbers: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("renames = %v, want %v", got, tc.want)
			}
		})
	}
}
