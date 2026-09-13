package main

import "testing"

// TestValidateLabelsVocabularyConfig has no store dependency (a pure string
// check), so it is deliberately NOT cgo-gated: it must run under the
// pure-Go build too.
func TestValidateLabelsVocabularyConfig(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{"open", false},
		{"warn", false},
		{"enforce", false},
		{"", true},
		{"OPEN", true},
		{"disabled", true},
		{"strict", true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			err := validateLabelsVocabularyConfig(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLabelsVocabularyConfig(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeLabelsVocabularyMode(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"", labelsVocabularyOpen},
		{"open", labelsVocabularyOpen},
		{"warn", labelsVocabularyWarn},
		{"enforce", labelsVocabularyEnforce},
		{"bogus", labelsVocabularyOpen},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := normalizeLabelsVocabularyMode(tt.value); got != tt.want {
				t.Errorf("normalizeLabelsVocabularyMode(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
