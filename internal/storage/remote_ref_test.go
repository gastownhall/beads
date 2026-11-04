package storage

import (
	"strings"
	"testing"
)

// An unset ref and Dolt's default name the same ref; any other value is
// compared verbatim, so a branch and a ref outside refs/heads/ are distinct
// from each other and from the default.
func TestRemoteRefsMatch(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"", DefaultGitDataRef, true},
		{DefaultGitDataRef, "", true},
		{" refs/dolt/data ", "", true},
		{"refs/heads/issue-data", "refs/heads/issue-data", true},
		{"refs/dolt/units/team-12542", "refs/dolt/units/team-12542", true},
		{"refs/heads/issue-data", "", false},
		{"refs/dolt/units/team-12542", "", false},
		{"refs/heads/issue-data", "refs/dolt/units/team-12542", false},
		{"refs/heads/issue-data", "refs/heads/issue-data-2", false},
	}
	for _, tt := range tests {
		if got := RemoteRefsMatch(tt.a, tt.b); got != tt.want {
			t.Errorf("RemoteRefsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// A params object carries the ref as a string or not at all. A present value
// of any other JSON type is an error rather than the default ref: reading it as
// the default is how a remote pinned to a custom ref gets pushed onto
// refs/dolt/data, which is exactly the failure this parameter exists to
// prevent. An absent key and an explicit null are the genuine "no ref" shapes.
func TestGitRefFromParams(t *testing.T) {
	for _, tt := range []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"absent", map[string]any{"aws-region": "us-east-1"}, ""},
		{"empty object", map[string]any{}, ""},
		{"nil map", nil, ""},
		{"null", map[string]any{GitRefParam: nil}, ""},
		{"empty string", map[string]any{GitRefParam: ""}, ""},
		{"trimmed", map[string]any{GitRefParam: "  refs/dolt/units/k  "}, "refs/dolt/units/k"},
		{"branch", map[string]any{GitRefParam: "refs/heads/issue-data"}, "refs/heads/issue-data"},
	} {
		got, err := GitRefFromParams(tt.params)
		if err != nil || got != tt.want {
			t.Errorf("GitRefFromParams(%s) = %q, %v; want %q, nil", tt.name, got, err, tt.want)
		}
	}

	for _, tt := range []struct {
		name  string
		value any
	}{
		{"array", []any{"refs/dolt/units/x"}},
		{"number", float64(7)},
		{"bool", true},
		{"object", map[string]any{"ref": "refs/dolt/units/x"}},
	} {
		got, err := GitRefFromParams(map[string]any{GitRefParam: tt.value})
		if err == nil {
			t.Errorf("GitRefFromParams(%s) = %q, nil; want an error, not the default ref", tt.name, got)
			continue
		}
		if !strings.Contains(err.Error(), GitRefParam) {
			t.Errorf("GitRefFromParams(%s) error %q does not name %s", tt.name, err, GitRefParam)
		}
	}
}

// GitRefFromParamsJSON reads the same key out of the SQL params column, so it
// must refuse the same shapes: the two readers are one implementation.
func TestGitRefFromParamsJSONRefusesNonStringRef(t *testing.T) {
	for _, raw := range []string{
		`{"git_ref":["refs/dolt/units/x"]}`,
		`{"git_ref":7}`,
		`{"git_ref":true}`,
		`{"git_ref":{"ref":"refs/dolt/units/x"}}`,
	} {
		if got, err := GitRefFromParamsJSON(raw); err == nil {
			t.Errorf("GitRefFromParamsJSON(%s) = %q, nil; want an error", raw, got)
		}
	}
	for _, tt := range []struct{ raw, want string }{
		{"", ""},
		{"null", ""},
		{`{}`, ""},
		{`{"git_ref":null}`, ""},
		{`{"aws-region":"us-east-1"}`, ""},
		{`{"git_ref":"refs/heads/issue-data"}`, "refs/heads/issue-data"},
	} {
		got, err := GitRefFromParamsJSON(tt.raw)
		if err != nil || got != tt.want {
			t.Errorf("GitRefFromParamsJSON(%s) = %q, %v; want %q, nil", tt.raw, got, err, tt.want)
		}
	}
}

func TestEffectiveGitDataRef(t *testing.T) {
	if got := EffectiveGitDataRef(""); got != DefaultGitDataRef {
		t.Errorf("EffectiveGitDataRef(\"\") = %q, want %q", got, DefaultGitDataRef)
	}
	if got := EffectiveGitDataRef("  refs/dolt/units/k  "); got != "refs/dolt/units/k" {
		t.Errorf("EffectiveGitDataRef trims but does not rewrite: got %q", got)
	}
}
