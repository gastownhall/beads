package main

import "testing"

func TestFindStateLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		labels    []string
		dimension string
		wantValue string
		wantFound bool
	}{
		{
			name:      "first exact dimension match wins",
			labels:    []string{"mode:normal", "mode:degraded"},
			dimension: "mode",
			wantValue: "normal",
			wantFound: true,
		},
		{
			name:      "retains colons in value",
			labels:    []string{"error:code:500"},
			dimension: "error",
			wantValue: "code:500",
			wantFound: true,
		},
		{
			name:      "empty value is found",
			labels:    []string{"mode:"},
			dimension: "mode",
			wantValue: "",
			wantFound: true,
		},
		{
			name:      "does not match dimension prefix without colon",
			labels:    []string{"mode-extra:degraded"},
			dimension: "mode",
			wantValue: "",
			wantFound: false,
		},
		{
			name:      "missing dimension",
			labels:    []string{"mode:normal"},
			dimension: "health",
			wantValue: "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, found := findStateLabel(tt.labels, tt.dimension)
			if value != tt.wantValue || found != tt.wantFound {
				t.Fatalf("findStateLabel(%q, %q) = (%q, %t), want (%q, %t)", tt.labels, tt.dimension, value, found, tt.wantValue, tt.wantFound)
			}
		})
	}
}

func TestCollectStateLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels []string
		want   map[string]string
	}{
		{
			name: "nil labels returns nonnil empty map",
			want: map[string]string{},
		},
		{
			name:   "empty labels returns nonnil empty map",
			labels: []string{},
			want:   map[string]string{},
		},
		{
			name:   "ignores labels without a nonempty dimension",
			labels: []string{"backend", ":value"},
			want:   map[string]string{},
		},
		{
			name:   "retains empty and colon values",
			labels: []string{"mode:", "error:code:500"},
			want:   map[string]string{"mode": "", "error": "code:500"},
		},
		{
			name:   "last duplicate wins",
			labels: []string{"mode:normal", "mode:degraded"},
			want:   map[string]string{"mode": "degraded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectStateLabels(tt.labels)
			if got == nil {
				t.Fatal("collectStateLabels returned a nil map")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("collectStateLabels(%q) has %d values, want %d: %v", tt.labels, len(got), len(tt.want), got)
			}
			for dimension, wantValue := range tt.want {
				if gotValue, ok := got[dimension]; !ok || gotValue != wantValue {
					t.Errorf("collectStateLabels(%q)[%q] = (%q, %t), want (%q, true)", tt.labels, dimension, gotValue, ok, wantValue)
				}
			}
		})
	}
}
