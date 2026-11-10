package doltutil

import "testing"

func TestQuoteIdentifierUnvalidated(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "beads_x", "`beads_x`"},
		{"empty", "", "``"},
		{"backtick breaks out", "evil`; DROP TABLE x", "`evil``; DROP TABLE x`"},
		{"leading backtick", "`beads", "```beads`"},
		{"only backtick", "`", "````"},
		{"dot is not special", "a.b", "`a.b`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QuoteIdentifierUnvalidated(tt.in); got != tt.want {
				t.Errorf("QuoteIdentifierUnvalidated(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
