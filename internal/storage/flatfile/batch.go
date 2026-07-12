package flatfile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// This file implements batch issue creation, mirroring
// issueops.CreateIssuesInTxWithResult. The SQL backends get all-or-nothing
// semantics from transaction rollback; the flat-file store validates the
// ENTIRE batch in memory first and runs the per-file writes under the
// pre-image journal (journal.go), so a mid-batch I/O failure rolls back to
// the pre-batch state instead of persisting a partial import.

// plannedIssue is the write-phase plan for one input issue.
type plannedIssue struct {
	final     *types.Issue        // issue JSON to write (nil when nothing is written)
	incoming  *types.Issue        // the caller's issue (source of deps/comments)
	isNew     bool                // records a 'created' event
	addLabels []string            // labels genuinely added (one label_added event each)
	stale     bool                // RejectStaleUpserts kept the stored row; aux skipped
	deps      []*types.Dependency // inline edges to validate in the dependency pass
}

// CreateIssuesWithFullOptions creates a batch of issues with import-grade
// options. Mirrors sqlkit.CreateIssuesWithFullOptions + issueops
// CreateIssuesInTxWithResult: cross-bucket in-batch edges are rejected (or
// dropped and reported), existing rows follow upsert/ConflictSkip/
// RejectStaleUpserts semantics, hierarchical orphans follow OrphanHandling,
// and inline dependencies are persisted in a batch pass that skips missing
// targets and rejects self-dependencies and blocking cycles.
func (s *FlatFileStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if len(issues) == 0 {
		return nil
	}
	// bd import and repo-sync call this directly with no RunInTransaction
	// wrapper, while the SQL backends are internally tx-wrapped
	// (CreateIssuesInTxWithResult) and get all-or-nothing semantics from
	// rollback. Wrap the batch in RunInTransaction so the journal covers
	// every write and a mid-batch I/O failure (ENOSPC, EIO) restores the
	// pre-batch state. Inside an active transaction (txOwner match) the
	// outer journal already covers rollback — re-arming would clobber it,
	// and re-taking txMu would self-deadlock — so the body runs directly.
	if s.txOwner.Load() != goroutineID() {
		return s.RunInTransaction(ctx, "", func(storage.Transaction) error {
			return s.createIssuesBatch(ctx, issues, actor, opts)
		})
	}
	return s.createIssuesBatch(ctx, issues, actor, opts)
}

