package fix

import "testing"

// The classifier is the load-bearing part of the #5812 doctor check: it decides
// which stored labels bd can no longer filter for. The SQL around it is a plain
// table scan.
func TestClassifyLabelWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  LabelWhitespaceClass
	}{
		{"plain slug", "theme:a", LabelClean},
		{"empty", "", LabelBlank},
		{"spaces only", "   ", LabelBlank},
		{"tab only", "\t", LabelBlank},

		// The shape pflag's CSV split produces from `--labels 'a, b'`.
		{"leading space", " theme:b", LabelUntrimmed},
		{"trailing space", "theme:b ", LabelUntrimmed},
		{"trailing newline", "theme:b\n", LabelUntrimmed},

		// An internal space is matchable by an identical filter string, so it is
		// not corruption by this definition. Whether a space between labels
		// should separate them is a semantic question settled elsewhere.
		{"internal space", "theme:a theme:b", LabelClean},
		{"internal tab", "theme:a\ttheme:b", LabelClean},
		{"multi-word github label", "good first issue", LabelClean},

		// Outer whitespace is the finding even when the label also has an
		// internal space — the trimmed form is what a filter would match.
		{"untrimmed with internal space", " theme:a theme:b ", LabelUntrimmed},

		// TrimSpace is Unicode-aware, so the check must be too: a non-breaking
		// space is invisible in a terminal and would otherwise slip through.
		{"unicode nbsp leading", "\u00a0theme:a", LabelUntrimmed},
		{"unicode nbsp trailing", "theme:a\u00a0", LabelUntrimmed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyLabelWhitespace(tt.label); got != tt.want {
				t.Errorf("ClassifyLabelWhitespace(%q) = %v, want %v", tt.label, got, tt.want)
			}
		})
	}
}
