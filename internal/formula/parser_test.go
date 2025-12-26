package formula

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_BasicFormula(t *testing.T) {
	jsonData := `{
  "formula": "mol-test",
  "description": "Test workflow",
  "version": 1,
  "type": "workflow",
  "vars": {
    "component": {
      "description": "Component name",
      "required": true
    },
    "framework": {
      "description": "Target framework",
      "default": "react",
      "enum": ["react", "vue", "angular"]
    }
  },
  "steps": [
    {"id": "design", "title": "Design {{component}}", "type": "task", "priority": 1},
    {"id": "implement", "title": "Implement {{component}}", "type": "task", "depends_on": ["design"]},
    {"id": "test", "title": "Test {{component}} with {{framework}}", "type": "task", "depends_on": ["implement"]}
  ]
}`
	p := NewParser()
	formula, err := p.Parse([]byte(jsonData))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Check basic fields
	if formula.Formula != "mol-test" {
		t.Errorf("Formula = %q, want mol-test", formula.Formula)
	}
	if formula.Description != "Test workflow" {
		t.Errorf("Description = %q, want 'Test workflow'", formula.Description)
	}
	if formula.Version != 1 {
		t.Errorf("Version = %d, want 1", formula.Version)
	}
	if formula.Type != TypeWorkflow {
		t.Errorf("Type = %q, want workflow", formula.Type)
	}

	// Check vars
	if len(formula.Vars) != 2 {
		t.Fatalf("len(Vars) = %d, want 2", len(formula.Vars))
	}
	if v := formula.Vars["component"]; v == nil || !v.Required {
		t.Error("component var should be required")
	}
	if v := formula.Vars["framework"]; v == nil || v.Default != "react" {
		t.Error("framework var should have default 'react'")
	}
	if v := formula.Vars["framework"]; v == nil || len(v.Enum) != 3 {
		t.Error("framework var should have 3 enum values")
	}

	// Check steps
	if len(formula.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(formula.Steps))
	}
	if formula.Steps[0].ID != "design" {
		t.Errorf("Steps[0].ID = %q, want 'design'", formula.Steps[0].ID)
	}
	if formula.Steps[1].DependsOn[0] != "design" {
		t.Errorf("Steps[1].DependsOn = %v, want [design]", formula.Steps[1].DependsOn)
	}
}

func TestValidate_ValidFormula(t *testing.T) {
	formula := &Formula{
		Formula: "mol-valid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1"},
			{ID: "step2", Title: "Step 2", DependsOn: []string{"step1"}},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid formula: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	formula := &Formula{
		Version: 1,
		Type:    TypeWorkflow,
		Steps:   []*Step{{ID: "step1", Title: "Step 1"}},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for formula without name")
	}
}

func TestValidate_DuplicateStepID(t *testing.T) {
	formula := &Formula{
		Formula: "mol-dup",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1"},
			{ID: "step1", Title: "Step 1 again"}, // duplicate
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for duplicate step IDs")
	}
}

func TestValidate_InvalidDependency(t *testing.T) {
	formula := &Formula{
		Formula: "mol-bad-dep",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", DependsOn: []string{"nonexistent"}},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for dependency on nonexistent step")
	}
}

func TestValidate_RequiredWithDefault(t *testing.T) {
	formula := &Formula{
		Formula: "mol-bad-var",
		Version: 1,
		Type:    TypeWorkflow,
		Vars: map[string]*VarDef{
			"bad": {Required: true, Default: "value"}, // can't have both
		},
		Steps: []*Step{{ID: "step1", Title: "Step 1"}},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for required var with default")
	}
}

func TestValidate_InvalidPriority(t *testing.T) {
	p := 10 // invalid: must be 0-4
	formula := &Formula{
		Formula: "mol-bad-priority",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", Priority: &p},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for priority > 4")
	}
}

func TestValidate_ChildSteps(t *testing.T) {
	formula := &Formula{
		Formula: "mol-children",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1"},
					{ID: "child2", Title: "Child 2", DependsOn: []string{"child1"}},
				},
			},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid nested formula: %v", err)
	}
}

