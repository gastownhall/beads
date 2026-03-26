package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestNullString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		isNil  bool
		expect string
	}{
		{name: "empty string returns nil", input: "", isNil: true},
		{name: "non-empty string returns value", input: "hello", isNil: false, expect: "hello"},
		{name: "whitespace is not empty", input: " ", isNil: false, expect: " "},
		{name: "tab is not empty", input: "\t", isNil: false, expect: "\t"},
		{name: "newline is not empty", input: "\n", isNil: false, expect: "\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NullString(tc.input)
			if tc.isNil {
				if got != nil {
					t.Errorf("NullString(%q) = %v, want nil", tc.input, got)
				}
			} else {
				if got == nil {
					t.Fatalf("NullString(%q) = nil, want %q", tc.input, tc.expect)
				}
				if got.(string) != tc.expect {
					t.Errorf("NullString(%q) = %q, want %q", tc.input, got, tc.expect)
				}
			}
		})
	}
}

func TestNullStringPtr(t *testing.T) {
	t.Parallel()

	strVal := "hello"
	emptyStr := ""

	tests := []struct {
		name   string
		input  *string
		isNil  bool
		expect string
	}{
		{name: "nil pointer returns nil", input: nil, isNil: true},
		{name: "pointer to non-empty string returns value", input: &strVal, isNil: false, expect: "hello"},
		{name: "pointer to empty string returns empty string", input: &emptyStr, isNil: false, expect: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NullStringPtr(tc.input)
			if tc.isNil {
				if got != nil {
					t.Errorf("NullStringPtr() = %v, want nil", got)
				}
			} else {
				if got == nil {
					t.Fatalf("NullStringPtr() = nil, want %q", tc.expect)
				}
				if got.(string) != tc.expect {
					t.Errorf("NullStringPtr() = %q, want %q", got, tc.expect)
				}
			}
		})
	}
}

func TestNullInt(t *testing.T) {
	t.Parallel()

	zero := 0
	positive := 42
	negative := -1

	tests := []struct {
		name   string
		input  *int
		isNil  bool
		expect int
	}{
		{name: "nil pointer returns nil", input: nil, isNil: true},
		{name: "pointer to zero returns zero", input: &zero, isNil: false, expect: 0},
		{name: "pointer to positive returns value", input: &positive, isNil: false, expect: 42},
		{name: "pointer to negative returns value", input: &negative, isNil: false, expect: -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NullInt(tc.input)
			if tc.isNil {
				if got != nil {
					t.Errorf("NullInt() = %v, want nil", got)
				}
			} else {
				if got == nil {
					t.Fatalf("NullInt() = nil, want %d", tc.expect)
				}
				if got.(int) != tc.expect {
					t.Errorf("NullInt() = %d, want %d", got, tc.expect)
				}
			}
		})
	}
}

func TestNullIntVal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  int
		isNil  bool
		expect int
	}{
		{name: "zero returns nil", input: 0, isNil: true},
		{name: "positive returns value", input: 42, isNil: false, expect: 42},
		{name: "negative returns value", input: -1, isNil: false, expect: -1},
		{name: "one returns value", input: 1, isNil: false, expect: 1},
		{name: "large value returns value", input: 999999, isNil: false, expect: 999999},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NullIntVal(tc.input)
			if tc.isNil {
				if got != nil {
					t.Errorf("NullIntVal(%d) = %v, want nil", tc.input, got)
				}
			} else {
				if got == nil {
					t.Fatalf("NullIntVal(%d) = nil, want %d", tc.input, tc.expect)
				}
				if got.(int) != tc.expect {
					t.Errorf("NullIntVal(%d) = %d, want %d", tc.input, got, tc.expect)
				}
			}
		})
	}
}

func TestJSONMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []byte
		expect string
	}{
		{name: "nil returns empty object", input: nil, expect: "{}"},
		{name: "empty slice returns empty object", input: []byte{}, expect: "{}"},
		{name: "valid json object returned as-is", input: []byte(`{"key":"value"}`), expect: `{"key":"value"}`},
		{name: "valid empty json object", input: []byte(`{}`), expect: `{}`},
		{name: "valid json array", input: []byte(`[1,2,3]`), expect: `[1,2,3]`},
		{name: "invalid json returns empty object", input: []byte(`{bad json`), expect: "{}"},
		{name: "plain string is invalid json", input: []byte(`hello`), expect: "{}"},
		{name: "nested valid json", input: []byte(`{"a":{"b":1}}`), expect: `{"a":{"b":1}}`},
		{name: "valid json number", input: []byte(`42`), expect: `42`},
		{name: "valid json boolean", input: []byte(`true`), expect: `true`},
		{name: "valid json null", input: []byte(`null`), expect: `null`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := JSONMetadata(tc.input)
			if got != tc.expect {
				t.Errorf("JSONMetadata(%q) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

func TestFormatJSONStringArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []string
		expect string
	}{
		{name: "nil returns empty string", input: nil, expect: ""},
		{name: "empty slice returns empty string", input: []string{}, expect: ""},
		{name: "single element", input: []string{"a"}, expect: `["a"]`},
		{name: "multiple elements", input: []string{"a", "b", "c"}, expect: `["a","b","c"]`},
		{name: "elements with special chars", input: []string{"hello world", "foo\"bar"}, expect: `["hello world","foo\"bar"]`},
		{name: "elements with unicode", input: []string{"\u00e9"}, expect: `["é"]`},
		{name: "empty strings in array", input: []string{""}, expect: `[""]`},
		{name: "mixed empty and non-empty", input: []string{"", "a", ""}, expect: `["","a",""]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FormatJSONStringArray(tc.input)
			if got != tc.expect {
				t.Errorf("FormatJSONStringArray(%v) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

func TestIsWisp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		issue     *types.Issue
		expectVal bool
	}{
		{
			name:      "neither ephemeral nor no_history",
			issue:     &types.Issue{Ephemeral: false, NoHistory: false},
			expectVal: false,
		},
		{
			name:      "ephemeral only",
			issue:     &types.Issue{Ephemeral: true, NoHistory: false},
			expectVal: true,
		},
		{
			name:      "no_history only",
			issue:     &types.Issue{Ephemeral: false, NoHistory: true},
			expectVal: true,
		},
		{
			name:      "both ephemeral and no_history",
			issue:     &types.Issue{Ephemeral: true, NoHistory: true},
			expectVal: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IsWisp(tc.issue)
			if got != tc.expectVal {
				t.Errorf("IsWisp() = %v, want %v", got, tc.expectVal)
			}
		})
	}
}

func TestTableRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		issue       *types.Issue
		wantIssue   string
		wantEvent   string
	}{
		{
			name:      "regular issue routes to issues/events",
			issue:     &types.Issue{Ephemeral: false, NoHistory: false},
			wantIssue: "issues",
			wantEvent: "events",
		},
		{
			name:      "ephemeral routes to wisps/wisp_events",
			issue:     &types.Issue{Ephemeral: true},
			wantIssue: "wisps",
			wantEvent: "wisp_events",
		},
		{
			name:      "no_history routes to wisps/wisp_events",
			issue:     &types.Issue{NoHistory: true},
			wantIssue: "wisps",
			wantEvent: "wisp_events",
		},
		{
			name:      "both flags routes to wisps/wisp_events",
			issue:     &types.Issue{Ephemeral: true, NoHistory: true},
			wantIssue: "wisps",
			wantEvent: "wisp_events",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotIssue, gotEvent := TableRouting(tc.issue)
			if gotIssue != tc.wantIssue {
				t.Errorf("TableRouting() issueTable = %q, want %q", gotIssue, tc.wantIssue)
			}
			if gotEvent != tc.wantEvent {
				t.Errorf("TableRouting() eventTable = %q, want %q", gotEvent, tc.wantEvent)
			}
		})
	}
}
