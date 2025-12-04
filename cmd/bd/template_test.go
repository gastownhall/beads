package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBuiltinTemplate(t *testing.T) {
	tests := []struct {
		name          string
		templateName  string
		wantType      string
		wantPriority  int
		wantHasLabels bool
	}{
		{
			name:          "epic template",
			templateName:  "epic",
			wantType:      "epic",
			wantPriority:  1,
			wantHasLabels: true,
		},
		{
			name:          "bug template",
			templateName:  "bug",
			wantType:      "bug",
			wantPriority:  1,
			wantHasLabels: true,
		},
		{
			name:          "feature template",
			templateName:  "feature",
			wantType:      "feature",
			wantPriority:  2,
			wantHasLabels: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := loadBuiltinTemplate(tt.templateName)
			if err != nil {
				t.Fatalf("loadBuiltinTemplate() error = %v", err)
			}

			if tmpl.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", tmpl.Type, tt.wantType)
			}

			if tmpl.Priority != tt.wantPriority {
				t.Errorf("Priority = %v, want %v", tmpl.Priority, tt.wantPriority)
			}

			if tt.wantHasLabels && len(tmpl.Labels) == 0 {
				t.Errorf("Expected labels but got none")
			}

			if tmpl.Description == "" {
				t.Errorf("Expected description but got empty string")
			}

			if tmpl.AcceptanceCriteria == "" {
				t.Errorf("Expected acceptance criteria but got empty string")
			}
		})
	}
}

func TestLoadBuiltinTemplateNotFound(t *testing.T) {
	_, err := loadBuiltinTemplate("nonexistent")
	if err == nil {
		t.Errorf("Expected error for nonexistent template, got nil")
	}
}

func TestLoadCustomTemplate(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .beads/templates directory
	templatesDir := filepath.Join(".beads", "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		t.Fatalf("Failed to create templates directory: %v", err)
	}

	// Create a custom template
	customTemplate := `name: custom-test
description: Test custom template
type: chore
priority: 3
labels:
  - test
  - custom
design: Test design
acceptance_criteria: Test acceptance
`
	templatePath := filepath.Join(templatesDir, "custom-test.yaml")
	if err := os.WriteFile(templatePath, []byte(customTemplate), 0644); err != nil {
		t.Fatalf("Failed to write template: %v", err)
	}

	// Load the custom template
	tmpl, err := loadCustomTemplate("custom-test")
	if err != nil {
		t.Fatalf("loadCustomTemplate() error = %v", err)
	}

	if tmpl.Name != "custom-test" {
		t.Errorf("Name = %v, want custom-test", tmpl.Name)
	}

	if tmpl.Type != "chore" {
		t.Errorf("Type = %v, want chore", tmpl.Type)
	}

	if tmpl.Priority != 3 {
		t.Errorf("Priority = %v, want 3", tmpl.Priority)
	}

	if len(tmpl.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(tmpl.Labels))
	}
}

func TestLoadTemplate_PreferCustomOverBuiltin(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .beads/templates directory
	templatesDir := filepath.Join(".beads", "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		t.Fatalf("Failed to create templates directory: %v", err)
	}

	// Create a custom template with same name as builtin
	customTemplate := `name: epic
description: Custom epic override
type: epic
priority: 0
labels:
  - custom-epic
design: Custom design
acceptance_criteria: Custom acceptance
`
	templatePath := filepath.Join(templatesDir, "epic.yaml")
	if err := os.WriteFile(templatePath, []byte(customTemplate), 0644); err != nil {
		t.Fatalf("Failed to write template: %v", err)
	}

	// loadTemplate should prefer custom over builtin
	tmpl, err := loadTemplate("epic")
	if err != nil {
		t.Fatalf("loadTemplate() error = %v", err)
	}

	// Should get custom template (priority 0) not builtin (priority 1)
	if tmpl.Priority != 0 {
		t.Errorf("Priority = %v, want 0 (custom template)", tmpl.Priority)
	}

	if len(tmpl.Labels) != 1 || tmpl.Labels[0] != "custom-epic" {
		t.Errorf("Expected custom-epic label, got %v", tmpl.Labels)
	}
}

func TestIsBuiltinTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     bool
	}{
		{"epic is builtin", "epic", true},
		{"bug is builtin", "bug", true},
		{"feature is builtin", "feature", true},
		{"custom is not builtin", "custom", false},
		{"random is not builtin", "random", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBuiltinTemplate(tt.template); got != tt.want {
				t.Errorf("isBuiltinTemplate(%v) = %v, want %v", tt.template, got, tt.want)
			}
		})
	}
}
