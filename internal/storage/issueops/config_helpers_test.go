package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestParseStatusFallback(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []types.CustomStatus
	}{
		{
			name:  "simple names get CategoryUnspecified",
			input: []string{"review", "testing"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryUnspecified},
				{Name: "testing", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "category format parsed correctly",
			input: []string{"review:active", "testing:wip"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "testing", Category: types.CategoryWIP},
			},
		},
		{
			name:  "mixed format",
			input: []string{"review:active", "legacy"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "legacy", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "empty entries filtered",
			input: []string{"", "review", ""},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "empty list",
			input: []string{},
			want:  nil,
		},
		{
			name:  "nil input",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStatusFallback(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statuses, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Category != tt.want[i].Category {
					t.Errorf("status[%d] = {%q, %q}, want {%q, %q}",
						i, g.Name, g.Category, tt.want[i].Name, tt.want[i].Category)
				}
			}
		})
	}
}

func TestParseCommaSeparatedList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty string", "", nil},
		{"single value", "foo", []string{"foo"}},
		{"multiple values", "foo,bar,baz", []string{"foo", "bar", "baz"}},
		{"whitespace trimmed", " foo , bar , baz ", []string{"foo", "bar", "baz"}},
		{"empty entries filtered", "foo,,bar,,", []string{"foo", "bar"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommaSeparatedList(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
