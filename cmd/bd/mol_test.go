package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestParseDistillVar(t *testing.T) {
	tests := []struct {
		name           string
		varFlag        string
		searchableText string
		wantFind       string
		wantVar        string
		wantErr        bool
	}{
		{
			name:           "spawn-style: variable=value",
			varFlag:        "branch=feature-auth",
			searchableText: "Implement feature-auth login flow",
			wantFind:       "feature-auth",
			wantVar:        "branch",
			wantErr:        false,
		},
		{
			name:           "substitution-style: value=variable",
			varFlag:        "feature-auth=branch",
			searchableText: "Implement feature-auth login flow",
			wantFind:       "feature-auth",
			wantVar:        "branch",
			wantErr:        false,
		},
		{
			name:           "spawn-style with version number",
			varFlag:        "version=1.2.3",
			searchableText: "Release version 1.2.3 to production",
			wantFind:       "1.2.3",
			wantVar:        "version",
			wantErr:        false,
		},
		{
			name:           "both found - prefers spawn-style",
			varFlag:        "api=api",
			searchableText: "The api endpoint uses api keys",
			wantFind:       "api",
			wantVar:        "api",
			wantErr:        false,
		},
		{
			name:           "neither found - error",
			varFlag:        "foo=bar",
			searchableText: "Nothing matches here",
			wantFind:       "",
			wantVar:        "",
			wantErr:        true,
		},
		{
			name:           "empty left side - error",
			varFlag:        "=value",
			searchableText: "Some text with value",
			wantFind:       "",
			wantVar:        "",
			wantErr:        true,
		},
		{
			name:           "empty right side - error",
			varFlag:        "value=",
			searchableText: "Some text with value",
			wantFind:       "",
			wantVar:        "",
			wantErr:        true,
		},
		{
			name:           "no equals sign - error",
			varFlag:        "noequals",
			searchableText: "Some text",
			wantFind:       "",
			wantVar:        "",
			wantErr:        true,
		},
		{
			name:           "value with equals sign",
			varFlag:        "env=KEY=VALUE",
			searchableText: "Set KEY=VALUE in config",
			wantFind:       "KEY=VALUE",
			wantVar:        "env",
			wantErr:        false,
		},
		{
			name:           "partial match in longer word - finds it",
			varFlag:        "name=auth",
			searchableText: "authentication module",
			wantFind:       "auth",
			wantVar:        "name",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFind, gotVar, err := parseDistillVar(tt.varFlag, tt.searchableText)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseDistillVar() expected error, got none")
				}
				return
			}

			if err != nil {
				t.Errorf("parseDistillVar() unexpected error: %v", err)
				return
			}

			if gotFind != tt.wantFind {
				t.Errorf("parseDistillVar() find = %q, want %q", gotFind, tt.wantFind)
			}
			if gotVar != tt.wantVar {
				t.Errorf("parseDistillVar() var = %q, want %q", gotVar, tt.wantVar)
			}
		})
	}
}

func TestCollectSubgraphText(t *testing.T) {
	// Create a simple subgraph for testing
	subgraph := &MoleculeSubgraph{
		Issues: []*types.Issue{
			{
				Title:       "Epic: Feature Auth",
				Description: "Implement authentication",
				Design:      "Use OAuth2",
			},
			{
				Title: "Add login endpoint",
				Notes: "See RFC 6749",
			},
		},
	}

	text := collectSubgraphText(subgraph)

	// Verify all fields are included
	expected := []string{
		"Epic: Feature Auth",
		"Implement authentication",
		"Use OAuth2",
		"Add login endpoint",
		"See RFC 6749",
	}

	for _, exp := range expected {
		if !strings.Contains(text, exp) {
			t.Errorf("collectSubgraphText() missing %q", exp)
		}
	}
}