func TestValidate_ChildStepsInvalidDependsOn(t *testing.T) {
	formula := &Formula{
		Formula: "mol-bad-child-dep",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1"},
					{ID: "child2", Title: "Child 2", DependsOn: []string{"nonexistent"}},
				},
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for child depends_on referencing unknown step")
	}
}

func TestValidate_ChildStepsInvalidPriority(t *testing.T) {
	p := 10 // invalid
	formula := &Formula{
		Formula: "mol-bad-child-priority",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1", Priority: &p},
				},
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for child with invalid priority")
	}
}

func TestValidate_BondPoints(t *testing.T) {
	formula := &Formula{
		Formula: "mol-compose",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1"},
			{ID: "step2", Title: "Step 2"},
		},
		Compose: &ComposeRules{
			BondPoints: []*BondPoint{
				{ID: "after-step1", AfterStep: "step1"},
				{ID: "before-step2", BeforeStep: "step2"},
			},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid bond points: %v", err)
	}
}

func TestValidate_BondPointBothAnchors(t *testing.T) {
	formula := &Formula{
		Formula: "mol-bad-bond",
		Version: 1,
		Type:    TypeWorkflow,
		Steps:   []*Step{{ID: "step1", Title: "Step 1"}},
		Compose: &ComposeRules{
			BondPoints: []*BondPoint{
				{ID: "bad", AfterStep: "step1", BeforeStep: "step1"}, // can't have both
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("Validate should fail for bond point with both after_step and before_step")
	}
}

func TestExtractVariables(t *testing.T) {
	formula := &Formula{
		Formula:     "mol-vars",
		Description: "Build {{project}} for {{env}}",
		Steps: []*Step{
			{ID: "s1", Title: "Deploy {{project}} to {{env}}"},
			{ID: "s2", Title: "Notify {{owner}}"},
		},
	}

	vars := ExtractVariables(formula)
	want := map[string]bool{"project": true, "env": true, "owner": true}

	if len(vars) != len(want) {
		t.Errorf("ExtractVariables found %d vars, want %d", len(vars), len(want))
	}
	for _, v := range vars {
		if !want[v] {
			t.Errorf("Unexpected variable: %q", v)
		}
	}
}

func TestSubstitute(t *testing.T) {
	tests := []struct {
		input string
		vars  map[string]string
		want  string
	}{
		{
			input: "Deploy {{project}} to {{env}}",
			vars:  map[string]string{"project": "myapp", "env": "prod"},
			want:  "Deploy myapp to prod",
		},
		{
			input: "{{name}} version {{version}}",
			vars:  map[string]string{"name": "beads"},
			want:  "beads version {{version}}", // unresolved kept
		},
		{
			input: "No variables here",
			vars:  map[string]string{"unused": "value"},
			want:  "No variables here",
		},
	}

	for _, tt := range tests {
		got := Substitute(tt.input, tt.vars)
		if got != tt.want {
			t.Errorf("Substitute(%q, %v) = %q, want %q", tt.input, tt.vars, got, tt.want)
		}
	}
}

func TestValidateVars(t *testing.T) {
	formula := &Formula{
		Formula: "mol-vars",
		Vars: map[string]*VarDef{
			"required_var": {Required: true},
			"enum_var":     {Enum: []string{"a", "b", "c"}},
			"pattern_var":  {Pattern: `^[a-z]+$`},
			"optional_var": {Default: "default"},
		},
	}

	tests := []struct {
		name    string
		values  map[string]string
		wantErr bool
	}{
		{
			name:    "missing required",
			values:  map[string]string{},
			wantErr: true,
		},
		{
			name:    "all provided",
			values:  map[string]string{"required_var": "value"},
			wantErr: false,
		},
		{
			name:    "valid enum",
			values:  map[string]string{"required_var": "x", "enum_var": "a"},
			wantErr: false,
		},
		{
			name:    "invalid enum",
			values:  map[string]string{"required_var": "x", "enum_var": "invalid"},
			wantErr: true,
		},
		{
			name:    "valid pattern",
			values:  map[string]string{"required_var": "x", "pattern_var": "abc"},
			wantErr: false,
		},
		{
			name:    "invalid pattern",
			values:  map[string]string{"required_var": "x", "pattern_var": "123"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVars(formula, tt.values)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateVars() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestApplyDefaults(t *testing.T) {
	formula := &Formula{
		Formula: "mol-defaults",
		Vars: map[string]*VarDef{
			"with_default":    {Default: "default_value"},
			"without_default": {},
		},
	}

	values := map[string]string{"without_default": "provided"}
	result := ApplyDefaults(formula, values)

	if result["with_default"] != "default_value" {
		t.Errorf("with_default = %q, want 'default_value'", result["with_default"])
	}
	if result["without_default"] != "provided" {
		t.Errorf("without_default = %q, want 'provided'", result["without_default"])
	}
}

func TestParseFile_AndResolve(t *testing.T) {
	// Create temp directory with test formulas
	dir := t.TempDir()
	formulaDir := filepath.Join(dir, ".beads", "formulas")
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write parent formula
	parent := `{
  "formula": "base-workflow",
  "version": 1,
  "type": "workflow",
  "vars": {
    "project": {
      "description": "Project name",
      "required": true
    }
  },
  "steps": [
    {"id": "init", "title": "Initialize {{project}}"}
  ]
}`
	if err := os.WriteFile(filepath.Join(formulaDir, "base-workflow.formula.json"), []byte(parent), 0644); err != nil {
		t.Fatalf("write parent: %v", err)
	}

	// Write child formula that extends parent
	child := `{
  "formula": "extended-workflow",
  "version": 1,
  "type": "workflow",
  "extends": ["base-workflow"],
  "vars": {
    "env": {
      "default": "dev"
    }
  },
  "steps": [
    {"id": "deploy", "title": "Deploy {{project}} to {{env}}", "depends_on": ["init"]}
  ]
}`
	childPath := filepath.Join(formulaDir, "extended-workflow.formula.json")
	if err := os.WriteFile(childPath, []byte(child), 0644); err != nil {
		t.Fatalf("write child: %v", err)
	}

	// Parse and resolve
	p := NewParser(formulaDir)
	formula, err := p.ParseFile(childPath)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	resolved, err := p.Resolve(formula)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Check inheritance
	if len(resolved.Vars) != 2 {
		t.Errorf("len(Vars) = %d, want 2 (inherited + child)", len(resolved.Vars))
	}
	if resolved.Vars["project"] == nil {
		t.Error("inherited var 'project' not found")
	}
	if resolved.Vars["env"] == nil {
		t.Error("child var 'env' not found")
	}

	// Check steps (parent + child)
	if len(resolved.Steps) != 2 {
		t.Errorf("len(Steps) = %d, want 2", len(resolved.Steps))
	}
	if resolved.Steps[0].ID != "init" {
		t.Errorf("Steps[0].ID = %q, want 'init' (inherited)", resolved.Steps[0].ID)
	}
	if resolved.Steps[1].ID != "deploy" {
		t.Errorf("Steps[1].ID = %q, want 'deploy' (child)", resolved.Steps[1].ID)
	}
}

func TestResolve_CircularExtends(t *testing.T) {
	dir := t.TempDir()
	formulaDir := filepath.Join(dir, ".beads", "formulas")
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write formulas that extend each other (cycle)
	formulaA := `{
  "formula": "cycle-a",
  "version": 1,
  "type": "workflow",
  "extends": ["cycle-b"],
  "steps": [{"id": "a", "title": "A"}]
}`
	formulaB := `{
  "formula": "cycle-b",
  "version": 1,
  "type": "workflow",
  "extends": ["cycle-a"],
  "steps": [{"id": "b", "title": "B"}]
}`
	if err := os.WriteFile(filepath.Join(formulaDir, "cycle-a.formula.json"), []byte(formulaA), 0644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(formulaDir, "cycle-b.formula.json"), []byte(formulaB), 0644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	p := NewParser(formulaDir)
	formula, err := p.ParseFile(filepath.Join(formulaDir, "cycle-a.formula.json"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	_, err = p.Resolve(formula)
	if err == nil {
		t.Error("Resolve should fail for circular extends")
	}

	// Verify the error message shows the full cycle chain
	errStr := err.Error()
	if !strings.Contains(errStr, "cycle-a") {
		t.Errorf("error should mention cycle-a: %v", err)
	}
	if !strings.Contains(errStr, "cycle-b") {
		t.Errorf("error should mention cycle-b: %v", err)
	}
	if !strings.Contains(errStr, "->") {
		t.Errorf("error should show cycle chain with '->': %v", err)
	}
}

func TestGetStepByID(t *testing.T) {
	formula := &Formula{
		Formula: "mol-nested",
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1"},
					{
						ID:    "child2",
						Title: "Child 2",
						Children: []*Step{
							{ID: "grandchild", Title: "Grandchild"},
						},
					},
				},
			},
			{ID: "step2", Title: "Step 2"},
		},
	}

	tests := []struct {
		id   string
		want string
	}{
		{"epic1", "Epic 1"},
		{"child1", "Child 1"},
		{"grandchild", "Grandchild"},
		{"step2", "Step 2"},
		{"nonexistent", ""},
	}

	for _, tt := range tests {
		step := formula.GetStepByID(tt.id)
		if tt.want == "" {
			if step != nil {
				t.Errorf("GetStepByID(%q) = %v, want nil", tt.id, step)
			}
		} else {
			if step == nil || step.Title != tt.want {
				t.Errorf("GetStepByID(%q).Title = %v, want %q", tt.id, step, tt.want)
			}
		}
	}
}

func TestFormulaType_IsValid(t *testing.T) {
	tests := []struct {
		t    FormulaType
		want bool
	}{
		{TypeWorkflow, true},
		{TypeExpansion, true},
		{TypeAspect, true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := tt.t.IsValid(); got != tt.want {
			t.Errorf("%q.IsValid() = %v, want %v", tt.t, got, tt.want)
		}
	}
}

// TestValidate_NeedsField tests validation of the needs field (bd-hr39)
func TestValidate_NeedsField(t *testing.T) {
	// Valid needs reference
	formula := &Formula{
		Formula: "mol-needs",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1"},
			{ID: "step2", Title: "Step 2", Needs: []string{"step1"}},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid needs reference: %v", err)
	}

	// Invalid needs reference
	formulaBad := &Formula{
		Formula: "mol-bad-needs",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1"},
			{ID: "step2", Title: "Step 2", Needs: []string{"nonexistent"}},
		},
	}

	err := formulaBad.Validate()
	if err == nil {
		t.Error("Validate should fail for needs referencing unknown step")
	}
}

// TestValidate_WaitsForField tests validation of the waits_for field (bd-j4cr)
func TestValidate_WaitsForField(t *testing.T) {
	// Valid waits_for value
	formula := &Formula{
		Formula: "mol-waits-for",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "fanout", Title: "Fanout"},
			{ID: "aggregate", Title: "Aggregate", Needs: []string{"fanout"}, WaitsFor: "all-children"},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid waits_for: %v", err)
	}

	// Invalid waits_for value
	formulaBad := &Formula{
		Formula: "mol-bad-waits-for",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", WaitsFor: "invalid-gate"},
		},
	}

	err := formulaBad.Validate()
	if err == nil {
		t.Error("Validate should fail for invalid waits_for value")
	}
}

// TestValidate_WaitsForChildrenOf tests the children-of(step) syntax (gt-8tmz.38)
func TestValidate_WaitsForChildrenOf(t *testing.T) {
	// Valid children-of() syntax
	formula := &Formula{
		Formula: "mol-children-of",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "survey-workers", Title: "Survey Workers"},
			{ID: "aggregate", Title: "Aggregate", Needs: []string{"survey-workers"}, WaitsFor: "children-of(survey-workers)"},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid children-of(): %v", err)
	}

	// Invalid: reference to unknown step
	formulaBad := &Formula{
		Formula: "mol-bad-children-of",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", WaitsFor: "children-of(unknown-step)"},
		},
	}

	if err := formulaBad.Validate(); err == nil {
		t.Error("Validate should fail for children-of() with unknown step")
	}

	// Invalid: empty step ID
	formulaEmpty := &Formula{
		Formula: "mol-empty-children-of",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", WaitsFor: "children-of()"},
		},
	}

	if err := formulaEmpty.Validate(); err == nil {
		t.Error("Validate should fail for children-of() with empty step ID")
	}
}

// TestParseWaitsFor tests the ParseWaitsFor helper function (gt-8tmz.38)
func TestParseWaitsFor(t *testing.T) {
	tests := []struct {
		input     string
		wantGate  string
		wantSpawn string
		wantNil   bool
	}{
		{"", "", "", true},
		{"all-children", "all-children", "", false},
		{"any-children", "any-children", "", false},
		{"children-of(survey)", "all-children", "survey", false},
		{"children-of(my-step)", "all-children", "my-step", false},
		{"invalid", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			spec := ParseWaitsFor(tt.input)
			if tt.wantNil {
				if spec != nil {
					t.Errorf("ParseWaitsFor(%q) = %+v, want nil", tt.input, spec)
				}
				return
			}
			if spec == nil {
				t.Fatalf("ParseWaitsFor(%q) = nil, want non-nil", tt.input)
			}
			if spec.Gate != tt.wantGate {
				t.Errorf("ParseWaitsFor(%q).Gate = %q, want %q", tt.input, spec.Gate, tt.wantGate)
			}
			if spec.SpawnerID != tt.wantSpawn {
				t.Errorf("ParseWaitsFor(%q).SpawnerID = %q, want %q", tt.input, spec.SpawnerID, tt.wantSpawn)
			}
		})
	}
}

