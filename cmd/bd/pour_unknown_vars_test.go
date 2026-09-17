package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/types"
)

func varSubgraph(text string, varDefs map[string]formula.VarDef) *TemplateSubgraph {
	return &TemplateSubgraph{
		Issues:  []*types.Issue{{ID: "t-1", Title: "step", Description: text}},
		VarDefs: varDefs,
	}
}

// conditionVarSubgraph is varSubgraph plus the names a formula's step
// conditions referenced. cook records these before FilterStepsByCondition
// runs, since the step it drops takes its condition with it.
func conditionVarSubgraph(text string, varDefs map[string]formula.VarDef, conditionVars ...string) *TemplateSubgraph {
	sg := varSubgraph(text, varDefs)
	sg.ConditionVars = conditionVars
	return sg
}

func TestCheckPourVarsRejectsUnknownVars(t *testing.T) {
	defaulted := map[string]formula.VarDef{"component": {Default: strPtr("core")}}

	tests := []struct {
		name        string
		subgraph    *TemplateSubgraph
		attached    []*TemplateSubgraph
		vars        map[string]string
		wantErr     bool
		wantInError []string
	}{
		{
			name:     "declared var is accepted",
			subgraph: varSubgraph("build {{component}}", defaulted),
			vars:     map[string]string{"component": "rule"},
		},
		{
			name:     "var referenced only as a handlebar is accepted",
			subgraph: varSubgraph("notes for {{reviewer}}", defaulted),
			vars:     map[string]string{"reviewer": "harry"},
		},
		{
			name:     "var belonging to an attached proto is accepted",
			subgraph: varSubgraph("build {{component}}", defaulted),
			attached: []*TemplateSubgraph{varSubgraph("deploy to {{cluster}}", nil)},
			vars:     map[string]string{"component": "rule", "cluster": "prod"},
		},
		{
			name:        "typo in a defaulted var is rejected",
			subgraph:    varSubgraph("build {{component}}", defaulted),
			vars:        map[string]string{"compnent": "rule"},
			wantErr:     true,
			wantInError: []string{"compnent", "component"},
		},
		{
			name:        "proto with no variables at all",
			subgraph:    varSubgraph("no placeholders here", nil),
			vars:        map[string]string{"anything": "x"},
			wantErr:     true,
			wantInError: []string{"anything", "takes no variables"},
		},
		{
			name:        "unknown vars are reported together and sorted",
			subgraph:    varSubgraph("build {{component}}", defaulted),
			vars:        map[string]string{"zeta": "1", "alpha": "2"},
			wantErr:     true,
			wantInError: []string{"alpha, zeta"},
		},
		{
			// A var used only in a step condition appears in no issue field
			// and need not be declared in [vars], but it decides which steps
			// get poured at all - so it is consumable, and rejecting it would
			// fail a pour the var demonstrably changes.
			name:     "var referenced only by a step condition is accepted",
			subgraph: conditionVarSubgraph("build {{component}}", defaulted, "has_spike"),
			vars:     map[string]string{"component": "rule", "has_spike": "true"},
		},
		{
			// The condition var widens the known set; it does not disable the
			// check. A typo in it is still unusable.
			name:        "typo in a condition var is still rejected",
			subgraph:    conditionVarSubgraph("build {{component}}", defaulted, "has_spike"),
			vars:        map[string]string{"has_spke": "true"},
			wantErr:     true,
			wantInError: []string{"has_spke", "has_spike"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPourVars(tt.subgraph, tt.attached, tt.vars)
			if tt.wantErr && err == nil {
				t.Fatalf("checkPourVars() = nil, want an unknown-variable error")
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("checkPourVars() = %v, want nil", err)
				}
				return
			}
			for _, want := range tt.wantInError {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("checkPourVars() error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

// A missing required var must still be reported as missing rather than being
// reframed by the unknown-var check, because the caller's hint depends on it.
func TestCheckPourVarsReportsMissingBeforeUnknown(t *testing.T) {
	subgraph := varSubgraph("build {{component}}", map[string]formula.VarDef{"component": {}})

	err := checkPourVars(subgraph, nil, map[string]string{"typo": "x"})
	if err == nil {
		t.Fatal("checkPourVars() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "missing required variables") {
		t.Errorf("checkPourVars() error = %q, want the missing-variable error", err)
	}
}

// bd mol wisp takes --var through its own check, which must reject an
// unusable name for the same reason bd mol pour does.
func TestCheckRequiredVarsRejectsUnknownVars(t *testing.T) {
	subgraph := varSubgraph("build {{component}}", map[string]formula.VarDef{"component": {Default: strPtr("core")}})

	if err := checkRequiredVars(subgraph, map[string]string{"component": "rule"}); err != nil {
		t.Fatalf("checkRequiredVars() = %v, want nil for a declared var", err)
	}

	err := checkRequiredVars(subgraph, map[string]string{"compnent": "rule"})
	if err == nil {
		t.Fatal("checkRequiredVars() = nil, want an unknown-variable error")
	}
	if !strings.Contains(err.Error(), "compnent") {
		t.Errorf("checkRequiredVars() error = %q, want it to name the unknown var", err)
	}
}
