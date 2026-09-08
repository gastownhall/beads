package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

func resolveProxiedCustomTypes(dbTypes []string) []string {
	if len(dbTypes) > 0 {
		return dbTypes
	}
	return config.GetCustomTypesFromYAML()
}

func runCreateProxiedServer(cmd *cobra.Command, ctx context.Context, in createInput) {
	if in.repoOverrideSet {
		FatalError("--repo is not supported with --proxied-server")
	}
	switch {
	case in.graphFile != "":
		runCreateProxiedGraph(cmd, ctx, in)
	case in.markdownFile != "":
		runCreateProxiedMarkdown(cmd, ctx, in)
	default:
		runCreateProxiedSingle(cmd, ctx, in)
	}
}

func proxiedOpenUOW(ctx context.Context) (uow.UnitOfWork, domain.CreateContext) {
	if uowProvider == nil {
		FatalError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		FatalError("open unit of work: %v", err)
	}
	cctx, err := uw.ConfigUseCase().LoadCreateContext(ctx)
	if err != nil {
		uw.Close(ctx)
		FatalError("load create context: %v", err)
	}
	return uw, cctx
}

func runCreateProxiedSingle(_ *cobra.Command, ctx context.Context, in createInput) {
	runCreateLintIssue(in)
	if in.explicitID != "" {
		if _, err := validation.ValidateIDFormat(in.explicitID); err != nil {
			FatalError("%v", err)
		}
	}
	deps, err := parseDepSpecs(in.deps)
	if err != nil {
		FatalError("%v", err)
	}
	waitsFor, err := buildWaitsFor(in.waitsFor, in.waitsForGate)
	if err != nil {
		FatalError("%v", err)
	}

	if in.dryRun {
		previewLabels := in.labels
		if in.parentID != "" {
			if uowProvider == nil {
				FatalError("proxied-server UOW provider not initialized")
			}
			dryUW, err := uowProvider.NewUOW(ctx)
			if err != nil {
				FatalError("open unit of work: %v", err)
			}
			if _, err := dryUW.IssueUseCase().GetIssue(ctx, in.parentID); err != nil {
				dryUW.Close(ctx)
				FatalError("parent issue %s not found: %v", in.parentID, err)
			}
			if !in.noInheritLabels {
				inherited, lerr := dryUW.LabelUseCase().GetLabels(ctx, in.parentID)
				if lerr != nil {
					dryUW.Close(ctx)
					FatalError("dry-run inherit labels: %v", lerr)
				}
				previewLabels = mergeCreateLabels(in.labels, inherited)
			}
			dryUW.Close(ctx)
		}
		previewIssue := buildCreateIssueFromInput(in)
		if in.jsonOutput {
			if err := outputJSON(previewIssue); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		} else {
			renderCreateDryRunPreview(previewIssue, previewLabels, in.deps)
		}
		return
	}

	// Load create context (read-only) to validate input before the write tx.
	configUW, cctx := proxiedOpenUOW(ctx)
	configUW.Close(ctx)

	customTypes := resolveProxiedCustomTypes(cctx.CustomTypes)
	if in.issueType != "" {
		it := types.IssueType(in.issueType).Normalize()
		if !it.IsValidWithCustom(customTypes) {
			FatalError("invalid type %q (allowed: built-ins plus configured custom types)", in.issueType)
		}
	}
	if in.explicitID != "" {
		effectivePrefix := overlayYAMLPrefix(cctx.IssuePrefix)
		if err := validation.ValidateIDPrefixAllowed(in.explicitID, effectivePrefix, cctx.AllowedPrefixes, in.force); err != nil {
			FatalError("%v", err)
		}
	}

	issue := buildCreateIssueFromInput(in)
	params := domain.CreateIssueParams{
		Issue:                   issue,
		ExplicitID:              in.explicitID,
		ParentID:                in.parentID,
		Labels:                  in.labels,
		InheritLabelsFromParent: !in.noInheritLabels && in.parentID != "",
		Dependencies:            deps,
		WaitsFor:                waitsFor,
		DiscoveredFromParent:    discoveredFromParent(in.deps),
		ForcePrefix:             in.force,
	}

	var result domain.CreateIssueResult
	if err := uow.RunInTxMsg(ctx, uowProvider, func(uw uow.UnitOfWork) (string, error) {
		var e error
		if issue.Ephemeral {
			result, e = uw.IssueUseCase().CreateWisp(ctx, params, in.createdBy)
		} else {
			result, e = uw.IssueUseCase().CreateIssue(ctx, params, in.createdBy)
		}
		if e != nil {
			return "", e
		}
		return fmt.Sprintf("bd: create %s", result.Issue.ID), nil
	}); err != nil {
		FatalError("%v", err)
	}

	switch {
	case in.jsonOutput:
		if err := outputJSON(result.Issue); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	case in.silent:
		fmt.Println(result.Issue.ID)
	default:
		fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(result.Issue.ID, result.Issue.Title))
		fmt.Printf("  Priority: P%d\n", result.Issue.Priority)
		fmt.Printf("  Status: %s\n", result.Issue.Status)
	}
}

