package dolt

import (
	"testing"
)

func TestEscapeSQL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"review", "review"},
		{"it's", "it''s"},
		{"a'b'c", "a''b''c"},
		{"normal-status_123", "normal-status_123"},
		{"'; DROP TABLE issues; --", "''; DROP TABLE issues; --"},
		{"review' OR '1'='1", "review'' OR ''1''=''1"},
	}
	for _, tt := range tests {
		got := escapeSQL(tt.input)
		if got != tt.want {
			t.Errorf("escapeSQL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
