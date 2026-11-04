package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/types"
)

// varSubgraph models a proto COOKED FROM A FORMULA: DeclaredVarsKnown is what
// the cook stamps, and it is what licenses the refusal at all, so leaving it
// unset here would silently turn every rejection case below into a no-op.
// dbVarSubgraph is the other shape.
func varSubgraph(text string, varDefs map[string]formula.VarDef) *TemplateSubgraph {
	return &TemplateSubgraph{
		Issues:            []*types.Issue{{ID: "t-1", Title: "step", Description: text}},
		VarDefs:           varDefs,
		DeclaredVarsKnown: true,
	}
}

// dbVarSubgraph models a proto LOADED FROM THE DATABASE - `bd cook --persist`
// output, or any --attach proto. loadTemplateSubgraph cannot recover the
// formula's [vars], so DeclaredVarsKnown stays false.
func dbVarSubgraph(text string) *TemplateSubgraph {
	sg := varSubgraph(text, nil)
	sg.DeclaredVarsKnown = false
	return sg
}

// formulaVarRefSubgraph is varSubgraph plus the names the formula referenced
// that the condition filter erased before the cook: a step condition, or the
// text of a step it dropped. cook records these ahead of the filter.
func formulaVarRefSubgraph(text string, varDefs map[string]formula.VarDef, formulaVarRefs ...string) *TemplateSubgraph {
	sg := varSubgraph(text, varDefs)
	sg.FormulaVarRefs = formulaVarRefs
	return sg
}

// gateVarSubgraph models a cooked proto whose only reference to a var is a GATE
// field: the await_id, or the metadata.repo selector createGateIssue writes for
// a gh:* gate. Neither is a prose field, and neither has to be declared in
// [vars] - but cloneSubgraphInto substitutes both, so both are consumable.
func gateVarSubgraph(awaitType, awaitID, repoSelector string) *TemplateSubgraph {
	gate := &types.Issue{
		ID:        "t-1.gate-deploy",
		Title:     "Gate: " + awaitType,
		AwaitType: awaitType,
		AwaitID:   awaitID,
	}
	if repoSelector != "" {
		gate.Metadata = json.RawMessage(`{"repo":` + strconv.Quote(repoSelector) + `}`)
	}
	return &TemplateSubgraph{
		Issues:            []*types.Issue{{ID: "t-1", Title: "root"}, gate},
		DeclaredVarsKnown: true,
	}
}