// TestValidate_ChildNeedsAndWaitsFor tests needs and waits_for in child steps
func TestValidate_ChildNeedsAndWaitsFor(t *testing.T) {
	formula := &Formula{
		Formula: "mol-child-fields",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1"},
					{ID: "child2", Title: "Child 2", Needs: []string{"child1"}, WaitsFor: "any-children"},
				},
			},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid child needs/waits_for: %v", err)
	}

	// Invalid child needs
	formulaBadNeeds := &Formula{
		Formula: "mol-bad-child-needs",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1", Needs: []string{"nonexistent"}},
				},
			},
		},
	}

	if err := formulaBadNeeds.Validate(); err == nil {
		t.Error("Validate should fail for child with invalid needs reference")
	}

	// Invalid child waits_for
	formulaBadWaitsFor := &Formula{
		Formula: "mol-bad-child-waits-for",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "epic1",
				Title: "Epic 1",
				Children: []*Step{
					{ID: "child1", Title: "Child 1", WaitsFor: "bad-value"},
				},
			},
		},
	}

	if err := formulaBadWaitsFor.Validate(); err == nil {
		t.Error("Validate should fail for child with invalid waits_for")
	}
}

// TestParse_NeedsAndWaitsFor tests JSON parsing of needs and waits_for fields
func TestParse_NeedsAndWaitsFor(t *testing.T) {
	jsonData := `{
  "formula": "mol-deacon",
  "version": 1,
  "type": "workflow",
  "steps": [
    {"id": "inbox-check", "title": "Check inbox"},
    {"id": "health-scan", "title": "Check health", "needs": ["inbox-check"]},
    {"id": "aggregate", "title": "Aggregate results", "needs": ["health-scan"], "waits_for": "all-children"}
  ]
}`
	p := NewParser()
	formula, err := p.Parse([]byte(jsonData))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Validate parsed formula
	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}

	// Check needs field
	if len(formula.Steps[1].Needs) != 1 || formula.Steps[1].Needs[0] != "inbox-check" {
		t.Errorf("Steps[1].Needs = %v, want [inbox-check]", formula.Steps[1].Needs)
	}

	// Check waits_for field
	if formula.Steps[2].WaitsFor != "all-children" {
		t.Errorf("Steps[2].WaitsFor = %q, want 'all-children'", formula.Steps[2].WaitsFor)
	}
}