// createIssuesBatch is the batch body. The caller guarantees txMu's write
// half is held by this goroutine (RunInTransaction above, or an enclosing
// transaction callback), so lockWrite's txShared half is an owner-match
// no-op and only writeMu is taken here.
func (s *FlatFileStore) createIssuesBatch(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	// The whole plan-then-write batch runs under lockWrite so the validated
	// in-memory plan cannot be invalidated by a concurrent mutation. No
	// callee below takes writeMu — comment writes go through
	// importCommentLocked (the exported ImportComment takes lockWrite and
	// would self-deadlock), and events are append-only.
	defer s.lockWrite()()

	// All-wisp batches force Ephemeral (mirrors sqlkit).
	if issueops.AllWisps(issues) {
		for _, issue := range issues {
			if !issue.NoHistory {
				issue.Ephemeral = true
			}
		}
	}

	filtered, err := issueops.FilterCreateIssuesMixedBucketDependencies(issues, opts)
	if err != nil {
		return err
	}

	// A failed issue_prefix read (corrupt config_kv.json, EACCES) aborts the
	// batch with the real cause, mirroring issueops.NewBatchContext →
	// ReadConfigPrefix; swallowing it would misdiagnose every explicit ID as
	// a prefix-validation failure against an empty prefix. A missing key is
	// still ("", nil). The allowed_prefixes swallow IS parity: SQL discards
	// that scan error too.
	configPrefix, err := s.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}
	allowedPrefixes, _ := s.GetConfig(ctx, "allowed_prefixes")

	// Read once per batch and reused for every issue, mirroring
	// issueops.NewBatchContext.
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}

	// planned holds the final state of every batch-written issue, keyed by ID,
	// so later batch members see earlier ones (parents, dep targets).
	planned := make(map[string]*types.Issue)
	var plans []*plannedIssue

	for _, issue := range filtered {
		if issue == nil {
			continue
		}
		// Mirrors issueops.CreateIssueInTxWithResult: PrepareIssueForInsert
		// validates every batch member before anything is written.
		if err := prepareAndValidateIssue(issue, actor, customStatuses, customTypes); err != nil {
			return err
		}

		if issue.ID == "" {
			prefix := configPrefix
			if prefix == "" {
				return fmt.Errorf("flatfile: cannot generate ID: issue_prefix not configured")
			}
			if issue.PrefixOverride != "" {
				prefix = issue.PrefixOverride
			} else if issue.IDPrefix != "" {
				prefix = prefix + "-" + issue.IDPrefix
			} else if issueops.IsWisp(issue) {
				prefix = prefix + "-wisp"
			}
			// planned makes ID generation batch-aware: a candidate claimed by
			// an earlier batch member advances the nonce instead of colliding
			// (mirrors the SQL in-transaction collision SELECT).
			id, err := s.generateID(ctx, prefix, issue, actor, planned)
			if err != nil {
				return err
			}
			issue.ID = id
		} else if !opts.SkipPrefixValidation {
			if err := issueops.ValidateIssueIDPrefix(issue.ID, configPrefix, allowedPrefixes); err != nil {
				return fmt.Errorf("prefix validation failed for %s: %w", issue.ID, err)
			}
		}

		stored, err := s.batchStoredIssue(issue.ID, planned)
		if err != nil {
			return err
		}

		// Mirror issueops.checkCrossTableIDCollision: issues and wisps share
		// one ID space but live in different buckets, so an incoming issue
		// whose ID is stored in the sibling bucket is a hard error — or,
		// under ConflictSkip, a FULL skip that persists no columns, labels,
		// comments, or dependencies (SQL returns before PersistLabels/
		// PersistComments). Upserting over it would silently flip the bucket.
		// SQL runs this BEFORE CheckOrphan (CreateIssueInTxWithResult), so a
		// colliding hierarchical ID with a missing parent resolves as a
		// collision, not an orphan.
		if stored != nil && issueops.IsWisp(stored) != issueops.IsWisp(issue) {
			if opts.ConflictSkip {
				plans = append(plans, &plannedIssue{incoming: issue})
				continue
			}
			sibling := "wisps"
			if issueops.IsWisp(issue) {
				sibling = "issues"
			}
			return fmt.Errorf("cannot create %q: ID already exists in the %s table (issues and wisps share one ID space)", issue.ID, sibling)
		}

		// Orphan handling for hierarchical IDs (mirrors issueops.CheckOrphan,
		// which SQL runs after the cross-table collision check).
		if skip, err := s.checkBatchOrphan(issue, planned, opts.OrphanHandling); err != nil {
			return err
		} else if skip {
			plans = append(plans, &plannedIssue{incoming: issue})
			continue
		}

		plan := &plannedIssue{incoming: issue, deps: issue.Dependencies}
		switch {
		case stored == nil:
			final := *issue
			final.Dependencies = nil
			// Comments live in the comments/ store (imported in the write
			// phase); embedding them in the issue file would leave a stale
			// duplicate that never sees later comment writes.
			final.Comments = nil
			final.Labels = dedupeLabels(issue.Labels)
			plan.final = &final
			plan.isNew = true
			plan.addLabels = final.Labels
		case opts.ConflictSkip:
			// Row untouched; aux (labels) still merges additively.
			plan.final, plan.addLabels = mergeLabels(stored, issue.Labels)
		case opts.RejectStaleUpserts && stored.UpdatedAt.After(issue.UpdatedAt):
			// Stored row strictly newer: reject, and keep the snapshot's aux
			// data (labels/comments/deps) out of the batch too (bd-578h9.8).
			plan.stale = true
			plan.deps = nil
		case opts.RejectStaleUpserts && !issue.UpdatedAt.After(stored.UpdatedAt):
			// Same-second tie: the stored columns win, aux merges additively.
			plan.final, plan.addLabels = mergeLabels(stored, issue.Labels)
		default:
			// Plain upsert: incoming columns win; stored aux merges additively.
			final := *issue
			final.Dependencies = stored.Dependencies
			final.Comments = nil // comments/ store only, as above
			final.Labels, plan.addLabels = unionLabels(stored.Labels, issue.Labels)
			plan.final = &final
		}

		if plan.final != nil {
			planned[issue.ID] = plan.final
		}
		plans = append(plans, plan)
	}

	extraDeps, err := s.planBatchDependencies(plans, planned, actor, opts)
	if err != nil {
		return err
	}

	// Write phase: validation passed; the only remaining failures are I/O
	// errors, and every write below is journal-aware (writeIssue/atomicWrite,
	// appendEvent, writeCommentFile), so an error here rolls the whole batch
	// back in the enclosing transaction.
	for _, plan := range plans {
		if plan.stale {
			if opts.OnStaleRejected != nil {
				opts.OnStaleRejected(plan.incoming.ID)
			}
			continue
		}
		if plan.final == nil {
			continue // orphan-skipped
		}
		if err := s.writeIssue(plan.final); err != nil {
			return err
		}
		if plan.isNew {
			if err := s.recordEvent(plan.final.ID, types.EventCreated, actor, "", ""); err != nil {
				return err
			}
		}
		for _, label := range plan.addLabels {
			if err := s.recordCommentEvent(plan.final.ID, issueops.IsWisp(plan.final), types.EventLabelAdded, actor, "Added label: "+label); err != nil {
				return err
			}
		}
		// Comments: mirror issueops.PersistComments — skip a comment identical
		// to an existing one (author, created_at, text: re-import dedup),
		// preserve the incoming comment ID, and mint one only when absent.
		if len(plan.incoming.Comments) > 0 {
			existing, err := s.GetIssueComments(ctx, plan.final.ID)
			if err != nil {
				return err
			}
			for _, comment := range plan.incoming.Comments {
				createdAt := comment.CreatedAt
				if createdAt.IsZero() {
					createdAt = time.Now().UTC()
				}
				if hasIdenticalComment(existing, comment.Author, comment.Text, createdAt) {
					continue
				}
				c := *comment
				c.IssueID = plan.final.ID
				c.CreatedAt = createdAt
				if err := s.importCommentLocked(&c); err != nil {
					return err
				}
				existing = append(existing, &c)
			}
		}
	}
	// Edges whose source is outside the batch: read-modify-write.
	for sourceID, deps := range extraDeps {
		issue, err := s.readIssue(sourceID)
		if err != nil {
			return err
		}
		issue.Dependencies = append(issue.Dependencies, deps...)
		if err := s.writeIssue(issue); err != nil {
			return err
		}
	}
	return nil
}