func TestIsProto(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"with template label", []string{"template", "other"}, true},
		{"template only", []string{"template"}, true},
		{"no template label", []string{"bug", "feature"}, false},
		{"empty labels", []string{}, false},
		{"nil labels", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{Labels: tt.labels}
			got := isProto(issue)
			if got != tt.want {
				t.Errorf("isProto() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOperandType(t *testing.T) {
	if got := operandType(true); got != "proto" {
		t.Errorf("operandType(true) = %q, want %q", got, "proto")
	}
	if got := operandType(false); got != "molecule" {
		t.Errorf("operandType(false) = %q, want %q", got, "molecule")
	}
}

func TestMinPriority(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 3, 0},
		{3, 3, 3},
	}
	for _, tt := range tests {
		got := minPriority(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("minPriority(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestBondProtoProto(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create two protos
	protoA := &types.Issue{
		Title:     "Proto A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	protoB := &types.Issue{
		Title:     "Proto B",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}

	if err := store.CreateIssue(ctx, protoA, "test"); err != nil {
		t.Fatalf("Failed to create protoA: %v", err)
	}
	if err := store.CreateIssue(ctx, protoB, "test"); err != nil {
		t.Fatalf("Failed to create protoB: %v", err)
	}

	// Test sequential bond
	result, err := bondProtoProto(ctx, store, protoA, protoB, types.BondTypeSequential, "", "test")
	if err != nil {
		t.Fatalf("bondProtoProto failed: %v", err)
	}

	if result.ResultType != "compound_proto" {
		t.Errorf("ResultType = %q, want %q", result.ResultType, "compound_proto")
	}
	if result.BondType != types.BondTypeSequential {
		t.Errorf("BondType = %q, want %q", result.BondType, types.BondTypeSequential)
	}

	// Verify compound was created
	compound, err := store.GetIssue(ctx, result.ResultID)
	if err != nil {
		t.Fatalf("Failed to get compound: %v", err)
	}
	if !isProto(compound) {
		t.Errorf("Compound should be a proto (have template label), got labels: %v", compound.Labels)
	}
	if compound.Priority != 1 {
		t.Errorf("Compound priority = %d, want %d (min of 1,2)", compound.Priority, 1)
	}

	// Verify dependencies exist (protoA depends on compound via parent-child)
	deps, err := store.GetDependenciesWithMetadata(ctx, protoA.ID)
	if err != nil {
		t.Fatalf("Failed to get deps for protoA: %v", err)
	}
	foundParentChild := false
	for _, dep := range deps {
		if dep.ID == compound.ID && dep.DependencyType == types.DepParentChild {
			foundParentChild = true
		}
	}
	if !foundParentChild {
		t.Error("Expected parent-child dependency from protoA to compound")
	}
}

func TestBondProtoMol(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a proto with a child issue
	proto := &types.Issue{
		Title:     "Proto: {{name}}",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := store.CreateIssue(ctx, proto, "test"); err != nil {
		t.Fatalf("Failed to create proto: %v", err)
	}

	protoChild := &types.Issue{
		Title:     "Step 1 for {{name}}",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{MoleculeLabel},
	}
	if err := store.CreateIssue(ctx, protoChild, "test"); err != nil {
		t.Fatalf("Failed to create proto child: %v", err)
	}

	// Add parent-child dependency
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     protoChild.ID,
		DependsOnID: proto.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Create a molecule (existing epic)
	mol := &types.Issue{
		Title:     "Existing Work",
		Status:    types.StatusInProgress,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, mol, "test"); err != nil {
		t.Fatalf("Failed to create molecule: %v", err)
	}

	// Bond proto to molecule
	vars := map[string]string{"name": "auth-feature"}
	result, err := bondProtoMol(ctx, store, proto, mol, types.BondTypeSequential, vars, "", "test")
	if err != nil {
		t.Fatalf("bondProtoMol failed: %v", err)
	}

	if result.ResultType != "compound_molecule" {
		t.Errorf("ResultType = %q, want %q", result.ResultType, "compound_molecule")
	}
	if result.Spawned != 2 {
		t.Errorf("Spawned = %d, want 2", result.Spawned)
	}
	if result.ResultID != mol.ID {
		t.Errorf("ResultID = %q, want %q (original molecule)", result.ResultID, mol.ID)
	}
}

func TestBondMolMol(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create two molecules
	molA := &types.Issue{
		Title:     "Molecule A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	molB := &types.Issue{
		Title:     "Molecule B",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}

	if err := store.CreateIssue(ctx, molA, "test"); err != nil {
		t.Fatalf("Failed to create molA: %v", err)
	}
	if err := store.CreateIssue(ctx, molB, "test"); err != nil {
		t.Fatalf("Failed to create molB: %v", err)
	}

	// Test sequential bond
	result, err := bondMolMol(ctx, store, molA, molB, types.BondTypeSequential, "test")
	if err != nil {
		t.Fatalf("bondMolMol failed: %v", err)
	}

	if result.ResultType != "compound_molecule" {
		t.Errorf("ResultType = %q, want %q", result.ResultType, "compound_molecule")
	}
	if result.ResultID != molA.ID {
		t.Errorf("ResultID = %q, want %q", result.ResultID, molA.ID)
	}

	// Verify dependency: B blocks on A
	deps, err := store.GetDependenciesWithMetadata(ctx, molB.ID)
	if err != nil {
		t.Fatalf("Failed to get deps for molB: %v", err)
	}
	foundBlocks := false
	for _, dep := range deps {
		if dep.ID == molA.ID && dep.DependencyType == types.DepBlocks {
			foundBlocks = true
		}
	}
	if !foundBlocks {
		t.Error("Expected blocks dependency from molB to molA for sequential bond")
	}

	// Test parallel bond (create new molecules)
	molC := &types.Issue{
		Title:     "Molecule C",
		Status:    types.StatusOpen,
		IssueType: types.TypeEpic,
	}
	molD := &types.Issue{
		Title:     "Molecule D",
		Status:    types.StatusOpen,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, molC, "test"); err != nil {
		t.Fatalf("Failed to create molC: %v", err)
	}
	if err := store.CreateIssue(ctx, molD, "test"); err != nil {
		t.Fatalf("Failed to create molD: %v", err)
	}

	result2, err := bondMolMol(ctx, store, molC, molD, types.BondTypeParallel, "test")
	if err != nil {
		t.Fatalf("bondMolMol parallel failed: %v", err)
	}

	// Verify parent-child dependency for parallel
	deps2, err := store.GetDependenciesWithMetadata(ctx, molD.ID)
	if err != nil {
		t.Fatalf("Failed to get deps for molD: %v", err)
	}
	foundParentChild := false
	for _, dep := range deps2 {
		if dep.ID == molC.ID && dep.DependencyType == types.DepParentChild {
			foundParentChild = true
		}
	}
	if !foundParentChild {
		t.Errorf("Expected parent-child dependency for parallel bond, result: %+v", result2)
	}
}

func TestSquashMolecule(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a molecule (root issue)
	root := &types.Issue{
		Title:     "Test Molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	// Create ephemeral children
	child1 := &types.Issue{
		Title:       "Step 1: Design",
		Description: "Design the architecture",
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		Wisp:        true,
		CloseReason: "Completed design",
	}
	child2 := &types.Issue{
		Title:       "Step 2: Implement",
		Description: "Build the feature",
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		Wisp:        true,
		CloseReason: "Code merged",
	}

	if err := s.CreateIssue(ctx, child1, "test"); err != nil {
		t.Fatalf("Failed to create child1: %v", err)
	}
	if err := s.CreateIssue(ctx, child2, "test"); err != nil {
		t.Fatalf("Failed to create child2: %v", err)
	}

	// Add parent-child dependencies
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child1.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add child1 dependency: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child2.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add child2 dependency: %v", err)
	}

	// Test squash with keep-children
	children := []*types.Issue{child1, child2}
	result, err := squashMolecule(ctx, s, root, children, true, "", "test")
	if err != nil {
		t.Fatalf("squashMolecule failed: %v", err)
	}

	if result.SquashedCount != 2 {
		t.Errorf("SquashedCount = %d, want 2", result.SquashedCount)
	}
	if result.DeletedCount != 0 {
		t.Errorf("DeletedCount = %d, want 0 (keep-children)", result.DeletedCount)
	}
	if !result.KeptChildren {
		t.Error("KeptChildren should be true")
	}

	// Verify digest was created
	digest, err := s.GetIssue(ctx, result.DigestID)
	if err != nil {
		t.Fatalf("Failed to get digest: %v", err)
	}
	if digest.Wisp {
		t.Error("Digest should NOT be ephemeral")
	}
	if digest.Status != types.StatusClosed {
		t.Errorf("Digest status = %v, want closed", digest.Status)
	}
	if !strings.Contains(digest.Description, "Step 1: Design") {
		t.Error("Digest should contain child titles")
	}
	if !strings.Contains(digest.Description, "Completed design") {
		t.Error("Digest should contain close reasons")
	}

	// Children should still exist
	c1, err := s.GetIssue(ctx, child1.ID)
	if err != nil || c1 == nil {
		t.Error("Child1 should still exist with keep-children")
	}
}

func TestSquashMoleculeWithDelete(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a molecule with ephemeral children
	root := &types.Issue{
		Title:     "Delete Test Molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	child := &types.Issue{
		Title:     "Wisp Step",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		Wisp:      true,
	}
	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Squash with delete (keepChildren=false)
	result, err := squashMolecule(ctx, s, root, []*types.Issue{child}, false, "", "test")
	if err != nil {
		t.Fatalf("squashMolecule failed: %v", err)
	}

	if result.DeletedCount != 1 {
		t.Errorf("DeletedCount = %d, want 1", result.DeletedCount)
	}

	// Child should be deleted
	c, err := s.GetIssue(ctx, child.ID)
	if err == nil && c != nil {
		t.Error("Child should have been deleted")
	}

	// Digest should exist
	digest, err := s.GetIssue(ctx, result.DigestID)
	if err != nil || digest == nil {
		t.Error("Digest should exist after squash")
	}
}

func TestGenerateDigest(t *testing.T) {
	root := &types.Issue{
		Title: "Test Molecule",
	}
	children := []*types.Issue{
		{
			Title:       "Step 1",
			Description: "First step description",
			Status:      types.StatusClosed,
			CloseReason: "Done",
		},
		{
			Title:       "Step 2",
			Description: "Second step description that is longer",
			Status:      types.StatusInProgress,
		},
	}

	digest := generateDigest(root, children)

	// Verify structure
	if !strings.Contains(digest, "## Molecule Execution Summary") {
		t.Error("Digest should have summary header")
	}
	if !strings.Contains(digest, "Test Molecule") {
		t.Error("Digest should contain molecule title")
	}
	if !strings.Contains(digest, "**Steps**: 2") {
		t.Error("Digest should show step count")
	}
	if !strings.Contains(digest, "**Completed**: 1/2") {
		t.Error("Digest should show completion stats")
	}
	if !strings.Contains(digest, "**In Progress**: 1") {
		t.Error("Digest should show in-progress count")
	}
	if !strings.Contains(digest, "Step 1") {
		t.Error("Digest should list step titles")
	}
	if !strings.Contains(digest, "*Outcome: Done*") {
		t.Error("Digest should include close reasons")
	}
}

// TestSquashMoleculeWithAgentSummary verifies that agent-provided summaries are used
func TestSquashMoleculeWithAgentSummary(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a molecule with ephemeral child
	root := &types.Issue{
		Title:     "Agent Summary Test",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	child := &types.Issue{
		Title:       "Wisp Step",
		Description: "This should NOT appear in digest",
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		Wisp:        true,
		CloseReason: "Done",
	}
	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Squash with agent-provided summary
	agentSummary := "## AI-Generated Summary\n\nThe agent completed the task successfully."
	result, err := squashMolecule(ctx, s, root, []*types.Issue{child}, true, agentSummary, "test")
	if err != nil {
		t.Fatalf("squashMolecule failed: %v", err)
	}

	// Verify digest uses agent summary, not auto-generated content
	digest, err := s.GetIssue(ctx, result.DigestID)
	if err != nil {
		t.Fatalf("Failed to get digest: %v", err)
	}

	if digest.Description != agentSummary {
		t.Errorf("Digest should use agent summary.\nGot: %s\nWant: %s", digest.Description, agentSummary)
	}

	// Verify auto-generated content is NOT present
	if strings.Contains(digest.Description, "Wisp Step") {
		t.Error("Digest should NOT contain auto-generated content when agent summary provided")
	}
}

// =============================================================================
// Spawn --attach Tests (bd-f7p1)
// =============================================================================

// TestSpawnWithBasicAttach tests spawning a proto with one --attach flag
func TestSpawnWithBasicAttach(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create primary proto with a child
	primaryProto := &types.Issue{
		Title:     "Primary: {{feature}}",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, primaryProto, "test"); err != nil {
		t.Fatalf("Failed to create primary proto: %v", err)
	}

	primaryChild := &types.Issue{
		Title:     "Step 1 for {{feature}}",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, primaryChild, "test"); err != nil {
		t.Fatalf("Failed to create primary child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     primaryChild.ID,
		DependsOnID: primaryProto.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add primary child dependency: %v", err)
	}

	// Create attachment proto with a child
	attachProto := &types.Issue{
		Title:     "Attachment: {{feature}} docs",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, attachProto, "test"); err != nil {
		t.Fatalf("Failed to create attach proto: %v", err)
	}

	attachChild := &types.Issue{
		Title:     "Write docs for {{feature}}",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, attachChild, "test"); err != nil {
		t.Fatalf("Failed to create attach child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     attachChild.ID,
		DependsOnID: attachProto.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add attach child dependency: %v", err)
	}

	// Spawn primary proto
	primarySubgraph, err := loadTemplateSubgraph(ctx, s, primaryProto.ID)
	if err != nil {
		t.Fatalf("Failed to load primary subgraph: %v", err)
	}

	vars := map[string]string{"feature": "auth"}
	spawnResult, err := spawnMolecule(ctx, s, primarySubgraph, vars, "", "test", true)
	if err != nil {
		t.Fatalf("Failed to spawn primary: %v", err)
	}

	if spawnResult.Created != 2 {
		t.Errorf("Spawn created = %d, want 2", spawnResult.Created)
	}

	// Get the spawned molecule
	spawnedMol, err := s.GetIssue(ctx, spawnResult.NewEpicID)
	if err != nil {
		t.Fatalf("Failed to get spawned molecule: %v", err)
	}

	// Attach the second proto (simulating --attach flag behavior)
	bondResult, err := bondProtoMol(ctx, s, attachProto, spawnedMol, types.BondTypeSequential, vars, "", "test")
	if err != nil {
		t.Fatalf("Failed to bond attachment: %v", err)
	}

	if bondResult.Spawned != 2 {
		t.Errorf("Bond spawned = %d, want 2", bondResult.Spawned)
	}
	if bondResult.ResultType != "compound_molecule" {
		t.Errorf("ResultType = %q, want %q", bondResult.ResultType, "compound_molecule")
	}

	// Verify the spawned attachment root has dependency on the primary molecule
	attachedRootID := bondResult.IDMapping[attachProto.ID]
	deps, err := s.GetDependenciesWithMetadata(ctx, attachedRootID)
	if err != nil {
		t.Fatalf("Failed to get deps: %v", err)
	}

	foundBlocks := false
	for _, dep := range deps {
		if dep.ID == spawnedMol.ID && dep.DependencyType == types.DepBlocks {
			foundBlocks = true
		}
	}
	if !foundBlocks {
		t.Error("Expected blocks dependency from attached proto to spawned molecule for sequential bond")
	}

	// Verify variable substitution worked in attached issues
	attachedRoot, err := s.GetIssue(ctx, attachedRootID)
	if err != nil {
		t.Fatalf("Failed to get attached root: %v", err)
	}
	if !strings.Contains(attachedRoot.Title, "auth") {
		t.Errorf("Attached root title %q should contain 'auth' from variable substitution", attachedRoot.Title)
	}
}

// TestSpawnWithMultipleAttachments tests spawning with --attach A --attach B
func TestSpawnWithMultipleAttachments(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create primary proto
	primaryProto := &types.Issue{
		Title:     "Primary Feature",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, primaryProto, "test"); err != nil {
		t.Fatalf("Failed to create primary proto: %v", err)
	}

	// Create first attachment proto
	attachA := &types.Issue{
		Title:     "Attachment A: Testing",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, attachA, "test"); err != nil {
		t.Fatalf("Failed to create attachA: %v", err)
	}

	// Create second attachment proto
	attachB := &types.Issue{
		Title:     "Attachment B: Documentation",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, attachB, "test"); err != nil {
		t.Fatalf("Failed to create attachB: %v", err)
	}

	// Spawn primary
	primarySubgraph, err := loadTemplateSubgraph(ctx, s, primaryProto.ID)
	if err != nil {
		t.Fatalf("Failed to load primary subgraph: %v", err)
	}

	spawnResult, err := spawnMolecule(ctx, s, primarySubgraph, nil, "", "test", true)
	if err != nil {
		t.Fatalf("Failed to spawn primary: %v", err)
	}

	spawnedMol, err := s.GetIssue(ctx, spawnResult.NewEpicID)
	if err != nil {
		t.Fatalf("Failed to get spawned molecule: %v", err)
	}

	// Attach both protos (simulating --attach A --attach B)
	bondResultA, err := bondProtoMol(ctx, s, attachA, spawnedMol, types.BondTypeSequential, nil, "", "test")
	if err != nil {
		t.Fatalf("Failed to bond attachA: %v", err)
	}

	bondResultB, err := bondProtoMol(ctx, s, attachB, spawnedMol, types.BondTypeSequential, nil, "", "test")
	if err != nil {
		t.Fatalf("Failed to bond attachB: %v", err)
	}

	// Both should have spawned their protos
	if bondResultA.Spawned != 1 {
		t.Errorf("bondResultA.Spawned = %d, want 1", bondResultA.Spawned)
	}
	if bondResultB.Spawned != 1 {
		t.Errorf("bondResultB.Spawned = %d, want 1", bondResultB.Spawned)
	}

	// Both should depend on the primary molecule
	attachedAID := bondResultA.IDMapping[attachA.ID]
	attachedBID := bondResultB.IDMapping[attachB.ID]

	depsA, err := s.GetDependenciesWithMetadata(ctx, attachedAID)
	if err != nil {
		t.Fatalf("Failed to get deps for A: %v", err)
	}
	depsB, err := s.GetDependenciesWithMetadata(ctx, attachedBID)
	if err != nil {
		t.Fatalf("Failed to get deps for B: %v", err)
	}

	foundABlocks := false
	for _, dep := range depsA {
		if dep.ID == spawnedMol.ID && dep.DependencyType == types.DepBlocks {
			foundABlocks = true
		}
	}
	foundBBlocks := false
	for _, dep := range depsB {
		if dep.ID == spawnedMol.ID && dep.DependencyType == types.DepBlocks {
			foundBBlocks = true
		}
	}

	if !foundABlocks {
		t.Error("Expected A to block on spawned molecule")
	}
	if !foundBBlocks {
		t.Error("Expected B to block on spawned molecule")
	}
}

// TestSpawnAttachTypes verifies sequential vs parallel bonding behavior
func TestSpawnAttachTypes(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create primary proto
	primaryProto := &types.Issue{
		Title:     "Primary",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, primaryProto, "test"); err != nil {
		t.Fatalf("Failed to create primary: %v", err)
	}

	// Create attachment proto
	attachProto := &types.Issue{
		Title:     "Attachment",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, attachProto, "test"); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	tests := []struct {
		name       string
		bondType   string
		expectType types.DependencyType
	}{
		{"sequential uses blocks", types.BondTypeSequential, types.DepBlocks},
		{"parallel uses parent-child", types.BondTypeParallel, types.DepParentChild},
		{"conditional uses conditional-blocks", types.BondTypeConditional, types.DepConditionalBlocks},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Spawn fresh primary for each test
			primarySubgraph, err := loadTemplateSubgraph(ctx, s, primaryProto.ID)
			if err != nil {
				t.Fatalf("Failed to load primary subgraph: %v", err)
			}

			spawnResult, err := spawnMolecule(ctx, s, primarySubgraph, nil, "", "test", true)
			if err != nil {
				t.Fatalf("Failed to spawn primary: %v", err)
			}

			spawnedMol, err := s.GetIssue(ctx, spawnResult.NewEpicID)
			if err != nil {
				t.Fatalf("Failed to get spawned molecule: %v", err)
			}

			// Bond with specified type
			bondResult, err := bondProtoMol(ctx, s, attachProto, spawnedMol, tt.bondType, nil, "", "test")
			if err != nil {
				t.Fatalf("Failed to bond: %v", err)
			}

			// Check dependency type
			attachedID := bondResult.IDMapping[attachProto.ID]
			deps, err := s.GetDependenciesWithMetadata(ctx, attachedID)
			if err != nil {
				t.Fatalf("Failed to get deps: %v", err)
			}

			foundExpected := false
			for _, dep := range deps {
				if dep.ID == spawnedMol.ID && dep.DependencyType == tt.expectType {
					foundExpected = true
				}
			}

			if !foundExpected {
				t.Errorf("Expected %s dependency from attached to spawned molecule", tt.expectType)
			}
		})
	}
}

// TestSpawnAttachNonProtoError tests that attaching a non-proto fails validation
func TestSpawnAttachNonProtoError(t *testing.T) {
	// The isProto function is tested separately in TestIsProto
	// This test verifies the validation logic that would be used in runMolSpawn

	// Create a non-proto issue (no template label)
	issue := &types.Issue{
		Title:  "Not a proto",
		Status: types.StatusOpen,
		Labels: []string{"bug"}, // Not MoleculeLabel
	}

	if isProto(issue) {
		t.Error("isProto should return false for issue without template label")
	}

	// Issue with template label should pass
	protoIssue := &types.Issue{
		Title:  "A proto",
		Status: types.StatusOpen,
		Labels: []string{MoleculeLabel},
	}

	if !isProto(protoIssue) {
		t.Error("isProto should return true for issue with template label")
	}
}

// TestSpawnVariableAggregation tests that variables from primary + attachments are combined
func TestSpawnVariableAggregation(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create primary proto with one variable
	primaryProto := &types.Issue{
		Title:       "Feature: {{feature_name}}",
		Description: "Implement the {{feature_name}} feature",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		Labels:      []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, primaryProto, "test"); err != nil {
		t.Fatalf("Failed to create primary: %v", err)
	}

	// Create attachment proto with a different variable
	attachProto := &types.Issue{
		Title:       "Docs for {{doc_version}}",
		Description: "Document version {{doc_version}}",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeEpic,
		Labels:      []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, attachProto, "test"); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Load subgraphs and extract variables
	primarySubgraph, err := loadTemplateSubgraph(ctx, s, primaryProto.ID)
	if err != nil {
		t.Fatalf("Failed to load primary subgraph: %v", err)
	}
	attachSubgraph, err := loadTemplateSubgraph(ctx, s, attachProto.ID)
	if err != nil {
		t.Fatalf("Failed to load attach subgraph: %v", err)
	}

	// Aggregate variables (simulating runMolSpawn logic)
	requiredVars := extractAllVariables(primarySubgraph)
	attachVars := extractAllVariables(attachSubgraph)
	for _, v := range attachVars {
		found := false
		for _, rv := range requiredVars {
			if rv == v {
				found = true
				break
			}
		}
		if !found {
			requiredVars = append(requiredVars, v)
		}
	}

	// Should have both variables
	if len(requiredVars) != 2 {
		t.Errorf("Expected 2 required vars, got %d: %v", len(requiredVars), requiredVars)
	}

	hasFeatureName := false
	hasDocVersion := false
	for _, v := range requiredVars {
		if v == "feature_name" {
			hasFeatureName = true
		}
		if v == "doc_version" {
			hasDocVersion = true
		}
	}

	if !hasFeatureName {
		t.Error("Missing feature_name variable from primary proto")
	}
	if !hasDocVersion {
		t.Error("Missing doc_version variable from attachment proto")
	}

	// Provide both variables and verify substitution
	vars := map[string]string{
		"feature_name": "authentication",
		"doc_version":  "2.0",
	}

	// Spawn primary with variables
	spawnResult, err := spawnMolecule(ctx, s, primarySubgraph, vars, "", "test", true)
	if err != nil {
		t.Fatalf("Failed to spawn primary: %v", err)
	}

	// Verify primary variable was substituted
	spawnedPrimary, err := s.GetIssue(ctx, spawnResult.NewEpicID)
	if err != nil {
		t.Fatalf("Failed to get spawned primary: %v", err)
	}
	if !strings.Contains(spawnedPrimary.Title, "authentication") {
		t.Errorf("Primary title %q should contain 'authentication'", spawnedPrimary.Title)
	}

	// Bond attachment with same variables
	spawnedMol, _ := s.GetIssue(ctx, spawnResult.NewEpicID)
	bondResult, err := bondProtoMol(ctx, s, attachProto, spawnedMol, types.BondTypeSequential, vars, "", "test")
	if err != nil {
		t.Fatalf("Failed to bond: %v", err)
	}

	// Verify attachment variable was substituted
	attachedID := bondResult.IDMapping[attachProto.ID]
	attachedIssue, err := s.GetIssue(ctx, attachedID)
	if err != nil {
		t.Fatalf("Failed to get attached issue: %v", err)
	}
	if !strings.Contains(attachedIssue.Title, "2.0") {
		t.Errorf("Attached title %q should contain '2.0'", attachedIssue.Title)
	}
}

// TestSpawnAttachDryRunOutput tests that dry-run includes attachment info
// This is a lighter test since dry-run is mainly a CLI output concern
func TestSpawnAttachDryRunOutput(t *testing.T) {
	// The dry-run logic in runMolSpawn outputs attachment info when len(attachments) > 0
	// We verify the data structures that would be used in dry-run

	type attachmentInfo struct {
		id       string
		title    string
		subgraph *MoleculeSubgraph
	}

	// Simulate the attachment info collection
	attachments := []attachmentInfo{
		{id: "test-1", title: "Attachment 1", subgraph: &MoleculeSubgraph{
			Issues: []*types.Issue{{Title: "Issue A"}, {Title: "Issue B"}},
		}},
		{id: "test-2", title: "Attachment 2", subgraph: &MoleculeSubgraph{
			Issues: []*types.Issue{{Title: "Issue C"}},
		}},
	}

	// Verify attachment count calculation (used in dry-run output)
	totalAttachmentIssues := 0
	for _, attach := range attachments {
		totalAttachmentIssues += len(attach.subgraph.Issues)
	}

	if totalAttachmentIssues != 3 {
		t.Errorf("Expected 3 total attachment issues, got %d", totalAttachmentIssues)
	}

	// Verify bond type would be included (sequential is default)
	attachType := types.BondTypeSequential
	if attachType != "sequential" {
		t.Errorf("Expected default attach type 'sequential', got %q", attachType)
	}
}

// TestSquashWispToPermanent tests cross-store squash: wisp → permanent digest (bd-kwjh.4)
func TestSquashWispToPermanent(t *testing.T) {
	ctx := context.Background()

	// Create separate wisp and permanent stores
	wispPath := t.TempDir() + "/wisp.db"
	permPath := t.TempDir() + "/permanent.db"

	wispStore, err := sqlite.New(ctx, wispPath)
	if err != nil {
		t.Fatalf("Failed to create wisp store: %v", err)
	}
	defer wispStore.Close()
	if err := wispStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set wisp config: %v", err)
	}

	permStore, err := sqlite.New(ctx, permPath)
	if err != nil {
		t.Fatalf("Failed to create permanent store: %v", err)
	}
	defer permStore.Close()
	if err := permStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set permanent config: %v", err)
	}

	// Create a wisp molecule in wisp storage
	wispRoot := &types.Issue{
		Title:     "Deacon Patrol Cycle",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Wisp:      true,
	}
	if err := wispStore.CreateIssue(ctx, wispRoot, "test"); err != nil {
		t.Fatalf("Failed to create wisp root: %v", err)
	}

	wispChild1 := &types.Issue{
		Title:       "Check witnesses",
		Description: "Verified 3 witnesses healthy",
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		Wisp:        true,
		CloseReason: "All healthy",
	}
	wispChild2 := &types.Issue{
		Title:       "Process mail queue",
		Description: "Processed 5 mail items",
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		Wisp:        true,
		CloseReason: "Mail delivered",
	}

	if err := wispStore.CreateIssue(ctx, wispChild1, "test"); err != nil {
		t.Fatalf("Failed to create wisp child1: %v", err)
	}
	if err := wispStore.CreateIssue(ctx, wispChild2, "test"); err != nil {
		t.Fatalf("Failed to create wisp child2: %v", err)
	}

	// Add parent-child dependencies
	if err := wispStore.AddDependency(ctx, &types.Dependency{
		IssueID:     wispChild1.ID,
		DependsOnID: wispRoot.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add child1 dependency: %v", err)
	}
	if err := wispStore.AddDependency(ctx, &types.Dependency{
		IssueID:     wispChild2.ID,
		DependsOnID: wispRoot.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add child2 dependency: %v", err)
	}

	// Load the subgraph
	subgraph, err := loadTemplateSubgraph(ctx, wispStore, wispRoot.ID)
	if err != nil {
		t.Fatalf("Failed to load wisp subgraph: %v", err)
	}

	// Verify subgraph loaded correctly
	if len(subgraph.Issues) != 3 {
		t.Fatalf("Expected 3 issues in subgraph, got %d", len(subgraph.Issues))
	}

	// Perform cross-store squash
	result, err := squashWispToPermanent(ctx, wispStore, permStore, subgraph, false, "", "test")
	if err != nil {
		t.Fatalf("squashWispToPermanent failed: %v", err)
	}

	// Verify result
	if result.SquashedCount != 3 {
		t.Errorf("SquashedCount = %d, want 3", result.SquashedCount)
	}
	if !result.WispSquash {
		t.Error("WispSquash should be true")
	}
	if result.DigestID == "" {
		t.Error("DigestID should not be empty")
	}
	if result.DeletedCount != 3 {
		t.Errorf("DeletedCount = %d, want 3", result.DeletedCount)
	}

	// Verify digest was created in permanent storage
	digest, err := permStore.GetIssue(ctx, result.DigestID)
	if err != nil {
		t.Fatalf("Failed to get digest from permanent store: %v", err)
	}
	if digest.Wisp {
		t.Error("Digest should NOT be a wisp")
	}
	if digest.Status != types.StatusClosed {
		t.Errorf("Digest status = %v, want closed", digest.Status)
	}
	if !strings.Contains(digest.Title, "Deacon Patrol Cycle") {
		t.Errorf("Digest title %q should contain original molecule title", digest.Title)
	}
	if !strings.Contains(digest.Description, "Check witnesses") {
		t.Error("Digest description should contain child titles")
	}

	// Verify wisps were deleted from wisp storage
	// Note: GetIssue returns (nil, nil) when issue doesn't exist
	rootIssue, err := wispStore.GetIssue(ctx, wispRoot.ID)
	if err != nil {
		t.Errorf("Unexpected error checking root deletion: %v", err)
	}
	if rootIssue != nil {
		t.Error("Wisp root should have been deleted")
	}
	child1Issue, err := wispStore.GetIssue(ctx, wispChild1.ID)
	if err != nil {
		t.Errorf("Unexpected error checking child1 deletion: %v", err)
	}
	if child1Issue != nil {
		t.Error("Wisp child1 should have been deleted")
	}
	child2Issue, err := wispStore.GetIssue(ctx, wispChild2.ID)
	if err != nil {
		t.Errorf("Unexpected error checking child2 deletion: %v", err)
	}
	if child2Issue != nil {
		t.Error("Wisp child2 should have been deleted")
	}
}

// TestSquashWispToPermanentWithSummary tests that agent summaries override auto-generation
func TestSquashWispToPermanentWithSummary(t *testing.T) {
	ctx := context.Background()

	wispPath := t.TempDir() + "/wisp.db"
	permPath := t.TempDir() + "/permanent.db"

	wispStore, err := sqlite.New(ctx, wispPath)
	if err != nil {
		t.Fatalf("Failed to create wisp store: %v", err)
	}
	defer wispStore.Close()
	if err := wispStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set wisp config: %v", err)
	}

	permStore, err := sqlite.New(ctx, permPath)
	if err != nil {
		t.Fatalf("Failed to create permanent store: %v", err)
	}
	defer permStore.Close()
	if err := permStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set permanent config: %v", err)
	}

	// Create a simple wisp molecule
	wispRoot := &types.Issue{
		Title:     "Patrol Cycle",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeEpic,
		Wisp:      true,
	}
	if err := wispStore.CreateIssue(ctx, wispRoot, "test"); err != nil {
		t.Fatalf("Failed to create wisp root: %v", err)
	}

	subgraph := &MoleculeSubgraph{
		Root:   wispRoot,
		Issues: []*types.Issue{wispRoot},
	}

	// Squash with agent-provided summary
	agentSummary := "## AI-Generated Patrol Summary\n\nAll systems healthy. No issues found."
	result, err := squashWispToPermanent(ctx, wispStore, permStore, subgraph, true, agentSummary, "test")
	if err != nil {
		t.Fatalf("squashWispToPermanent failed: %v", err)
	}

	// Verify digest uses agent summary
	digest, err := permStore.GetIssue(ctx, result.DigestID)
	if err != nil {
		t.Fatalf("Failed to get digest: %v", err)
	}
	if digest.Description != agentSummary {
		t.Errorf("Digest should use agent summary.\nGot: %s\nWant: %s", digest.Description, agentSummary)
	}
}

// TestSquashWispToPermanentKeepChildren tests --keep-children flag
func TestSquashWispToPermanentKeepChildren(t *testing.T) {
	ctx := context.Background()

	wispPath := t.TempDir() + "/wisp.db"
	permPath := t.TempDir() + "/permanent.db"

	wispStore, err := sqlite.New(ctx, wispPath)
	if err != nil {
		t.Fatalf("Failed to create wisp store: %v", err)
	}
	defer wispStore.Close()
	if err := wispStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set wisp config: %v", err)
	}

	permStore, err := sqlite.New(ctx, permPath)
	if err != nil {
		t.Fatalf("Failed to create permanent store: %v", err)
	}
	defer permStore.Close()
	if err := permStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set permanent config: %v", err)
	}

	// Create a wisp molecule
	wispRoot := &types.Issue{
		Title:     "Test Molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Wisp:      true,
	}
	if err := wispStore.CreateIssue(ctx, wispRoot, "test"); err != nil {
		t.Fatalf("Failed to create wisp root: %v", err)
	}

	subgraph := &MoleculeSubgraph{
		Root:   wispRoot,
		Issues: []*types.Issue{wispRoot},
	}

	// Squash with keepChildren=true
	result, err := squashWispToPermanent(ctx, wispStore, permStore, subgraph, true, "", "test")
	if err != nil {
		t.Fatalf("squashWispToPermanent failed: %v", err)
	}

	// Verify no deletion
	if result.DeletedCount != 0 {
		t.Errorf("DeletedCount = %d, want 0 (keep-children)", result.DeletedCount)
	}
	if !result.KeptChildren {
		t.Error("KeptChildren should be true")
	}

	// Wisp should still exist
	_, err = wispStore.GetIssue(ctx, wispRoot.ID)
	if err != nil {
		t.Error("Wisp should still exist with --keep-children")
	}

	// Digest should still be created
	_, err = permStore.GetIssue(ctx, result.DigestID)
	if err != nil {
		t.Error("Digest should be created even with --keep-children")
	}
}

// TestWispFilteringFromExport verifies that wisp issues are filtered
// from JSONL export (bd-687g). Wisp issues should only exist in SQLite,
// not in issues.jsonl, to prevent "zombie" resurrection after mol squash.
func TestWispFilteringFromExport(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a mix of wisp and non-wisp issues
	normalIssue := &types.Issue{
		Title:     "Normal Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Wisp:      false,
	}
	wispIssue := &types.Issue{
		Title:     "Wisp Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Wisp:      true,
	}

	if err := s.CreateIssue(ctx, normalIssue, "test"); err != nil {
		t.Fatalf("Failed to create normal issue: %v", err)
	}
	if err := s.CreateIssue(ctx, wispIssue, "test"); err != nil {
		t.Fatalf("Failed to create wisp issue: %v", err)
	}

	// Get all issues from DB - should include both
	allIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}
	if len(allIssues) != 2 {
		t.Fatalf("Expected 2 issues in DB, got %d", len(allIssues))
	}

	// Filter wisp issues (simulating export behavior)
	exportableIssues := make([]*types.Issue, 0)
	for _, issue := range allIssues {
		if !issue.Wisp {
			exportableIssues = append(exportableIssues, issue)
		}
	}

	// Should only have the non-wisp issue
	if len(exportableIssues) != 1 {
		t.Errorf("Expected 1 exportable issue, got %d", len(exportableIssues))
	}
	if exportableIssues[0].ID != normalIssue.ID {
		t.Errorf("Expected normal issue %s, got %s", normalIssue.ID, exportableIssues[0].ID)
	}
}

