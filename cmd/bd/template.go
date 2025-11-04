package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*.yaml
var builtinTemplates embed.FS

// Template represents an issue template
type Template struct {
	Name               string   `yaml:"name" json:"name"`
	Description        string   `yaml:"description" json:"description"`
	Type               string   `yaml:"type" json:"type"`
	Priority           int      `yaml:"priority" json:"priority"`
	Labels             []string `yaml:"labels" json:"labels"`
	Design             string   `yaml:"design" json:"design"`
	AcceptanceCriteria string   `yaml:"acceptance_criteria" json:"acceptance_criteria"`
}

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage issue templates",
	Long: `Manage issue templates for streamlined issue creation.

Templates can be built-in (epic, bug, feature) or custom templates
stored in .beads/templates/ directory.`,
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available templates",
	Run: func(cmd *cobra.Command, args []string) {
		templates, err := loadAllTemplates()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading templates: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(templates)
			return
		}

		// Group by source
		builtins := []Template{}
		customs := []Template{}

		for _, tmpl := range templates {
			if isBuiltinTemplate(tmpl.Name) {
				builtins = append(builtins, tmpl)
			} else {
				customs = append(customs, tmpl)
			}
		}

		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()

		if len(builtins) > 0 {
			fmt.Printf("%s\n", green("Built-in Templates:"))
			for _, tmpl := range builtins {
				fmt.Printf("  %s\n", blue(tmpl.Name))
				fmt.Printf("    Type: %s, Priority: P%d\n", tmpl.Type, tmpl.Priority)
				if len(tmpl.Labels) > 0 {
					fmt.Printf("    Labels: %s\n", strings.Join(tmpl.Labels, ", "))
				}
			}
			fmt.Println()
		}

		if len(customs) > 0 {
			fmt.Printf("%s\n", green("Custom Templates:"))
			for _, tmpl := range customs {
				fmt.Printf("  %s\n", blue(tmpl.Name))
				fmt.Printf("    Type: %s, Priority: P%d\n", tmpl.Type, tmpl.Priority)
				if len(tmpl.Labels) > 0 {
					fmt.Printf("    Labels: %s\n", strings.Join(tmpl.Labels, ", "))
				}
			}
			fmt.Println()
		}

		if len(templates) == 0 {
			fmt.Println("No templates available")
		}
	},
}

var templateShowCmd = &cobra.Command{
	Use:   "show <template-name>",
	Short: "Show template details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		templateName := args[0]
		tmpl, err := loadTemplate(templateName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(tmpl)
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()

		fmt.Printf("%s %s\n", green("Template:"), blue(tmpl.Name))
		fmt.Printf("Type: %s\n", tmpl.Type)
		fmt.Printf("Priority: P%d\n", tmpl.Priority)
		if len(tmpl.Labels) > 0 {
			fmt.Printf("Labels: %s\n", strings.Join(tmpl.Labels, ", "))
		}
		fmt.Printf("\n%s\n%s\n", green("Description:"), tmpl.Description)
		if tmpl.Design != "" {
			fmt.Printf("\n%s\n%s\n", green("Design:"), tmpl.Design)
		}
		if tmpl.AcceptanceCriteria != "" {
			fmt.Printf("\n%s\n%s\n", green("Acceptance Criteria:"), tmpl.AcceptanceCriteria)
		}
	},
}

var templateCreateCmd = &cobra.Command{
	Use:   "create <template-name>",
	Short: "Create a custom template",
	Long: `Create a custom template in .beads/templates/ directory.

This will create a template file that you can edit to customize
the default values for your common issue types.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		templateName := args[0]

		// Sanitize template name
		if err := sanitizeTemplateName(templateName); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Ensure .beads/templates directory exists
		templatesDir := filepath.Join(".beads", "templates")
		if err := os.MkdirAll(templatesDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating templates directory: %v\n", err)
			os.Exit(1)
		}

		// Create template file
		templatePath := filepath.Join(templatesDir, templateName+".yaml")
		if _, err := os.Stat(templatePath); err == nil {
			fmt.Fprintf(os.Stderr, "Error: template '%s' already exists\n", templateName)
			os.Exit(1)
		}

		// Default template structure
		tmpl := Template{
			Name:        templateName,
			Description: "[Describe the issue]\n\n## Additional Context\n\n[Add relevant details]",
			Type:        "task",
			Priority:    2,
			Labels:      []string{},
			Design:      "[Design notes]",
			AcceptanceCriteria: "- [ ] Acceptance criterion 1\n- [ ] Acceptance criterion 2",
		}

		// Marshal to YAML
		data, err := yaml.Marshal(tmpl)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating template: %v\n", err)
			os.Exit(1)
		}

		// Write template file
		if err := os.WriteFile(templatePath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing template: %v\n", err)
			os.Exit(1)
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Created template: %s\n", green("✓"), templatePath)
		fmt.Printf("Edit the file to customize your template.\n")
	},
}

func init() {
	templateCmd.AddCommand(templateListCmd)
	templateCmd.AddCommand(templateShowCmd)
	templateCmd.AddCommand(templateCreateCmd)
	rootCmd.AddCommand(templateCmd)
}

// loadAllTemplates loads both built-in and custom templates
func loadAllTemplates() ([]Template, error) {
	templates := []Template{}

	// Load built-in templates
	builtins := []string{"epic", "bug", "feature"}
	for _, name := range builtins {
		tmpl, err := loadBuiltinTemplate(name)
		if err != nil {
			// Skip if not found (shouldn't happen with built-ins)
			continue
		}
		templates = append(templates, *tmpl)
	}

	// Load custom templates from .beads/templates/
	templatesDir := filepath.Join(".beads", "templates")
	if _, err := os.Stat(templatesDir); err == nil {
		entries, err := os.ReadDir(templatesDir)
		if err != nil {
			return nil, fmt.Errorf("reading templates directory: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}

			name := strings.TrimSuffix(entry.Name(), ".yaml")
			tmpl, err := loadCustomTemplate(name)
			if err != nil {
				// Skip invalid templates
				continue
			}
			templates = append(templates, *tmpl)
		}
	}

	return templates, nil
}

// sanitizeTemplateName validates template name to prevent path traversal
func sanitizeTemplateName(name string) error {
	if name != filepath.Base(name) {
		return fmt.Errorf("invalid template name '%s' (no path separators allowed)", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid template name '%s' (no .. allowed)", name)
	}
	return nil
}

// loadTemplate loads a template by name (checks custom first, then built-in)
func loadTemplate(name string) (*Template, error) {
	if err := sanitizeTemplateName(name); err != nil {
		return nil, err
	}

	// Try custom templates first
	tmpl, err := loadCustomTemplate(name)
	if err == nil {
		return tmpl, nil
	}

	// Fall back to built-in templates
	return loadBuiltinTemplate(name)
}

// loadBuiltinTemplate loads a built-in template
func loadBuiltinTemplate(name string) (*Template, error) {
	path := fmt.Sprintf("templates/%s.yaml", name)
	data, err := builtinTemplates.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("template '%s' not found", name)
	}

	var tmpl Template
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	return &tmpl, nil
}

// loadCustomTemplate loads a custom template from .beads/templates/
func loadCustomTemplate(name string) (*Template, error) {
	path := filepath.Join(".beads", "templates", name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("template '%s' not found", name)
	}

	var tmpl Template
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	return &tmpl, nil
}

// isBuiltinTemplate checks if a template name is a built-in template
func isBuiltinTemplate(name string) bool {
	builtins := map[string]bool{
		"epic":    true,
		"bug":     true,
		"feature": true,
	}
	return builtins[name]
}
