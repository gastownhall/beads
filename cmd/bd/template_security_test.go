package main

import (
	"testing"
)

func TestSanitizeTemplateName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError bool
	}{
		{"valid simple name", "epic", false},
		{"valid with dash", "my-template", false},
		{"valid with underscore", "my_template", false},
		{"path traversal with ../", "../etc/passwd", true},
		{"path traversal with ..", "..", true},
		{"absolute path", "/etc/passwd", true},
		{"relative path", "foo/bar", true},
		{"hidden file", ".hidden", false}, // Hidden files are okay
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sanitizeTemplateName(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("sanitizeTemplateName(%q) error = %v, wantError %v", tt.input, err, tt.wantError)
			}
		})
	}
}

func TestLoadTemplatePathTraversal(t *testing.T) {
	// Try to load a template with path traversal
	_, err := loadTemplate("../../../etc/passwd")
	if err == nil {
		t.Error("Expected error for path traversal, got nil")
	}

	_, err = loadTemplate("foo/bar")
	if err == nil {
		t.Error("Expected error for path with separator, got nil")
	}
}
