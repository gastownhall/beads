package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/issueops"
)

func runCreateProxiedServer(cmd *cobra.Command, ctx context.Context, in createInput) error {
	if in.repoOverrideSet {
		// Defense in depth: validateProxyCapabilitiesBeforeProvider already
		// refuses `create --repo` before this route is reachable. Typed anyway,
		// so the day a command slips past the gate the refusal is still the
		// same shape rather than silently degrading to prose.
		return HandleProxyCapabilityError(AssertProxyCommandCapability("create", ProxyModeProxied, ProxyCapRepo))
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
	waitsFor, err := buildWaitsFor(in.waitsFor, in.waitsForGate, in.waitsForGateSet)
	if err != nil {
		return HandleError("%v", err)
	}

	if in.dryRun {
		if uowProvider == nil {
			return HandleError("proxied-server UOW provider not initialized")
		}
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
				// A READ inside the DRY-RUN unit of work, which is opened only to
				// be discarded: this previews what --parent would inherit without
				// creating anything. The role that answers it for real is
				// CreateRequest.InheritLabelsFromParent, which resolves the parent's
				// labels inside the create it is part of — and a preview has no
				// create to be part of. A dry-run mode on the create role is the
				// follow-up (ga-2ltro.12).
				inherited, lerr := dryUW.LabelUseCase().GetLabels(ctx, in.parentID) //nolint:forbidigo // dry-run preview; the role resolves this only inside a real create
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

	ops, err := proxiedIssueLifecycle()
	if err != nil {
		return HandleError("%v", err)
	}

	issue := buildCreateIssueFromInput(in)
	// Labels ride on the issue because that is where the contract reads them:
	// CreateRequest.Issue documents them as authoritative.
	issue.Labels = append([]string(nil), in.labels...)
	if err := inheritProxiedSourceRepo(ctx, issue, deps); err != nil {
		return err
	}

	// SPEC-GAP bd-yby99.32: Lifecycle.Create promises nothing about the
	// version-control entry a create records and CreateRequest carries no
	// Provenance to spell one, so this route's commit message moves from
	// "bd: create <id>" to whatever the implementation defaults to.
	result, err := ops.Create(ctx, issueops.CreateRequest{
		Actor:                   in.createdBy,
		Issue:                   issue,
		ParentID:                in.parentID,
		InheritLabelsFromParent: !in.noInheritLabels && in.parentID != "",
		Dependencies:            createDependencyRequests(deps),
		WaitsFor:                waitsForRequest(waitsFor),
		ForceIDPrefix:           in.force,
		// The workspace's config.yaml prefix wins over the server database's,
		// and only this side can see it. Without this the proxied route mints
		// ids the workspace's own configuration forbids and the direct route
		// refuses.
		IDPrefix: createIDPrefixOverride(),
	})
	if err != nil {
		// RULING R1, reported the same way the direct route reports it: an
		// occupied --id is a refusal, not a silent full-row upsert dressed up
		// as success.
		if errors.Is(err, storage.ErrAlreadyExists) && in.explicitID != "" {
			return HandleErrorRespectJSON("%s already exists; use bd update, or bd import for upsert semantics", in.explicitID)
		}
		return HandleError("%v", err)
	}
	// Every post-write read comes from the contract's result snapshot: the
	// local struct still has no ID for an auto-minted create. Dependencies and
	// comments come off because `bd create` has never printed them.
	created := result.Issue
	created.Dependencies = nil
	created.Comments = nil

	switch {
	case in.jsonOutput:
		if err := outputJSON(created); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	case in.silent:
		fmt.Println(created.ID)
	default:
		fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(created.ID, created.Title))
		fmt.Printf("  Priority: P%d\n", created.Priority)
		fmt.Printf("  Status: %s\n", created.Status)
	}
	return nil
}

// inheritProxiedSourceRepo copies a discovered-from parent's source repo onto
// the new issue, which is what the direct route does before it calls the role
// (cmd/bd/create.go).
//
// A failed lookup is not a verdict. The direct route ignores one and creates
// with the default source repo; a genuinely absent target is refused by the
// create itself, with the contract's own error for a dangling edge.
func inheritProxiedSourceRepo(ctx context.Context, issue *types.Issue, deps []domain.DependencySpec) error {
	// Reuse the already-parsed specs (not the raw --deps strings) so this
	// can't drift from parseDepSpec's normalization rules.
	parentID := discoveredFromParentSpec(deps)
	if parentID == "" {
		return nil
	}
	rd, err := proxiedIssueReader()
	if err != nil {
		return HandleError("%v", err)
	}
	details, err := rd.Get(ctx, issueops.GetRequest{ID: parentID})
	if err != nil {
		return nil
	}
	if details.Issue.SourceRepo != "" {
		issue.SourceRepo = details.Issue.SourceRepo
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
		StorageClass:       in.storageClass,
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

// runCreateProxiedMarkdown creates every issue in a markdown file as ONE act,
// through issueops.BatchCreator.
func runCreateProxiedMarkdown(_ *cobra.Command, ctx context.Context, in createInput) error {
	templates, err := parseMarkdownFile(in.markdownFile)
	if err != nil {
		return HandleError("parsing markdown file: %v", err)
	}
	if len(templates) == 0 {
		return HandleError("no issues found in markdown file")
	}
	request, err := buildMarkdownBatchRequest(templates, in)
	if err != nil {
		return err
	}
	creator, err := proxiedBatchCreator()
	if err != nil {
		return HandleError("%v", err)
	}
	result, err := creator.CreateBatch(ctx, request)
	if err != nil {
		return HandleError("creating issues from markdown: %v", err)
	}
	return reportMarkdownBatch(result.Issues, in)
}

// proxiedBatchCreator reaches the batch-create role through the provider's own
// capability accessor, which is where each decorator adds its layer.
func proxiedBatchCreator() (issueops.BatchCreator, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.BatchCreatorSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the batch-create surface", uowProvider)
	}
	return src.BatchCreator()
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

	// Live apply runs the same shared validation the dry-run branch above just
	// ran, through its own short-lived unit of work (the collision preflight
	// in this pass is therefore NOT atomic with the apply below — a real
	// explicit-ID collision is still caught, because BatchApplier's own
	// create item independently raises storage.ErrAlreadyExists; this preflight
	// just gives that case a clearer "invalid graph plan" message up front).
	// BatchApplier manages its own transaction and commit-retry budget (see
	// internal/storage/uow/batch_applier.go), so the apply itself is NOT
	// wrapped in uow.RunTxResult the way the old domain.GraphPlan path was.
	validateUW, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleError("open unit of work: %v", err)
	}
	cctx, err := validateUW.ConfigUseCase().LoadCreateContext(ctx)
	if err != nil {
		validateUW.Close(ctx)
		return HandleError("load create context: %v", err)
	}
	_, err = validateProxiedGraphPlan(&plan, in, cctx, uowIssueExists(ctx, validateUW))
	validateUW.Close(ctx)
	if err != nil {
		return HandleError("invalid graph plan: %v", err)
	}

	src, ok := uowProvider.(uow.BatchApplierSource)
	if !ok {
		return HandleError("proxied-server provider %T does not offer the batch-apply surface", uowProvider)
	}
	applier, err := src.BatchApplier()
	if err != nil {
		return HandleError("%v", err)
	}
	req, err := buildGraphApplyBatchRequest(&plan, in.graphApplyOptions(), in.createdBy, in.owner)
	if err != nil {
		return HandleError("%v", err)
	}
	result, err := applier.ApplyBatch(ctx, req)
	if err != nil {
		return HandleError("graph create: %v", err)
	}
	res := result.Keys

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
		customTypes: cctx.CustomTypes,
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
