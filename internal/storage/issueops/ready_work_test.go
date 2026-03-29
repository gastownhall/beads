package issueops

import (
	"testing"
)

func TestBuildSQLInClause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		ids              []string
		wantPlaceholders string
		wantArgs         []interface{}
	}{
		{
			name:             "single ID",
			ids:              []string{"42"},
			wantPlaceholders: "?",
			wantArgs:         []interface{}{"42"},
		},
		{
			name:             "multiple IDs",
			ids:              []string{"1", "2", "3"},
			wantPlaceholders: "?,?,?",
			wantArgs:         []interface{}{"1", "2", "3"},
		},
		{
			name:             "empty slice",
			ids:              []string{},
			wantPlaceholders: "",
			wantArgs:         []interface{}{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotPlaceholders, gotArgs := buildSQLInClause(tt.ids)

			if gotPlaceholders != tt.wantPlaceholders {
				t.Errorf("placeholders = %q, want %q", gotPlaceholders, tt.wantPlaceholders)
			}

			if len(gotArgs) != len(tt.wantArgs) {
				t.Fatalf("args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}

			for i := range gotArgs {
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}