func runCreateLintIssue(in createInput) {
	if in.validationMode != "error" && in.validationMode != "warn" {
		return
	}
	lintIssue := &types.Issue{
		IssueType:          types.IssueType(in.issueType).Normalize(),
		Description:        in.description,
		AcceptanceCriteria: in.acceptanceCriteria,
	}
	if err := validation.LintIssue(lintIssue); err != nil {
		if in.validationMode == "error" {
			FatalError("%v", err)
		}
		fmt.Fprintf(os.Stderr, "%s %v\n", ui.RenderWarn("⚠"), err)
	}
}

func buildCreateIssueFromInput(in createInput) *types.Issue {
	return buildCreateIssue(createIssueParams{
		ID:                 in.explicitID,
		Title:              in.title,
		Description:        in.description,
		Design:             in.design,
		AcceptanceCriteria: in.acceptanceCriteria,
		Notes:              in.notes,
		SpecID:             in.specID,
		Priority:           in.priority,
		IssueType:          types.IssueType(in.issueType).Normalize(),
		Assignee:           in.assignee,
		ExternalRef:        in.externalRef,
		EstimatedMinutes:   in.estimatedMinutes,
		Ephemeral:          in.ephemeral,
		NoHistory:          in.noHistory,
		CreatedBy:          in.createdBy,
		Owner:              in.owner,
		MolType:            in.molType,
		WispType:           in.wispType,
		EventKind:          in.eventCategory,
		Actor:              in.eventActor,
		Target:             in.eventTarget,
		Payload:            in.eventPayload,
		DueAt:              in.dueAt,
		DeferUntil:         in.deferUntil,
		Metadata:           in.metadata,
	})
}