// gt-8tmz.8: Tests for on_complete/for-each runtime expansion

func TestParse_OnComplete(t *testing.T) {
	jsonData := `{
  "formula": "mol-patrol",
  "version": 1,
  "type": "workflow",
  "steps": [
    {
      "id": "survey-workers",
      "title": "Survey workers",
      "on_complete": {
        "for_each": "output.polecats",
        "bond": "mol-polecat-arm",
        "vars": {
          "polecat_name": "{item.name}",
          "rig": "{item.rig}"
        },
        "parallel": true
      }
    },
    {
      "id": "aggregate",
      "title": "Aggregate results",
      "needs": ["survey-workers"],
      "waits_for": "all-children"
    }
  ]
}`
	p := NewParser()
	formula, err := p.Parse([]byte(jsonData))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Validate parsed formula
	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}

	// Check on_complete field
	oc := formula.Steps[0].OnComplete
	if oc == nil {
		t.Fatal("Steps[0].OnComplete is nil")
	}
	if oc.ForEach != "output.polecats" {
		t.Errorf("ForEach = %q, want 'output.polecats'", oc.ForEach)
	}
	if oc.Bond != "mol-polecat-arm" {
		t.Errorf("Bond = %q, want 'mol-polecat-arm'", oc.Bond)
	}
	if len(oc.Vars) != 2 {
		t.Errorf("len(Vars) = %d, want 2", len(oc.Vars))
	}
	if oc.Vars["polecat_name"] != "{item.name}" {
		t.Errorf("Vars[polecat_name] = %q, want '{item.name}'", oc.Vars["polecat_name"])
	}
	if !oc.Parallel {
		t.Error("Parallel should be true")
	}
}