// hasIdenticalComment mirrors the duplicate check in issueops.PersistComments:
// a comment matching an existing one's author, text, and created_at is a
// re-import and must not be written twice.
func hasIdenticalComment(existing []*types.Comment, author, text string, createdAt time.Time) bool {
	for _, e := range existing {
		if e.Author == author && e.Text == text && e.CreatedAt.Equal(createdAt) {
			return true
		}
	}
	return false
}

// CreateIssues creates multiple issues with default batch options
// (mirrors sqlkit.Store.CreateIssues).
func (s *FlatFileStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return s.CreateIssuesWithFullOptions(ctx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling: storage.OrphanAllow,
	})
}

// prepareAndValidateIssue mirrors issueops.PrepareIssueForInsert: metadata
// schema validation, then normalization defaults, then full field validation
// against custom statuses/types. IssueType additionally defaults to task via
// types.SetDefaults — SQL callers (import, molecules) apply SetDefaults before
// reaching storage, while flatfile's exported methods are their own boundary.
func prepareAndValidateIssue(issue *types.Issue, actor string, customStatuses, customTypes []string) error {
	if err := issueops.ValidateMetadataIfConfigured(issue.Metadata); err != nil {
		return fmt.Errorf("metadata validation failed for issue %s: %w", issue.ID, err)
	}
	issue.SetDefaults()
	prepareIssueDefaults(issue, actor)
	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed for issue %s: %w", issue.ID, err)
	}
	return nil
}

// prepareIssueDefaults mirrors the normalization half of
// issueops.PrepareIssueForInsert plus flatfile's CreatedBy default.
func prepareIssueDefaults(issue *types.Issue, actor string) {
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	} else {
		issue.CreatedAt = issue.CreatedAt.UTC()
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	} else {
		issue.UpdatedAt = issue.UpdatedAt.UTC()
	}
	if issue.CreatedBy == "" {
		issue.CreatedBy = actor
	}
	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}
	// Ensure closed issues have a closed_at timestamp.
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}
}