func runCreateProxiedMarkdown(_ *cobra.Command, ctx context.Context, in createInput) {
	templates, err := parseMarkdownFile(in.markdownFile)
	if err != nil {
		FatalError("parsing markdown file: %v", err)
	}
	if len(templates) == 0 {
		FatalError("no issues found in markdown file")
	}

	if in.validationMode == "error" || in.validationMode == "warn" {
		for _, t := range templates {
			lintIssue := &types.Issue{
				IssueType:          t.IssueType,
				Description:        t.Description,
				AcceptanceCriteria: t.AcceptanceCriteria,
			}
			if err := validation.LintIssue(lintIssue); err != nil {
				if in.validationMode == "error" {
					FatalError("template %q: %v", t.Title, err)
				}
				fmt.Fprintf(os.Stderr, "%s template %q: %v\n", ui.RenderWarn("⚠"), t.Title, err)
			}
		}
	}

	type templateBuild struct {
		template *IssueTemplate
		deps     []domain.DependencySpec
	}

	builds := make([]templateBuild, 0, len(templates))
	for _, t := range templates {
		deps, err := parseMarkdownDepSpecs(t.Dependencies, t.Title)
		if err != nil {
			FatalError("%v", err)
		}
		builds = append(builds, templateBuild{template: t, deps: deps})
	}

	configUW, cctx := proxiedOpenUOW(ctx)
	configUW.Close(ctx)

	customTypes := resolveProxiedCustomTypes(cctx.CustomTypes)
	for _, b := range builds {
		if b.template.IssueType == "" {
			continue
		}
		if !b.template.IssueType.IsValidWithCustom(customTypes) {
			FatalError("template %q: invalid type %q", b.template.Title, b.template.IssueType)
		}
	}

	paramsList := make([]domain.CreateIssueParams, 0, len(builds))
	for _, b := range builds {
		t := b.template
		paramsList = append(paramsList, domain.CreateIssueParams{
			Issue: &types.Issue{
				Title:              t.Title,
				Description:        t.Description,
				Design:             t.Design,
				AcceptanceCriteria: t.AcceptanceCriteria,
				Status:             types.StatusOpen,
				Priority:           t.Priority,
				IssueType:          t.IssueType,
				Assignee:           t.Assignee,
				Ephemeral:          in.ephemeral,
				NoHistory:          in.noHistory,
				MolType:            in.molType,
				CreatedBy:          in.createdBy,
				Owner:              in.owner,
			},
			Labels:       t.Labels,
			Dependencies: b.deps,
		})
	}

	var result domain.CreateIssuesResult
	if err := uow.RunInTxMsg(ctx, uowProvider, func(uw uow.UnitOfWork) (string, error) {
		var e error
		if in.ephemeral {
			result, e = uw.IssueUseCase().CreateWisps(ctx, paramsList, in.createdBy)
		} else {
			result, e = uw.IssueUseCase().CreateIssues(ctx, paramsList, in.createdBy)
		}
		if e != nil {
			return "", e
		}
		return fmt.Sprintf("bd: create %d issue(s) from %s", len(result.Issues), in.markdownFile), nil
	}); err != nil {
		FatalError("creating issues from markdown: %v", err)
	}

	if in.jsonOutput {
		if err := outputJSON(result.Issues); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return
	}

	fmt.Printf("%s Created %d issues from %s:\n", ui.RenderPass("✓"), len(result.Issues), in.markdownFile)
	for _, issue := range result.Issues {
		fmt.Printf("  %s: %s [P%d, %s]\n", issue.ID, issue.Title, issue.Priority, issue.IssueType)
	}
}

func parseMarkdownDepSpecs(deps []string, templateTitle string) ([]domain.DependencySpec, error) {
	var out []domain.DependencySpec
	for _, raw := range deps {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		var depType types.DependencyType
		var target string
		if strings.Contains(raw, ":") {
			parts := strings.SplitN(raw, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid dependency format %q for issue %q", raw, templateTitle)
			}
			depType = types.DependencyType(strings.TrimSpace(parts[0]))
			target = strings.TrimSpace(parts[1])
		} else {
			depType = types.DepBlocks
			target = raw
		}

		if !depType.IsValid() {
			return nil, fmt.Errorf("invalid dependency type %q for issue %q", depType, templateTitle)
		}
		out = append(out, domain.DependencySpec{
			Type:     depType,
			TargetID: target,
		})
	}
	return out, nil
}