// issueVarSubgraph models a cooked proto, with no [vars], whose only reference
// to a var is in one of the non-prose fields cloneSubgraphInto substitutes on
// any issue (GH#5110, GH#5754) - the assignee, a label, or a metadata value -
// which the caller sets on step.
func issueVarSubgraph(step *types.Issue) *TemplateSubgraph {
	step.ID = "t-1.step"
	step.Title = "step"
	return &TemplateSubgraph{
		Issues:            []*types.Issue{{ID: "t-1", Title: "root"}, step},
		DeclaredVarsKnown: true,
	}
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
			subgraph: formulaVarRefSubgraph("build {{component}}", defaulted, "has_spike"),
			vars:     map[string]string{"component": "rule", "has_spike": "true"},
		},
		{
			// The condition var widens the known set; it does not disable the
			// check. A typo in it is still unusable.
			name:        "typo in a condition var is still rejected",
			subgraph:    formulaVarRefSubgraph("build {{component}}", defaulted, "has_spike"),
			vars:        map[string]string{"has_spke": "true"},
			wantErr:     true,
			wantInError: []string{"has_spke", "has_spike"},
		},
		{
			// A handlebar living only inside a step the condition filter
			// dropped is still the formula's, so its name stays valid - a var
			// must not become unusable because another var's VALUE switched its
			// step off.
			name:     "var referenced only by a filtered-out step is accepted",
			subgraph: formulaVarRefSubgraph("build {{component}}", defaulted, "deploy", "deploy_target"),
			vars:     map[string]string{"deploy": "false", "deploy_target": "prod"},
		},
		{
			// cloneSubgraphInto substitutes AwaitID, so a name that appears
			// only in a gate's await_id is consumed by the pour. createGateIssue
			// normally also mirrors the awaitID into the gate Title, which would
			// make this pass through the prose path; the Title here carries no
			// mirror (a hand-edited or pre-format proto) so the case pins the
			// direct AwaitID read instead.
			name:     "var referenced only by a gate await_id is accepted",
			subgraph: gateVarSubgraph("gh:pr", "{{pr_number}}", ""),
			vars:     map[string]string{"pr_number": "5762"},
		},
		{
			// ...and so is metadata.repo on a gh:* gate, which is the arm with
			// no mirror anywhere else on the issue: createGateIssue stores the
			// selector literally and substituteMetadataVars fills it at clone
			// time.
			name:     "var referenced only by a gh gate metadata.repo is accepted",
			subgraph: gateVarSubgraph("gh:run", "12345", "{{gate_repo}}"),
			vars:     map[string]string{"gate_repo": "owner/repo"},
		},
		{
			// The gate fields widen the known set; they do not disable the
			// check. (With no [vars], gate_repo is also REQUIRED - the
			// required-var check reads metadata values too - so it is supplied
			// alongside the typo, or the missing-var error would fire first.)
			name:        "typo against a gate-only proto is still rejected",
			subgraph:    gateVarSubgraph("gh:run", "12345", "{{gate_repo}}"),
			vars:        map[string]string{"gate_repo": "owner/repo", "gate_rpo": "owner/repo"},
			wantErr:     true,
			wantInError: []string{"gate_rpo", "gate_repo"},
		},
		{
			// `repo` on a non-gh gate is ordinary metadata, not a repo
			// selector - but the pour substitutes every metadata string value
			// whatever the gate type (#5758 superseded the gh:*-only rule), so
			// a name used there is consumed. Refusing it would be a closed
			// loop: with no [vars], the required-var check demands the very
			// name this check would refuse, and the proto can never be poured.
			name:     "repo on a non-github gate is substituted like any metadata value",
			subgraph: gateVarSubgraph("human", "sign-off", "{{gate_repo}}"),
			vars:     map[string]string{"gate_repo": "owner/repo"},
		},
		{
			// The same holds on any issue for every non-prose field the pour
			// substitutes: a name used only in the assignee...
			name:     "var referenced only by an assignee is accepted",
			subgraph: issueVarSubgraph(&types.Issue{Assignee: "{{owner}}"}),
			vars:     map[string]string{"owner": "alice"},
		},
		{
			// ...only in a label...
			name:     "var referenced only by a label is accepted",
			subgraph: issueVarSubgraph(&types.Issue{Labels: []string{"area:{{area}}"}}),
			vars:     map[string]string{"area": "parser"},
		},
		{
			// ...or only in a metadata value, at any depth.
			name:     "var referenced only by a nested metadata value is accepted",
			subgraph: issueVarSubgraph(&types.Issue{Metadata: json.RawMessage(`{"owner":{"teams":["{{team}}"]}}`)}),
			vars:     map[string]string{"team": "core"},
		},
		{
			// substituteMetadataVars never rewrites an object KEY, so a name
			// that appears only in one is not consumable and must still be
			// refused. This is the polarity that keeps the read side honest
			// about WHICH metadata strings the pour substitutes.
			name:        "var referenced only by a metadata key is rejected",
			subgraph:    issueVarSubgraph(&types.Issue{Metadata: json.RawMessage(`{"{{team}}":"core"}`)}),
			vars:        map[string]string{"team": "core"},
			wantErr:     true,
			wantInError: []string{"team", "takes no variables"},
		},
		{
			// A DB-loaded proto has no formula behind it, so its [vars]
			// declarations are gone: a declared var that was never written into
			// a substituted field cannot be told apart from a typo, and
			// refusing it would break a pour that works today. (Its text
			// handlebars are all REQUIRED - the nil-VarDefs legacy path in
			// extractRequiredVariables - so component must be supplied; the
			// point of the case is gate_repo, which nothing in the subgraph
			// mentions.)
			name:     "db-loaded proto accepts a name it cannot vouch for",
			subgraph: dbVarSubgraph("build {{component}}"),
			vars:     map[string]string{"component": "core", "gate_repo": "owner/repo"},
		},
		{
			// One DB-loaded --attach proto is enough: the name could belong to
			// it, so no name in the pour can be refused.
			name:     "a db-loaded attachment stands the check down for the whole pour",
			subgraph: varSubgraph("build {{component}}", defaulted),
			attached: []*TemplateSubgraph{dbVarSubgraph("deploy to {{cluster}}")},
			vars:     map[string]string{"cluster": "prod", "anything": "x"},
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

// The hint is only meaningful for a MISSING var, whose name it suggests
// supplying. An unknown-var error has no such name, and the unguarded form this
// PR replaced rendered as `Provide them with: --var =<value>`. All three routes
// share handleVarErrorWithHint, so pinning it here pins every site.
func TestVarErrorHintIsSuppressedWhenThereIsNoNameToSuggest(t *testing.T) {
	unknown := checkPourVars(varSubgraph("no placeholders here", nil), nil, map[string]string{"anything": "x"})
	if unknown == nil {
		t.Fatal("checkPourVars() = nil, want an unknown-variable error to report")
	}

	missing := checkPourVars(varSubgraph("build {{component}}", map[string]formula.VarDef{"component": {}}), nil, nil)
	if missing == nil {
		t.Fatal("checkPourVars() = nil, want a missing-variable error to report")
	}

	tests := []struct {
		name     string
		err      error
		hint     string
		wantHint bool
	}{
		{
			name: "unknown variables carry no hint",
			err:  unknown,
			// What missingVarHint returns when nothing is missing.
			hint:     "",
			wantHint: false,
		},
		{
			name:     "a missing variable still names itself in the hint",
			err:      missing,
			hint:     "component",
			wantHint: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stderr := captureStderr(t, func() {
				if err := handleVarErrorWithHint(tt.err, tt.hint); err == nil {
					t.Error("handleVarErrorWithHint() = nil, want a non-nil exit error")
				}
			})

			if !strings.Contains(stderr, tt.err.Error()) {
				t.Errorf("stderr = %q, want it to report %q", stderr, tt.err)
			}
			if got := strings.Contains(stderr, "Provide them with:"); got != tt.wantHint {
				t.Errorf("stderr = %q, hint present = %v, want %v", stderr, got, tt.wantHint)
			}
			if strings.Contains(stderr, "--var =<value>") {
				t.Errorf("stderr = %q, want no empty-name hint", stderr)
			}
		})
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