// batchStoredIssue resolves the current state of an ID: an earlier batch
// member's planned write wins over the on-disk file.
func (s *FlatFileStore) batchStoredIssue(id string, planned map[string]*types.Issue) (*types.Issue, error) {
	if p, ok := planned[id]; ok {
		return p, nil
	}
	stored, err := s.readIssue(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return stored, nil
}

// checkBatchOrphan mirrors issueops.CheckOrphan: a hierarchical ID whose
// parent exists neither on disk nor earlier in the batch errors under
// OrphanStrict, is skipped under OrphanSkip, and proceeds otherwise.
func (s *FlatFileStore) checkBatchOrphan(issue *types.Issue, planned map[string]*types.Issue, handling storage.OrphanHandling) (skip bool, err error) {
	if issue.ID == "" {
		return false, nil
	}
	parentID, _, ok := issueops.ParseHierarchicalID(issue.ID)
	if !ok {
		return false, nil
	}
	if planned[parentID] != nil {
		return false, nil
	}
	if _, err := s.readIssue(parentID); err == nil {
		return false, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return false, err
	}
	switch handling {
	case storage.OrphanStrict:
		return false, fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
	case storage.OrphanSkip:
		return true, nil
	default: // OrphanAllow, OrphanResurrect
		return false, nil
	}
}

// planBatchDependencies validates every inline dependency against the
// simulated post-batch state (mirrors issueops.PersistDependencies...):
// missing targets are skipped and reported; self-dependencies and blocking
// cycles abort the batch unless SkipDependencyValidationErrors drops them.
// Validated edges are appended to their planned source, or returned keyed by
// source ID when the source is outside the batch.
func (s *FlatFileStore) planBatchDependencies(plans []*plannedIssue, planned map[string]*types.Issue, actor string, opts storage.BatchCreateOptions) (map[string][]*types.Dependency, error) {
	disk, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	// Simulated post-batch issue set: planned writes shadow disk files.
	combined := make([]*types.Issue, 0, len(disk)+len(planned))
	for _, issue := range disk {
		if planned[issue.ID] == nil {
			combined = append(combined, issue)
		}
	}
	for _, issue := range planned {
		combined = append(combined, issue)
	}

	exists := make(map[string]bool, len(combined))
	edges := make(map[string]map[string]bool)
	adj := make(map[string][]string)
	for _, issue := range combined {
		exists[issue.ID] = true
		for _, dep := range issue.Dependencies {
			if edges[issue.ID] == nil {
				edges[issue.ID] = make(map[string]bool)
			}
			edges[issue.ID][dep.DependsOnID] = true
			if isBlockingDepType(dep.Type) {
				adj[issue.ID] = append(adj[issue.ID], dep.DependsOnID)
			}
		}
	}

	extra := make(map[string][]*types.Dependency)
	for _, plan := range plans {
		if plan.stale || plan.final == nil {
			continue // stale or orphan-skipped: its edges stay out of the batch
		}
		for _, dep := range plan.deps {
			if dep == nil {
				continue
			}
			if dep.IssueID == "" {
				dep.IssueID = plan.incoming.ID
			}
			if !isExternalDepTarget(dep.DependsOnID) && !exists[dep.DependsOnID] {
				if opts.OnSkippedDependency != nil {
					opts.OnSkippedDependency(dep.IssueID, dep.DependsOnID, "target not found")
				}
				continue
			}
			if edges[dep.IssueID][dep.DependsOnID] {
				continue // duplicate edge: ON DUPLICATE KEY no-op
			}
			if cycleErr := checkBatchDependencyCycle(adj, dep); cycleErr != nil {
				if opts.SkipDependencyValidationErrors {
					if opts.OnSkippedDependency != nil {
						opts.OnSkippedDependency(dep.IssueID, dep.DependsOnID, cycleErr.Error())
					}
					continue
				}
				return nil, fmt.Errorf("invalid dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, cycleErr)
			}

			if dep.Metadata == "" {
				dep.Metadata = "{}"
			}
			if dep.CreatedAt.IsZero() {
				dep.CreatedAt = time.Now().UTC()
			}
			if dep.CreatedBy == "" {
				dep.CreatedBy = actor
			}
			if target, ok := planned[dep.IssueID]; ok {
				target.Dependencies = append(target.Dependencies, dep)
			} else {
				extra[dep.IssueID] = append(extra[dep.IssueID], dep)
			}
			if edges[dep.IssueID] == nil {
				edges[dep.IssueID] = make(map[string]bool)
			}
			edges[dep.IssueID][dep.DependsOnID] = true
			if isBlockingDepType(dep.Type) {
				adj[dep.IssueID] = append(adj[dep.IssueID], dep.DependsOnID)
			}
		}
	}
	return extra, nil
}

// checkBatchDependencyCycle mirrors issueops.CheckDependencyCycleInTx against
// the simulated batch graph.
func checkBatchDependencyCycle(adj map[string][]string, dep *types.Dependency) error {
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("cannot add self-dependency: %s cannot depend on itself", dep.IssueID)
	}
	if !isBlockingDepType(dep.Type) {
		return nil
	}
	if reachable(adj, dep.DependsOnID, dep.IssueID) {
		return fmt.Errorf("adding dependency would create a cycle")
	}
	return nil
}

// dedupeLabels returns labels with duplicates removed, preserving order.
func dedupeLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(labels))
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// mergeLabels merges incoming labels into a copy of the stored issue,
// returning the copy and the labels genuinely added.
func mergeLabels(stored *types.Issue, incoming []string) (*types.Issue, []string) {
	final := *stored
	merged, added := unionLabels(stored.Labels, incoming)
	final.Labels = merged
	return &final, added
}

// unionLabels returns stored ∪ incoming (order-preserving, deduped) and the
// incoming labels that were not already stored.
func unionLabels(stored, incoming []string) ([]string, []string) {
	seen := make(map[string]struct{}, len(stored)+len(incoming))
	merged := make([]string, 0, len(stored)+len(incoming))
	for _, l := range stored {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		merged = append(merged, l)
	}
	var added []string
	for _, l := range incoming {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		merged = append(merged, l)
		added = append(added, l)
	}
	if len(merged) == 0 {
		return nil, added
	}
	return merged, added
}
