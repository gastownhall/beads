package formula

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FormulaExt is the file extension for formula files.
const FormulaExt = ".formula.json"

// Parser handles loading and resolving formulas.
//
// NOTE: Parser is NOT thread-safe. Create a new Parser per goroutine or
// synchronize access externally. The cache and resolving maps have no
// internal synchronization.
type Parser struct {
	// searchPaths are directories to search for formulas (in order).
	searchPaths []string

	// cache stores loaded formulas by name.
	cache map[string]*Formula

	// resolving tracks formulas currently being resolved (for cycle detection).
	resolving map[string]bool
}

// NewParser creates a new formula parser.
// searchPaths are directories to search for formulas when resolving extends.
// Default paths are: .beads/formulas, ~/.beads/formulas, ~/gt/.beads/formulas
func NewParser(searchPaths ...string) *Parser {
	paths := searchPaths
	if len(paths) == 0 {
		paths = defaultSearchPaths()
	}
	return &Parser{
		searchPaths: paths,
		cache:       make(map[string]*Formula),
		resolving:   make(map[string]bool),
	}
}

// defaultSearchPaths returns the default formula search paths.
func defaultSearchPaths() []string {
	var paths []string

	// Project-level formulas
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".beads", "formulas"))
	}

	// User-level formulas
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".beads", "formulas"))

		// Gas Town formulas
		paths = append(paths, filepath.Join(home, "gt", ".beads", "formulas"))
	}

	return paths
}

// ParseFile parses a formula from a file path.
func (p *Parser) ParseFile(path string) (*Formula, error) {
	// Check cache first
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	if cached, ok := p.cache[absPath]; ok {
		return cached, nil
	}

	// Read and parse the file
	// #nosec G304 -- absPath comes from controlled search paths or explicit user input
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	formula, err := p.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	formula.Source = absPath
	p.cache[absPath] = formula

	// Also cache by name for extends resolution
	p.cache[formula.Formula] = formula

	return formula, nil
}

// Parse parses a formula from JSON bytes.
func (p *Parser) Parse(data []byte) (*Formula, error) {
	var formula Formula
	if err := json.Unmarshal(data, &formula); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}

	// Set defaults
	if formula.Version == 0 {
		formula.Version = 1
	}
	if formula.Type == "" {
		formula.Type = TypeWorkflow
	}

	return &formula, nil
}

// Resolve fully resolves a formula, processing extends and expansions.
// Returns a new formula with all inheritance applied.
func (p *Parser) Resolve(formula *Formula) (*Formula, error) {
	// Check for cycles
	if p.resolving[formula.Formula] {
		return nil, fmt.Errorf("circular extends detected: %s", formula.Formula)
	}
	p.resolving[formula.Formula] = true
	defer delete(p.resolving, formula.Formula)

	// If no extends, just validate and return
	if len(formula.Extends) == 0 {
		if err := formula.Validate(); err != nil {
			return nil, err
		}
		return formula, nil
	}

	// Build merged formula from parents
	merged := &Formula{
		Formula:     formula.Formula,
		Description: formula.Description,
		Version:     formula.Version,
		Type:        formula.Type,
		Source:      formula.Source,
		Vars:        make(map[string]*VarDef),
		Steps:       nil,
		Compose:     nil,
	}

	// Apply each parent in order
	for _, parentName := range formula.Extends {
		parent, err := p.loadFormula(parentName)
		if err != nil {
			return nil, fmt.Errorf("extends %s: %w", parentName, err)
		}

		// Resolve parent recursively
		parent, err = p.Resolve(parent)
		if err != nil {
			return nil, fmt.Errorf("resolve parent %s: %w", parentName, err)
		}

		// Merge parent vars (parent vars are inherited, child overrides)
		for name, varDef := range parent.Vars {
			if _, exists := merged.Vars[name]; !exists {
				merged.Vars[name] = varDef
			}
		}

		// Merge parent steps (append, child steps come after)
		merged.Steps = append(merged.Steps, parent.Steps...)

		// Merge parent compose rules
		merged.Compose = mergeComposeRules(merged.Compose, parent.Compose)
	}

	// Apply child overrides
	for name, varDef := range formula.Vars {
		merged.Vars[name] = varDef
	}
	merged.Steps = append(merged.Steps, formula.Steps...)
	merged.Compose = mergeComposeRules(merged.Compose, formula.Compose)

	// Use child description if set
	if formula.Description != "" {
		merged.Description = formula.Description
	}

	if err := merged.Validate(); err != nil {
		return nil, err
	}

	return merged, nil
}