// =============================================================================
// Mol Current Tests (bd-nurq)
// =============================================================================

// TestGetMoleculeProgress tests loading a molecule and computing progress
func TestGetMoleculeProgress(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a molecule (epic with template label)
	root := &types.Issue{
		Title:     "Test Molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{BeadsTemplateLabel},
		Assignee:  "test-agent",
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	// Create steps with different statuses
	step1 := &types.Issue{
		Title:     "Step 1: Done",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	step2 := &types.Issue{
		Title:     "Step 2: Current",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	step3 := &types.Issue{
		Title:     "Step 3: Pending",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, step := range []*types.Issue{step1, step2, step3} {
		if err := s.CreateIssue(ctx, step, "test"); err != nil {
			t.Fatalf("Failed to create step: %v", err)
		}
		// Add parent-child dependency
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     step.ID,
			DependsOnID: root.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}
	}

	// Add blocking dependency: step3 blocks on step2
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     step3.ID,
		DependsOnID: step2.ID,
		Type:        types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("Failed to add blocking dependency: %v", err)
	}

	// Get progress
	progress, err := getMoleculeProgress(ctx, s, root.ID)
	if err != nil {
		t.Fatalf("getMoleculeProgress failed: %v", err)
	}

	// Verify progress
	if progress.MoleculeID != root.ID {
		t.Errorf("MoleculeID = %q, want %q", progress.MoleculeID, root.ID)
	}
	if progress.MoleculeTitle != root.Title {
		t.Errorf("MoleculeTitle = %q, want %q", progress.MoleculeTitle, root.Title)
	}
	if progress.Assignee != "test-agent" {
		t.Errorf("Assignee = %q, want %q", progress.Assignee, "test-agent")
	}
	if progress.Total != 3 {
		t.Errorf("Total = %d, want 3", progress.Total)
	}
	if progress.Completed != 1 {
		t.Errorf("Completed = %d, want 1", progress.Completed)
	}
	if progress.CurrentStep == nil {
		t.Error("CurrentStep should not be nil")
	} else if progress.CurrentStep.ID != step2.ID {
		t.Errorf("CurrentStep.ID = %q, want %q", progress.CurrentStep.ID, step2.ID)
	}
	if len(progress.Steps) != 3 {
		t.Errorf("Steps count = %d, want 3", len(progress.Steps))
	}
}

// TestFindParentMolecule tests walking up parent-child chain
func TestFindParentMolecule(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create molecule root (epic with template label)
	root := &types.Issue{
		Title:     "Molecule Root",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{BeadsTemplateLabel},
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	// Create child step
	child := &types.Issue{
		Title:     "Child Step",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("Failed to create child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add parent-child: %v", err)
	}

	// Create grandchild
	grandchild := &types.Issue{
		Title:     "Grandchild Step",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, grandchild, "test"); err != nil {
		t.Fatalf("Failed to create grandchild: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     grandchild.ID,
		DependsOnID: child.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add grandchild parent-child: %v", err)
	}

	// Find parent molecule from grandchild
	moleculeID := findParentMolecule(ctx, s, grandchild.ID)
	if moleculeID != root.ID {
		t.Errorf("findParentMolecule(grandchild) = %q, want %q", moleculeID, root.ID)
	}

	// Find parent molecule from child
	moleculeID = findParentMolecule(ctx, s, child.ID)
	if moleculeID != root.ID {
		t.Errorf("findParentMolecule(child) = %q, want %q", moleculeID, root.ID)
	}

	// Find parent molecule from root
	moleculeID = findParentMolecule(ctx, s, root.ID)
	if moleculeID != root.ID {
		t.Errorf("findParentMolecule(root) = %q, want %q", moleculeID, root.ID)
	}

	// Create orphan issue (not part of any molecule)
	orphan := &types.Issue{
		Title:     "Orphan Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, orphan, "test"); err != nil {
		t.Fatalf("Failed to create orphan: %v", err)
	}

	// Should return empty for orphan
	moleculeID = findParentMolecule(ctx, s, orphan.ID)
	if moleculeID != "" {
		t.Errorf("findParentMolecule(orphan) = %q, want empty", moleculeID)
	}
}

// TestAdvanceToNextStep tests auto-advancing to next step
func TestAdvanceToNextStep(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create molecule with sequential steps
	root := &types.Issue{
		Title:     "Advance Test Molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{BeadsTemplateLabel},
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	step1 := &types.Issue{
		Title:     "Step 1",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	step2 := &types.Issue{
		Title:     "Step 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, step := range []*types.Issue{step1, step2} {
		if err := s.CreateIssue(ctx, step, "test"); err != nil {
			t.Fatalf("Failed to create step: %v", err)
		}
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     step.ID,
			DependsOnID: root.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}
	}

	// step2 blocks on step1
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     step2.ID,
		DependsOnID: step1.ID,
		Type:        types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("Failed to add blocking dependency: %v", err)
	}

	// Advance from step1 (just closed) without auto-claim
	result, err := AdvanceToNextStep(ctx, s, step1.ID, false, "test")
	if err != nil {
		t.Fatalf("AdvanceToNextStep failed: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.MoleculeID != root.ID {
		t.Errorf("MoleculeID = %q, want %q", result.MoleculeID, root.ID)
	}
	if result.NextStep == nil {
		t.Error("NextStep should not be nil")
	} else if result.NextStep.ID != step2.ID {
		t.Errorf("NextStep.ID = %q, want %q", result.NextStep.ID, step2.ID)
	}
	if result.AutoAdvanced {
		t.Error("AutoAdvanced should be false when not requested")
	}

	// Verify step2 is still open
	step2Updated, _ := s.GetIssue(ctx, step2.ID)
	if step2Updated.Status != types.StatusOpen {
		t.Errorf("Step2 status = %v, want open (no auto-claim)", step2Updated.Status)
	}

	// Now test with auto-claim
	result, err = AdvanceToNextStep(ctx, s, step1.ID, true, "test")
	if err != nil {
		t.Fatalf("AdvanceToNextStep with auto-claim failed: %v", err)
	}
	if !result.AutoAdvanced {
		t.Error("AutoAdvanced should be true when requested")
	}

	// Verify step2 is now in_progress
	step2Updated, _ = s.GetIssue(ctx, step2.ID)
	if step2Updated.Status != types.StatusInProgress {
		t.Errorf("Step2 status = %v, want in_progress (auto-claim)", step2Updated.Status)
	}
}

// TestAdvanceToNextStepMoleculeComplete tests behavior when molecule is complete
func TestAdvanceToNextStepMoleculeComplete(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create molecule with single step
	root := &types.Issue{
		Title:     "Complete Test Molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{BeadsTemplateLabel},
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}

	step1 := &types.Issue{
		Title:     "Only Step",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, step1, "test"); err != nil {
		t.Fatalf("Failed to create step: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     step1.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Advance from the only step (molecule should be complete)
	result, err := AdvanceToNextStep(ctx, s, step1.ID, false, "test")
	if err != nil {
		t.Fatalf("AdvanceToNextStep failed: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if !result.MolComplete {
		t.Error("MolComplete should be true when all steps are done")
	}
	if result.NextStep != nil {
		t.Error("NextStep should be nil when molecule is complete")
	}
}

// TestAdvanceToNextStepOrphanIssue tests behavior for non-molecule issues
func TestAdvanceToNextStepOrphanIssue(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create standalone issue (not part of molecule)
	orphan := &types.Issue{
		Title:     "Standalone Issue",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, orphan, "test"); err != nil {
		t.Fatalf("Failed to create orphan: %v", err)
	}

	// Advance should return nil (not part of molecule)
	result, err := AdvanceToNextStep(ctx, s, orphan.ID, false, "test")
	if err != nil {
		t.Fatalf("AdvanceToNextStep failed: %v", err)
	}
	if result != nil {
		t.Error("result should be nil for orphan issue")
	}
}

// =============================================================================
// Dynamic Bonding Tests (bd-xo1o.1)
// =============================================================================

// TestGenerateBondedID tests the custom ID generation for dynamic bonding
func TestGenerateBondedID(t *testing.T) {
	tests := []struct {
		name     string
		oldID    string
		rootID   string
		opts     CloneOptions
		wantID   string
		wantErr  bool
		errMatch string
	}{
		{
			name:   "root issue with simple childRef",
			oldID:  "mol-arm",
			rootID: "mol-arm",
			opts: CloneOptions{
				ParentID: "patrol-x7k",
				ChildRef: "arm-ace",
			},
			wantID: "patrol-x7k.arm-ace",
		},
		{
			name:   "root issue with variable substitution",
			oldID:  "mol-arm",
			rootID: "mol-arm",
			opts: CloneOptions{
				ParentID: "patrol-x7k",
				ChildRef: "arm-{{polecat_name}}",
				Vars:     map[string]string{"polecat_name": "ace"},
			},
			wantID: "patrol-x7k.arm-ace",
		},
		{
			name:   "child issue with relative ID",
			oldID:  "mol-arm.capture",
			rootID: "mol-arm",
			opts: CloneOptions{
				ParentID: "patrol-x7k",
				ChildRef: "arm-ace",
			},
			wantID: "patrol-x7k.arm-ace.capture",
		},
		{
			name:   "nested child issue",
			oldID:  "mol-arm.capture.sub",
			rootID: "mol-arm",
			opts: CloneOptions{
				ParentID: "patrol-x7k",
				ChildRef: "arm-ace",
			},
			wantID: "patrol-x7k.arm-ace.capture.sub",
		},
		{
			name:   "no parent ID returns empty (not a bonded operation)",
			oldID:  "mol-arm",
			rootID: "mol-arm",
			opts:   CloneOptions{},
			wantID: "",
		},
		{
			name:   "empty childRef after substitution is error",
			oldID:  "mol-arm",
			rootID: "mol-arm",
			opts: CloneOptions{
				ParentID: "patrol-x7k",
				ChildRef: "{{missing_var}}",
			},
			wantErr:  true,
			errMatch: "invalid childRef",
		},
		{
			name:   "childRef with special chars is error",
			oldID:  "mol-arm",
			rootID: "mol-arm",
			opts: CloneOptions{
				ParentID: "patrol-x7k",
				ChildRef: "arm/ace",
			},
			wantErr:  true,
			errMatch: "invalid childRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, err := generateBondedID(tt.oldID, tt.rootID, tt.opts)

			if tt.wantErr {
				if err == nil {
					t.Errorf("generateBondedID() expected error containing %q, got nil", tt.errMatch)
				} else if !strings.Contains(err.Error(), tt.errMatch) {
					t.Errorf("generateBondedID() error = %q, want error containing %q", err.Error(), tt.errMatch)
				}
				return
			}

			if err != nil {
				t.Errorf("generateBondedID() unexpected error: %v", err)
				return
			}

			if gotID != tt.wantID {
				t.Errorf("generateBondedID() = %q, want %q", gotID, tt.wantID)
			}
		})
	}
}

// TestGetRelativeID tests extracting relative portion from child IDs
func TestGetRelativeID(t *testing.T) {
	tests := []struct {
		name   string
		oldID  string
		rootID string
		want   string
	}{
		{
			name:   "same ID returns empty",
			oldID:  "mol-arm",
			rootID: "mol-arm",
			want:   "",
		},
		{
			name:   "child with single step",
			oldID:  "mol-arm.capture",
			rootID: "mol-arm",
			want:   "capture",
		},
		{
			name:   "child with nested steps",
			oldID:  "mol-arm.capture.sub.deep",
			rootID: "mol-arm",
			want:   "capture.sub.deep",
		},
		{
			name:   "unrelated IDs returns empty",
			oldID:  "other-123",
			rootID: "mol-arm",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getRelativeID(tt.oldID, tt.rootID)
			if got != tt.want {
				t.Errorf("getRelativeID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBondProtoMolWithRef tests dynamic bonding with custom child references
func TestBondProtoMolWithRef(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "patrol"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create a proto with child steps (mol-polecat-arm template)
	protoRoot := &types.Issue{
		Title:     "Polecat Arm: {{polecat_name}}",
		IssueType: types.TypeEpic,
		Status:    types.StatusOpen,
		Priority:  2,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, protoRoot, "test"); err != nil {
		t.Fatalf("Failed to create proto root: %v", err)
	}

	// Add proto steps
	protoCapture := &types.Issue{
		Title:     "Capture {{polecat_name}}",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := s.CreateIssue(ctx, protoCapture, "test"); err != nil {
		t.Fatalf("Failed to create proto capture: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     protoCapture.ID,
		DependsOnID: protoRoot.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add proto dependency: %v", err)
	}

	// Create target molecule (patrol-xxx)
	patrol := &types.Issue{
		Title:     "Witness Patrol",
		IssueType: types.TypeEpic,
		Status:    types.StatusInProgress,
		Priority:  1,
	}
	if err := s.CreateIssue(ctx, patrol, "test"); err != nil {
		t.Fatalf("Failed to create patrol: %v", err)
	}

	// Bond proto to patrol with custom child ref
	vars := map[string]string{"polecat_name": "ace"}
	childRef := "arm-{{polecat_name}}"
	result, err := bondProtoMol(ctx, s, protoRoot, patrol, types.BondTypeSequential, vars, childRef, "test")
	if err != nil {
		t.Fatalf("bondProtoMol failed: %v", err)
	}

	// Verify spawned count
	if result.Spawned != 2 {
		t.Errorf("Spawned = %d, want 2", result.Spawned)
	}

	// Verify root ID follows pattern: patrol.arm-ace
	expectedRootID := patrol.ID + ".arm-ace"
	if result.IDMapping[protoRoot.ID] != expectedRootID {
		t.Errorf("Root ID = %q, want %q", result.IDMapping[protoRoot.ID], expectedRootID)
	}

	// Verify child ID follows pattern: patrol.arm-ace.relative
	// The child's ID should be patrol.arm-ace.capture (but relative part depends on proto structure)
	childID := result.IDMapping[protoCapture.ID]
	if !strings.HasPrefix(childID, expectedRootID+".") {
		t.Errorf("Child ID %q should start with %q", childID, expectedRootID+".")
	}

	// Verify the spawned issues exist and have correct titles
	spawnedRoot, err := s.GetIssue(ctx, expectedRootID)
	if err != nil {
		t.Fatalf("Failed to get spawned root: %v", err)
	}
	if !strings.Contains(spawnedRoot.Title, "ace") {
		t.Errorf("Spawned root title %q should contain 'ace'", spawnedRoot.Title)
	}
}

// TestBondProtoMolMultipleArms tests bonding multiple arms to the same parent
func TestBondProtoMolMultipleArms(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	s, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "patrol"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Create simple proto
	proto := &types.Issue{
		Title:     "Arm: {{name}}",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, proto, "test"); err != nil {
		t.Fatalf("Failed to create proto: %v", err)
	}

	// Create parent patrol
	patrol := &types.Issue{
		Title:     "Patrol",
		IssueType: types.TypeEpic,
		Status:    types.StatusOpen,
		Priority:  1,
	}
	if err := s.CreateIssue(ctx, patrol, "test"); err != nil {
		t.Fatalf("Failed to create patrol: %v", err)
	}

	// Bond arm-ace
	varsAce := map[string]string{"name": "ace"}
	resultAce, err := bondProtoMol(ctx, s, proto, patrol, types.BondTypeParallel, varsAce, "arm-{{name}}", "test")
	if err != nil {
		t.Fatalf("bondProtoMol (ace) failed: %v", err)
	}

	// Bond arm-nux
	varsNux := map[string]string{"name": "nux"}
	resultNux, err := bondProtoMol(ctx, s, proto, patrol, types.BondTypeParallel, varsNux, "arm-{{name}}", "test")
	if err != nil {
		t.Fatalf("bondProtoMol (nux) failed: %v", err)
	}

	// Verify IDs are correct and distinct
	aceID := resultAce.IDMapping[proto.ID]
	nuxID := resultNux.IDMapping[proto.ID]

	expectedAceID := patrol.ID + ".arm-ace"
	expectedNuxID := patrol.ID + ".arm-nux"

	if aceID != expectedAceID {
		t.Errorf("Ace ID = %q, want %q", aceID, expectedAceID)
	}
	if nuxID != expectedNuxID {
		t.Errorf("Nux ID = %q, want %q", nuxID, expectedNuxID)
	}

	// Verify both exist
	aceIssue, err := s.GetIssue(ctx, aceID)
	if err != nil || aceIssue == nil {
		t.Errorf("Ace issue not found: %v", err)
	}
	nuxIssue, err := s.GetIssue(ctx, nuxID)
	if err != nil || nuxIssue == nil {
		t.Errorf("Nux issue not found: %v", err)
	}
}
