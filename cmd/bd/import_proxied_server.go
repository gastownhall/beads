package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// proxiedImporter hands back the guarded bulk-import surface for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedIssueReader and proxiedBatchCloser perform, and
// for the same reason: the accessor is where each layer is added, so a
// command that reached for the constructor would get an unlayered importer.
func proxiedImporter() (publicops.Importer, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.ImporterSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the import surface", uowProvider)
	}
	return src.Importer()
}

// runImportRecordsProxied is the proxied-server import pipeline over the
// parsed records. It mirrors runImportRecordsClassic stage for stage — dedup,
// the read-only plan (dried-run classification, stale pre-filter, record
// screen), batch write, issue_prefix reconciliation — through the SAME
// classification and reporting code, with two deliberate structural
// differences:
//
//   - ONE COMMIT PER INVOCATION. The classic path chunks a large import into
//     bounded transactions (a SQLite write-lock fairness measure) and commits
//     the issue_prefix sync separately; the proxied path has no PostRun
//     auto-commit and the whole import — rows, memories, prefix sync — lands
//     in ONE unit of work with ONE history entry, the Importer capability's
//     contract.
//
//   - The stale guard keeps its classic two-half shape: the pre-filter runs in
//     its own read (reporting StaleSkippedIDs and keeping stale rows' aux data
//     out of the batch), and RejectStaleUpserts re-checks updated_at inside
//     the write transaction, closing the same race the classic path closes.
func runImportRecordsProxied(ctx context.Context, issues []*types.Issue, memories []memoryRecord, sources map[*types.Issue]importRecordSource, source string) error {
	if uowProvider == nil {
		return fmt.Errorf("proxied-server UOW provider not initialized")
	}

	// Dedup: skip issues whose title matches an existing open issue.
	dedupHits := 0
	if importDedup && len(issues) > 0 {
		type dedupOutcome struct {
			kept []*types.Issue
			hits int
		}
		out, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (dedupOutcome, error) {
			kept, hits := filterDuplicatesByTitle(ctx, uowImportTitleSearcher{uw: uw}, issues)
			return dedupOutcome{kept: kept, hits: hits}, nil
		})
		if err != nil {
			return err
		}
		issues = out.kept
		dedupHits = out.hits
	}

	result := importResultJSON{
		Source:    source,
		DedupHits: dedupHits,
		DryRun:    importDryRun,
	}

	// THE PLAN (read-only), in ONE unit of work: the stale pre-filter or the
	// --allow-stale existence classification, then the pre-write record screen
	// (GH#4492) — whose vocabulary read rides the same ConfigUseCase the
	// proxied create path validates from. A reject fails the strict import
	// here, before the batch is submitted, so nothing is written on the
	// server either.
	plan, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*importRunPlan, error) {
		return planProxiedImport(ctx, uw, issues, importAllowStale, importDryRun, sources)
	})
	if err != nil {
		return err
	}
	if err := applyImportScreenPolicy(&result, plan); err != nil {
		return err
	}
	issues = plan.Issues

	if importDryRun {
		result.Memories = len(memories)
		result.Skipped = dedupHits

		classification, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*ImportResult, error) {
			return dryRunClassification(ctx, uw.IssueUseCase(), plan.Issues, plan.StaleSkippedIDs, &plan.ChangePlan, importAllowStale)
		})
		if err != nil {
			return fmt.Errorf("dry-run: %w", err)
		}
		applyImportDryRunClassification(&result, classification)
		return renderImportDryRun(result, len(memories), source, dedupHits)
	}

	// The rejects file lands BEFORE the batch is submitted (GH#4492): whatever
	// the import does next, the operator already holds the skipped records,
	// and an unwritable output aborts the import before anything is written
	// on the server either. A dry run never reaches here — it previews the
	// rejects without writing the file.
	if err := saveImportRejects(&result, plan.Rejects); err != nil {
		return err
	}

	memoryEntries := make([]publicops.ImportMemory, 0, len(memories))
	for _, mem := range memories {
		memoryEntries = append(memoryEntries, publicops.ImportMemory{
			Key:   kvPrefix + memoryPrefix + mem.Key,
			Value: mem.Value,
		})
	}

	importer, err := proxiedImporter()
	if err != nil {
		return err
	}

	// The ONE resolver both import paths share for config.yaml's issue_prefix
	// (importSyncPrefix, import.go): the --global exclusion, the single trim,
	// validatePrefix, the warning that names BOTH effects, and "" meaning
	// "reconcile nothing". Calling it instead of restating its rules here is
	// what keeps the two modes from drifting — a second hand-maintained copy
	// of this policy is the very divergence one resolver exists to prevent,
	// and the resolver's table test then covers this route too.
	//
	// Accepted boundary: a --global import against an externally-provisioned
	// GLOBAL database that is itself missing config.issue_prefix stays
	// unseedable and still fails with "issue_prefix config is missing" — that
	// row is `bd init --global`'s to write, and config.yaml cannot speak for
	// the shared store.
	syncPrefix := importSyncPrefix()

	// THE BATCH: rows, memories and the issue_prefix reconciliation in one
	// transaction, one history entry. The prefix sync runs even when the
	// batch is otherwise empty, exactly as the classic path's post-commit
	// sync does (be-llaf; config.yaml is authoritative, not a rename) — and
	// so does the seed, which reports itself through the same PrefixSynced
	// result so an otherwise-empty batch still commits it.
	batch, err := importer.ImportBatch(ctx, publicops.ImportBatchRequest{
		Actor:                getActorWithGit(),
		Issues:               issues,
		Memories:             memoryEntries,
		AllowStale:           importAllowStale,
		SkipPrefixValidation: true,
		SyncIssuePrefix:      syncPrefix,
		Source:               filepath.Base(source),
	})
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	result.Memories = batch.MemoriesImported

	if len(issues) > 0 {
		staleRejectedSet := make(map[string]struct{}, len(batch.StaleRejectedIDs))
		for _, id := range batch.StaleRejectedIDs {
			staleRejectedSet[id] = struct{}{}
		}
		retiredSet := make(map[string]struct{}, len(batch.RetiredIDs))
		for _, id := range batch.RetiredIDs {
			retiredSet[id] = struct{}{}
		}
		skippedDependencies := make([]string, 0, len(batch.SkippedDependencies))
		for _, dep := range batch.SkippedDependencies {
			skippedDependencies = append(skippedDependencies, fmt.Sprintf("%s -> %s: %s", dep.IssueID, dep.DependsOnID, dep.Reason))
		}
		applyImportOutcome(&result, assembleImportResult(issues, plan.StaleSkippedIDs, plan.ChangePlan, staleRejectedSet, retiredSet, skippedDependencies))
	} else {
		result.Skipped += len(plan.StaleSkippedIDs)
		result.StaleSkippedIDs = append(result.StaleSkippedIDs, plan.StaleSkippedIDs...)
	}

	return renderImportOutcome(result, source, dedupHits)
}

// planProxiedImport is planClassicImport's proxied twin (bd-8mz4zq /
// GH#4492): the same stale-filter-then-screen order over the same shared
// code, with the lookup and the vocabulary both served by the unit of work.
func planProxiedImport(ctx context.Context, uw uow.UnitOfWork, issues []*types.Issue, allowStale, classify bool, sources map[*types.Issue]importRecordSource) (*importRunPlan, error) {
	plan := &importRunPlan{}
	if allowStale {
		if classify {
			cp, err := classifyImportIssuesExistence(ctx, uw.IssueUseCase(), issues)
			if err != nil {
				return nil, err
			}
			plan.ChangePlan = cp
		}
		plan.Issues = issues
	} else {
		filtered, skippedIDs, cp, err := filterStaleImportIssues(ctx, uw.IssueUseCase(), issues)
		if err != nil {
			return nil, err
		}
		plan.Issues, plan.StaleSkippedIDs, plan.ChangePlan = filtered, skippedIDs, cp
	}
	kept, rejects, err := screenImportRows(ctx, uowImportVocabulary{uw: uw}, plan.Issues, sources)
	if err != nil {
		return nil, err
	}
	plan.Issues = kept
	plan.Rejects = rejects
	return plan, nil
}