func TestValidate_OnComplete_Valid(t *testing.T) {
	formula := &Formula{
		Formula: "mol-valid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "survey",
				Title: "Survey",
				OnComplete: &OnCompleteSpec{
					ForEach: "output.items",
					Bond:    "mol-item",
				},
			},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for valid on_complete: %v", err)
	}
}

func TestValidate_OnComplete_MissingBond(t *testing.T) {
	formula := &Formula{
		Formula: "mol-invalid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "survey",
				Title: "Survey",
				OnComplete: &OnCompleteSpec{
					ForEach: "output.items",
					// Bond is missing
				},
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("expected validation error for missing bond")
	}
	if !strings.Contains(err.Error(), "bond is required") {
		t.Errorf("expected 'bond is required' error, got: %v", err)
	}
}

func TestValidate_OnComplete_MissingForEach(t *testing.T) {
	formula := &Formula{
		Formula: "mol-invalid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "survey",
				Title: "Survey",
				OnComplete: &OnCompleteSpec{
					Bond: "mol-item",
					// ForEach is missing
				},
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("expected validation error for missing for_each")
	}
	if !strings.Contains(err.Error(), "for_each is required") {
		t.Errorf("expected 'for_each is required' error, got: %v", err)
	}
}

func TestValidate_OnComplete_InvalidForEachPath(t *testing.T) {
	formula := &Formula{
		Formula: "mol-invalid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "survey",
				Title: "Survey",
				OnComplete: &OnCompleteSpec{
					ForEach: "items", // Should start with "output."
					Bond:    "mol-item",
				},
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("expected validation error for invalid for_each path")
	}
	if !strings.Contains(err.Error(), "must start with 'output.'") {
		t.Errorf("expected 'must start with output.' error, got: %v", err)
	}
}

