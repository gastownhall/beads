package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
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

func runCreateProxiedServer(cmd *cobra.Command, ctx context.Context, in createInput) error {
	if in.repoOverrideSet {
		return HandleError("--repo is not supported with --proxied-server")
	}
	switch {
	case in.graphFile != "":
		return runCreateProxiedGraph(cmd, ctx, in)
	case in.markdownFile != "":
		return runCreateProxiedMarkdown(cmd, ctx, in)
	default:
		return runCreateProxiedSingle(cmd, ctx, in)
	}
}

func runCreateProxiedSingle(_ *cobra.Command, ctx context.Context, in createInput) error {
	if err := runCreateLintIssue(in); err != nil {
		return err
	}
	if in.explicitID != "" {
		if _, err := validation.ValidateIDFormat(in.explicitID); err != nil {
			return HandleError("%v", err)
		}
	}
	deps, err := parseDepSpecs(in.deps)
	if err != nil {
		return HandleError("%v", err)
	}
	waitsFor, err := buildWaitsFor(in.waitsFor, in.waitsForGate)
	if err != nil {
		return HandleError("%v", err)
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	if in.dryRun {
		previewLabels := in.labels
		if in.parentID != "" {
			dryUW, err := uowProvider.NewUOW(ctx)
			if err != nil {
				return HandleError("open unit of work: %v", err)
			}
			if _, err := dryUW.IssueUseCase().GetIssue(ctx, in.parentID); err != nil {
				dryUW.Close(ctx)
				return HandleError("parent issue %s not found: %v", in.parentID, err)
			}
			if !in.noInheritLabels {
				inherited, lerr := dryUW.LabelUseCase().GetLabels(ctx, in.parentID)
				if lerr != nil {
					dryUW.Close(ctx)
					return HandleError("dry-run inherit labels: %v", lerr)
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
		return nil
	}

	issue := buildCreateIssueFromInput(in)

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*types.Issue, string, error) {
		cctx, err := uw.ConfigUseCase().LoadCreateContext(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("load create context: %w", err)
		}

		customTypes := resolveProxiedCustomTypes(cctx.CustomTypes)
		if in.issueType != "" {
			it := types.IssueType(in.issueType).Normalize()
			if !it.IsValidWithCustom(customTypes) {
				return nil, "", fmt.Errorf("invalid type %q (allowed: built-ins plus configured custom types)", in.issueType)
			}
		}
		if in.status != "" {
			if !types.Status(in.status).IsValidWithCustom(types.CustomStatusNames(cctx.CustomStatuses)) {
				return nil, "", fmt.Errorf("invalid status %q (built-in: open, in_progress, blocked, deferred, closed, pinned, hooked; or configure custom statuses via 'bd config set status.custom')", in.status)
			}
		}
		if in.explicitID != "" {
			effectivePrefix := overlayYAMLPrefix(cctx.IssuePrefix)
			if err := validation.ValidateIDPrefixAllowed(in.explicitID, effectivePrefix, cctx.AllowedPrefixes, in.force); err != nil {
				return nil, "", err
			}
		}

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
		var createErr error
		if issue.Ephemeral || issue.NoHistory {
			result, createErr = uw.IssueUseCase().CreateWisp(ctx, params, in.createdBy)
		} else {
			result, createErr = uw.IssueUseCase().CreateIssue(ctx, params, in.createdBy)
		}
		if createErr != nil {
			return nil, "", createErr
		}

		return result.Issue, fmt.Sprintf("bd: create %s", result.Issue.ID), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	switch {
	case in.jsonOutput:
		if err := outputJSON(res); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	case in.silent:
		fmt.Println(res.ID)
	default:
		fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(res.ID, res.Title))
		fmt.Printf("  Priority: P%d\n", res.Priority)
		fmt.Printf("  Status: %s\n", res.Status)
	}
	return nil
}

func runCreateLintIssue(in createInput) error {
	if in.validationMode != "error" && in.validationMode != "warn" {
		return nil
	}
	lintIssue := &types.Issue{
		IssueType:          types.IssueType(in.issueType).Normalize(),
		Description:        in.description,
		AcceptanceCriteria: in.acceptanceCriteria,
	}
	if err := validation.LintIssue(lintIssue); err != nil {
		if in.validationMode == "error" {
			return HandleError("%v", err)
		}
		fmt.Fprintf(os.Stderr, "%s %v\n", ui.RenderWarn("⚠"), err)
	}
	return nil
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
		InitialStatus:      in.status,
		DueAt:              in.dueAt,
		DeferUntil:         in.deferUntil,
		Metadata:           in.metadata,
	})
}

func runCreateProxiedMarkdown(_ *cobra.Command, ctx context.Context, in createInput) error {
	templates, err := parseMarkdownFile(in.markdownFile)
	if err != nil {
		return HandleError("parsing markdown file: %v", err)
	}
	if len(templates) == 0 {
		return HandleError("no issues found in markdown file")
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
					return HandleError("template %q: %v", t.Title, err)
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
			return HandleError("%v", err)
		}
		builds = append(builds, templateBuild{template: t, deps: deps})
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.Issue, string, error) {
		cctx, err := uw.ConfigUseCase().LoadCreateContext(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("load create context: %w", err)
		}

		customTypes := resolveProxiedCustomTypes(cctx.CustomTypes)
		for _, b := range builds {
			if b.template.IssueType == "" {
				continue
			}
			if !b.template.IssueType.IsValidWithCustom(customTypes) {
				return nil, "", fmt.Errorf("template %q: invalid type %q", b.template.Title, b.template.IssueType)
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
		var createErr error
		if in.ephemeral || in.noHistory {
			result, createErr = uw.IssueUseCase().CreateWisps(ctx, paramsList, in.createdBy)
		} else {
			result, createErr = uw.IssueUseCase().CreateIssues(ctx, paramsList, in.createdBy)
		}
		if createErr != nil {
			return nil, "", fmt.Errorf("creating issues from markdown: %w", createErr)
		}

		return result.Issues, fmt.Sprintf("bd: create %d issue(s) from %s", len(result.Issues), in.markdownFile), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	if in.jsonOutput {
		if err := outputJSON(res); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return nil
	}

	fmt.Printf("%s Created %d issues from %s:\n", ui.RenderPass("✓"), len(res), in.markdownFile)
	for _, issue := range res {
		fmt.Printf("  %s: %s [P%d, %s]\n", issue.ID, issue.Title, issue.Priority, issue.IssueType)
	}
	return nil
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

func runCreateProxiedGraph(_ *cobra.Command, ctx context.Context, in createInput) error {
	data, err := os.ReadFile(in.graphFile) // #nosec G304 -- user-provided path is intentional
	if err != nil {
		return HandleError("reading graph plan: %v", err)
	}
	if unknown := detectUnknownGraphFields(data); len(unknown) > 0 {
		warnUnknownGraphFields(os.Stderr, unknown)
	}

	var plan GraphApplyPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return HandleError("parsing graph plan: %v", err)
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	if in.dryRun {
		dryUW, err := uowProvider.NewUOW(ctx)
		if err != nil {
			return HandleError("open unit of work: %v", err)
		}
		cctx, err := dryUW.ConfigUseCase().LoadCreateContext(ctx)
		if err != nil {
			dryUW.Close(ctx)
			return HandleError("load create context: %v", err)
		}
		// Keep the UOW open through validation: the explicit-ID collision
		// preflight reads through it.
		_, err = validateProxiedGraphPlan(&plan, in, cctx, uowIssueExists(ctx, dryUW))
		dryUW.Close(ctx)
		if err != nil {
			return HandleError("invalid graph plan: %v", err)
		}
		if err := emitGraphApplyDryRun(&plan, in.graphApplyOptions()); err != nil {
			return HandleError("%v", err)
		}
		return nil
	}

	domainPlan, err := buildDomainGraphPlan(plan, in)
	if err != nil {
		return err
	}

	commitMsg := plan.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("bd: graph-apply %d nodes", len(plan.Nodes))
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (map[string]string, string, error) {
		cctx, err := uw.ConfigUseCase().LoadCreateContext(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("load create context: %w", err)
		}

		// validateProxiedGraphPlan enforces a uniform storage class, so its
		// resolved useWisp decides which table the whole plan routes to. The
		// collision preflight runs inside this transaction, so it cannot race
		// a concurrent create of the same explicit ID.
		useWisp, err := validateProxiedGraphPlan(&plan, in, cctx, uowIssueExists(ctx, uw))
		if err != nil {
			return nil, "", fmt.Errorf("invalid graph plan: %w", err)
		}

		var result domain.GraphApplyResult
		var applyErr error
		if useWisp {
			result, applyErr = uw.IssueUseCase().ApplyWispGraph(ctx, domainPlan, in.createdBy)
		} else {
			result, applyErr = uw.IssueUseCase().ApplyIssueGraph(ctx, domainPlan, in.createdBy)
		}
		if applyErr != nil {
			return nil, "", fmt.Errorf("graph create: %w", applyErr)
		}

		return result.IDs, commitMsg, nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	if in.jsonOutput {
		if err := outputJSON(GraphApplyResult{IDs: res}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return nil
	}

	fmt.Printf("Created %d issues\n", len(res))
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}

	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %s -> %s\n", k, res[k])
	}
	return nil
}

// validateProxiedGraphPlan runs full plan validation for proxied-server mode:
// shared plan checks, uniform storage class (proxied routes the whole plan to
// one table), explicit-ID prefix checks against the server's config, and the
// explicit-ID collision preflight through the unit of work's issue lookup.
// The returned useWisp is the plan-wide table routing decision.
func validateProxiedGraphPlan(plan *GraphApplyPlan, in createInput, cctx domain.CreateContext, issueExists func(id string) (bool, error)) (useWisp bool, err error) {
	cfg := graphPlanConfig{
		customTypes: resolveProxiedCustomTypes(cctx.CustomTypes),
		// No YAML fallback for statuses — the server database is authoritative
		// (that's where 'bd config set status.custom' writes) and statuses are
		// store-only everywhere (single-issue create, list filters), unlike
		// custom types.
		customStatuses:  types.CustomStatusNames(cctx.CustomStatuses),
		dbPrefix:        overlayYAMLPrefix(cctx.IssuePrefix),
		allowedPrefixes: cctx.AllowedPrefixes,
		issueExists:     issueExists,
	}
	return validateFullGraphPlan(plan, cfg, in.graphApplyOptions(), true)
}

// uowIssueExists adapts a unit of work's issue lookups to the plan
// validator's explicit-ID collision probe, bound to the caller's context so
// in-transaction validation reads its own transaction. Issues and wisps share
// one ID space but the domain getters are per-table, so probe both.
func uowIssueExists(ctx context.Context, uw uow.UnitOfWork) func(id string) (bool, error) {
	isNotFound := func(err error) bool {
		return errors.Is(err, storage.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
	}
	return func(id string) (bool, error) {
		if _, err := uw.IssueUseCase().GetIssue(ctx, id); err == nil {
			return true, nil
		} else if !isNotFound(err) {
			return false, err
		}
		if _, err := uw.IssueUseCase().GetWisp(ctx, id); err == nil {
			return true, nil
		} else if !isNotFound(err) {
			return false, err
		}
		return false, nil
	}
}

// graphApplyNodeIssue path (full issue-model parity with `bd create`).
func buildDomainGraphPlan(plan GraphApplyPlan, in createInput) (domain.GraphPlan, error) {
	opts := in.graphApplyOptions()
	nodes := make([]domain.GraphNode, 0, len(plan.Nodes))
	for _, n := range plan.Nodes {
		issue, err := graphApplyNodeIssue(n, opts, in.createdBy, in.owner)
		if err != nil {
			return domain.GraphPlan{}, fmt.Errorf("invalid graph plan: %w", err)
		}
		deps := make([]domain.GraphNodeDep, 0, len(n.Deps))
		for _, d := range n.Deps {
			deps = append(deps, domain.GraphNodeDep{
				Type:   graphApplyDependencyType(d.Type),
				Target: d.Target,
			})
		}
		nodes = append(nodes, domain.GraphNode{
			Key:               n.Key,
			Issue:             issue,
			ParentKey:         n.effectiveParentKey(),
			ParentID:          n.ParentID,
			Assignee:          n.Assignee,
			AssignAfterCreate: n.AssignAfterCreate,
			MetadataRefs:      n.MetadataRefs,
			Labels:            n.Labels,
			Deps:              deps,
		})
	}
	edges := make([]domain.GraphEdge, 0, len(plan.Edges))
	for _, e := range plan.Edges {
		edges = append(edges, domain.GraphEdge{
			FromKey:    e.FromKey,
			FromID:     e.FromID,
			ToKey:      e.ToKey,
			ToID:       e.ToID,
			Type:       graphApplyDependencyType(e.Type),
			Gate:       e.Gate,
			SpawnerKey: e.SpawnerKey,
			SpawnerID:  e.SpawnerID,
			ThreadID:   e.ThreadID,
		})
	}
	return domain.GraphPlan{Nodes: nodes, Edges: edges}, nil
}