func runCreateProxiedGraph(_ *cobra.Command, ctx context.Context, in createInput) {
	data, err := os.ReadFile(in.graphFile) // #nosec G304 -- user-provided path is intentional
	if err != nil {
		FatalError("reading graph plan: %v", err)
	}
	if unknown := detectUnknownGraphFields(data); len(unknown) > 0 {
		warnUnknownGraphFields(os.Stderr, unknown)
	}

	var plan GraphApplyPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		FatalError("parsing graph plan: %v", err)
	}

	if in.dryRun {
		if uowProvider == nil {
			FatalError("proxied-server UOW provider not initialized")
		}
		dryUW, err := uowProvider.NewUOW(ctx)
		if err != nil {
			FatalError("open unit of work: %v", err)
		}
		cctx, err := dryUW.ConfigUseCase().LoadCreateContext(ctx)
		dryUW.Close(ctx)
		if err != nil {
			FatalError("load create context: %v", err)
		}
		if err := validateGraphApplyPlan(&plan, resolveProxiedCustomTypes(cctx.CustomTypes)); err != nil {
			FatalError("invalid graph plan: %v", err)
		}
		if err := emitGraphApplyDryRun(&plan); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return
	}

	uw, cctx := proxiedOpenUOW(ctx)
	defer uw.Close(ctx)

	if err := validateGraphApplyPlan(&plan, resolveProxiedCustomTypes(cctx.CustomTypes)); err != nil {
		FatalError("invalid graph plan: %v", err)
	}

	domainPlan := buildDomainGraphPlan(plan, in)

	var result domain.GraphApplyResult
	if in.ephemeral {
		result, err = uw.IssueUseCase().ApplyWispGraph(ctx, domainPlan, in.createdBy)
	} else {
		result, err = uw.IssueUseCase().ApplyIssueGraph(ctx, domainPlan, in.createdBy)
	}
	if err != nil {
		FatalError("graph create: %v", err)
	}

	commitMsg := plan.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("bd: graph-apply %d nodes", len(plan.Nodes))
	}

	if err := uw.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
		FatalError("commit: %v", err)
	}

	if in.jsonOutput {
		if err := outputJSON(GraphApplyResult{IDs: result.IDs}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return
	}

	fmt.Printf("Created %d issues\n", len(result.IDs))
	keys := make([]string, 0, len(result.IDs))
	for k := range result.IDs {
		keys = append(keys, k)
	}

	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %s -> %s\n", k, result.IDs[k])
	}
}

func buildDomainGraphPlan(plan GraphApplyPlan, in createInput) domain.GraphPlan {
	nodes := make([]domain.GraphNode, 0, len(plan.Nodes))
	for _, n := range plan.Nodes {
		nodes = append(nodes, domain.GraphNode{
			Key:               n.Key,
			Issue:             materializeGraphNodeIssue(n, in),
			ParentKey:         n.ParentKey,
			ParentID:          n.ParentID,
			Assignee:          n.Assignee,
			AssignAfterCreate: n.AssignAfterCreate,
			MetadataRefs:      n.MetadataRefs,
			Labels:            n.Labels,
		})
	}
	edges := make([]domain.GraphEdge, 0, len(plan.Edges))
	for _, e := range plan.Edges {
		edges = append(edges, domain.GraphEdge{
			FromKey: e.FromKey,
			FromID:  e.FromID,
			ToKey:   e.ToKey,
			ToID:    e.ToID,
			Type:    graphApplyDependencyType(e.Type),
		})
	}
	return domain.GraphPlan{Nodes: nodes, Edges: edges}
}

func materializeGraphNodeIssue(n GraphApplyNode, in createInput) *types.Issue {
	issueType := types.IssueType(n.Type)
	if issueType == "" {
		issueType = types.TypeTask
	}
	priority := 2
	if n.Priority != nil {
		priority = *n.Priority
	}
	var metadataJSON json.RawMessage
	if len(n.Metadata) > 0 {
		raw, err := json.Marshal(n.Metadata)
		if err != nil {
			FatalError("node %q: marshaling metadata: %v", n.Key, err)
		}
		metadataJSON = raw
	}
	return &types.Issue{
		Title:       n.Title,
		Description: n.Description,
		IssueType:   issueType,
		Status:      types.StatusOpen,
		Priority:    priority,
		Labels:      n.Labels,
		Metadata:    metadataJSON,
		Ephemeral:   in.ephemeral,
		NoHistory:   in.noHistory,
		CreatedBy:   in.createdBy,
		Owner:       in.owner,
	}
}