func TestValidate_OnComplete_ParallelAndSequential(t *testing.T) {
	formula := &Formula{
		Formula: "mol-invalid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "survey",
				Title: "Survey",
				OnComplete: &OnCompleteSpec{
					ForEach:    "output.items",
					Bond:       "mol-item",
					Parallel:   true,
					Sequential: true, // Can't have both
				},
			},
		},
	}

	err := formula.Validate()
	if err == nil {
		t.Error("expected validation error for parallel + sequential")
	}
	if !strings.Contains(err.Error(), "cannot set both parallel and sequential") {
		t.Errorf("expected 'cannot set both' error, got: %v", err)
	}
}

func TestValidate_OnComplete_Sequential(t *testing.T) {
	formula := &Formula{
		Formula: "mol-valid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "process-queue",
				Title: "Process queue",
				OnComplete: &OnCompleteSpec{
					ForEach:    "output.branches",
					Bond:       "mol-merge",
					Sequential: true,
				},
			},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for sequential on_complete: %v", err)
	}
}

func TestValidate_OnComplete_InChildren(t *testing.T) {
	formula := &Formula{
		Formula: "mol-valid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{
				ID:    "parent",
				Title: "Parent",
				Children: []*Step{
					{
						ID:    "child",
						Title: "Child",
						OnComplete: &OnCompleteSpec{
							ForEach: "output.items",
							Bond:    "mol-item",
						},
					},
				},
			},
		},
	}

	if err := formula.Validate(); err != nil {
		t.Errorf("Validate failed for on_complete in child: %v", err)
	}
}
