package main

import (
	"reflect"
	"testing"
)

func TestLabelsWithPrefix(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		prefix string
		want   []string
	}{
		{
			name:   "matches multiple labels sharing a prefix",
			labels: []string{"pool:refused:bd-cli-blast-radius-needs-human", "pool:refused:engine-rebuild-required", "needs-human"},
			prefix: "pool:refused:",
			want:   []string{"pool:refused:bd-cli-blast-radius-needs-human", "pool:refused:engine-rebuild-required"},
		},
		{
			name:   "no match returns empty",
			labels: []string{"needs-human", "story:blocked"},
			prefix: "pool:refused:",
			want:   nil,
		},
		{
			name:   "empty prefix matches everything",
			labels: []string{"a", "b"},
			prefix: "",
			want:   []string{"a", "b"},
		},
		{
			name:   "does not match a substring that isn't a prefix",
			labels: []string{"gate:passed", "needs-gate:review"},
			prefix: "gate:",
			want:   []string{"gate:passed"},
		},
		{
			name:   "empty label set returns empty",
			labels: nil,
			prefix: "pool:refused:",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := labelsWithPrefix(tt.labels, tt.prefix)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("labelsWithPrefix(%v, %q) = %v, want %v", tt.labels, tt.prefix, got, tt.want)
			}
		})
	}
}