// loadFormula loads a formula by name from search paths.
func (p *Parser) loadFormula(name string) (*Formula, error) {
	// Check cache first
	if cached, ok := p.cache[name]; ok {
		return cached, nil
	}

	// Search for the formula file
	filename := name + FormulaExt
	for _, dir := range p.searchPaths {
		path := filepath.Join(dir, filename)
		if _, err := os.Stat(path); err == nil {
			return p.ParseFile(path)
		}
	}

	return nil, fmt.Errorf("formula %q not found in search paths", name)
}

// LoadByName loads a formula by name from search paths.
// This is the public API for loading formulas used by expansion operators.
func (p *Parser) LoadByName(name string) (*Formula, error) {
	return p.loadFormula(name)
}

// mergeComposeRules merges two compose rule sets.
func mergeComposeRules(base, overlay *ComposeRules) *ComposeRules {
	if overlay == nil {
		return base
	}
	if base == nil {
		return overlay
	}

	result := &ComposeRules{
		BondPoints: append([]*BondPoint{}, base.BondPoints...),
		Hooks:      append([]*Hook{}, base.Hooks...),
		Expand:     append([]*ExpandRule{}, base.Expand...),
		Map:        append([]*MapRule{}, base.Map...),
	}

	// Add overlay bond points (override by ID)
	existingBP := make(map[string]int)
	for i, bp := range result.BondPoints {
		existingBP[bp.ID] = i
	}
	for _, bp := range overlay.BondPoints {
		if idx, exists := existingBP[bp.ID]; exists {
			result.BondPoints[idx] = bp
		} else {
			result.BondPoints = append(result.BondPoints, bp)
		}
	}

	// Add overlay hooks (append, no override)
	result.Hooks = append(result.Hooks, overlay.Hooks...)

	// Add overlay expand rules (append, no override)
	result.Expand = append(result.Expand, overlay.Expand...)

	// Add overlay map rules (append, no override)
	result.Map = append(result.Map, overlay.Map...)

	return result
}

// varPattern matches {{variable}} placeholders.
var varPattern = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// ExtractVariables finds all {{variable}} references in a formula.
func ExtractVariables(formula *Formula) []string {
	seen := make(map[string]bool)
	var vars []string

	// Helper to extract vars from a string
	extract := func(s string) {
		matches := varPattern.FindAllStringSubmatch(s, -1)
		for _, match := range matches {
			if len(match) >= 2 && !seen[match[1]] {
				seen[match[1]] = true
				vars = append(vars, match[1])
			}
		}
	}

	// Extract from formula fields
	extract(formula.Description)

	// Extract from steps
	var extractFromStep func(*Step)
	extractFromStep = func(step *Step) {
		extract(step.Title)
		extract(step.Description)
		extract(step.Assignee)
		extract(step.Condition)
		for _, child := range step.Children {
			extractFromStep(child)
		}
	}

	for _, step := range formula.Steps {
		extractFromStep(step)
	}

	return vars
}

// Substitute replaces {{variable}} placeholders with values.
func Substitute(s string, vars map[string]string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name from {{name}}
		name := match[2 : len(match)-2]
		if val, ok := vars[name]; ok {
			return val
		}
		return match // Keep unresolved placeholders
	})
}

// ValidateVars checks that all required variables are provided
// and all values pass their constraints.
func ValidateVars(formula *Formula, values map[string]string) error {
	var errs []string

	for name, def := range formula.Vars {
		val, provided := values[name]

		// Check required
		if def.Required && !provided {
			errs = append(errs, fmt.Sprintf("variable %q is required", name))
			continue
		}

		// Use default if not provided
		if !provided && def.Default != "" {
			val = def.Default
		}

		// Skip further validation if no value
		if val == "" {
			continue
		}

		// Check enum constraint
		if len(def.Enum) > 0 {
			found := false
			for _, allowed := range def.Enum {
				if val == allowed {
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, fmt.Sprintf("variable %q: value %q not in allowed values %v", name, val, def.Enum))
			}
		}

		// Check pattern constraint
		if def.Pattern != "" {
			re, err := regexp.Compile(def.Pattern)
			if err != nil {
				errs = append(errs, fmt.Sprintf("variable %q: invalid pattern %q: %v", name, def.Pattern, err))
			} else if !re.MatchString(val) {
				errs = append(errs, fmt.Sprintf("variable %q: value %q does not match pattern %q", name, val, def.Pattern))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("variable validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return nil
}

// ApplyDefaults returns a new map with default values filled in.
func ApplyDefaults(formula *Formula, values map[string]string) map[string]string {
	result := make(map[string]string)

	// Copy provided values
	for k, v := range values {
		result[k] = v
	}

	// Apply defaults for missing values
	for name, def := range formula.Vars {
		if _, exists := result[name]; !exists && def.Default != "" {
			result[name] = def.Default
		}
	}

	return result
}
