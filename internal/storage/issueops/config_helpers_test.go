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

// TestMergeWithYAMLCustomTypes pins the gastownhall/beads#4024 overlay
// semantics: project-extension types declared in .beads/config.yaml must
// union-add to whatever the database side already declared, with duplicates
// removed and order preserved (DB types first, then YAML-only types).
func TestMergeWithYAMLCustomTypes(t *testing.T) {
	tests := []struct {
		name      string
		dbTypes   []string
		yamlTypes []string
		want      []string
	}{
		{
			name:      "both empty returns nil",
			dbTypes:   nil,
			yamlTypes: nil,
			want:      nil,
		},
		{
			name:      "yaml only is returned alone",
			dbTypes:   nil,
			yamlTypes: []string{"step", "wisp"},
			want:      []string{"step", "wisp"},
		},
		{
			name:      "db only is returned alone (no yaml overlay)",
			dbTypes:   []string{"convoy", "gate"},
			yamlTypes: nil,
			want:      []string{"convoy", "gate"},
		},
		{
			name:      "db plus disjoint yaml is union-added",
			dbTypes:   []string{"convoy", "gate"},
			yamlTypes: []string{"step", "wisp"},
			want:      []string{"convoy", "gate", "step", "wisp"},
		},
		{
			name:      "yaml duplicate of db is deduped (db wins position)",
			dbTypes:   []string{"convoy", "gate"},
			yamlTypes: []string{"gate", "step"},
			want:      []string{"convoy", "gate", "step"},
		},
		{
			name:      "yaml internal duplicate deduped when db empty",
			dbTypes:   nil,
			yamlTypes: []string{"step", "step", "wisp"},
			want:      []string{"step", "wisp"},
		},
		{
			name:      "whitespace entries dropped",
			dbTypes:   []string{"convoy", "  ", "gate"},
			yamlTypes: []string{"step", "", " wisp "},
			want:      []string{"convoy", "gate", "step", "wisp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeWithYAMLCustomTypes(tt.dbTypes, func() []string { return tt.yamlTypes })
			if len(got) != len(tt.want) {
				t.Fatalf("got %v (len %d), want %v (len %d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestMergeWithYAMLCustomTypes_NilGetter verifies the nil-getter guard:
// callers that pass a nil yamlGetter (e.g. when YAML is intentionally
// disabled) must get the dbTypes unchanged.
func TestMergeWithYAMLCustomTypes_NilGetter(t *testing.T) {
	got := mergeWithYAMLCustomTypes([]string{"convoy"}, nil)
	if len(got) != 1 || got[0] != "convoy" {
		t.Fatalf("got %v, want [convoy]", got)
	}
}

// TestCustomTypesYAMLFallback pins the YAML-fallback decision in
// ResolveCustomTypesInTx. When the in-tx config-string read errors, we mirror
// ResolveCustomStatusesDetailedInTx: degraded DB + populated YAML must return
// the YAML overlay with a nil error so callers (NewBatchContext,
// UpdateIssueInTx) don't treat the YAML-fallback path as a fatal validation
// failure. When YAML supplies nothing, the original error propagates.
func TestCustomTypesYAMLFallback(t *testing.T) {
	sentinelErr := errSentinel("boom")

	tests := []struct {
		name      string
		yamlTypes []string
		wantTypes []string
		wantErr   bool
	}{
		{
			name:      "yaml supplies types -> return yaml with nil error",
			yamlTypes: []string{"step", "wisp"},
			wantTypes: []string{"step", "wisp"},
			wantErr:   false,
		},
		{
			name:      "yaml empty -> propagate db error",
			yamlTypes: nil,
			wantTypes: nil,
			wantErr:   true,
		},
		{
			name:      "yaml all-whitespace -> propagate db error",
			yamlTypes: []string{"  ", ""},
			wantTypes: nil,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := customTypesYAMLFallback(func() []string { return tt.yamlTypes }, sentinelErr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got nil error, want %v", sentinelErr)
				}
			} else {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
			}
			if len(got) != len(tt.wantTypes) {
				t.Fatalf("got %v, want %v", got, tt.wantTypes)
			}
			for i := range got {
				if got[i] != tt.wantTypes[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.wantTypes[i])
				}
			}
		})
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
