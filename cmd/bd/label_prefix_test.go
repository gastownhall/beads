package main

import (
	"reflect"
	"strings"
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

// TestLabelOperationGerund guards against the past-tense-on-failure bug a PR
// reviewer flagged on this exact code path: "label removed: ...: <err>" reads
// as a success report when it is actually the error message for a removal
// that failed. The failure-path callers must use the gerund form instead.
func TestLabelOperationGerund(t *testing.T) {
	tests := []struct {
		operation string
		want      string
	}{
		{labelOperationAdded, "adding"},
		{labelOperationRemoved, "removing"},
		{"custom", "custom"}, // unknown input passes through rather than panicking
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			if got := labelOperationGerund(tt.operation); got != tt.want {
				t.Errorf("labelOperationGerund(%q) = %q, want %q", tt.operation, got, tt.want)
			}
			if got := labelOperationGerund(tt.operation); strings.HasSuffix(got, "ed") {
				t.Errorf("labelOperationGerund(%q) = %q, still reads as past tense", tt.operation, got)
			}
		})
	}
}
