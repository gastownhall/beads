package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// BatchContext holds per-batch state read once and reused for every issue.
type BatchContext struct {
	CustomStatuses  []string
	CustomTypes     []string
	ConfigPrefix    string
	AllowedPrefixes string
	Opts            storage.BatchCreateOptions
}

// NewBatchContext reads config from the database and returns a BatchContext.
func NewBatchContext(ctx context.Context, tx *sql.Tx, opts storage.BatchCreateOptions) (*BatchContext, error) {
	customStatuses, err := GetCustomStatusesTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := ResolveCustomTypesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom types: %w", err)
	}
	configPrefix, err := ReadConfigPrefix(ctx, tx)
	if err != nil {
		return nil, err
	}
	var allowedPrefixes string
	_ = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "allowed_prefixes").Scan(&allowedPrefixes)

	return &BatchContext{
		CustomStatuses:  customStatuses,
		CustomTypes:     customTypes,
		ConfigPrefix:    configPrefix,
		AllowedPrefixes: allowedPrefixes,
		Opts:            opts,
	}, nil
}

func CreateIssueInTx(ctx context.Context, tx *sql.Tx, bc *BatchContext, issue *types.Issue, actor string) error {
	_, err := CreateIssueInTxWithResult(ctx, tx, bc, issue, actor)
	return err
}

// CreateIssueResult reports the tables actually written by CreateIssueInTx.
type CreateIssueResult struct {
	ChangedTables map[string]bool
	// StaleRejected reports that the RejectStaleUpserts guard kept the stored
	// row: nothing was written, and the issue's aux data must not be
	// persisted by later batch stages either (bd-578h9.8).
	StaleRejected bool
}

func (r *CreateIssueResult) markChanged(table string) {
	if table == "" {
		return
	}
	if r.ChangedTables == nil {
		r.ChangedTables = map[string]bool{}
	}
	r.ChangedTables[table] = true
}

func mergeChangedTables(dst map[string]bool, src map[string]bool) map[string]bool {
	for table := range src {
		if dst == nil {
			dst = map[string]bool{}
		}
		dst[table] = true
	}
	return dst
}

func CreateIssueInTxWithResult(ctx context.Context, tx *sql.Tx, bc *BatchContext, issue *types.Issue, actor string) (CreateIssueResult, error) {
	var result CreateIssueResult
	if err := PrepareIssueForInsert(issue, bc.CustomStatuses, bc.CustomTypes); err != nil {
		return result, err
	}

	issueTable, eventTable := TableRouting(issue)

	if issue.ID == "" {
		prefix := bc.ConfigPrefix
		if issue.PrefixOverride != "" {
			prefix = issue.PrefixOverride
		} else if issue.IDPrefix != "" {
			prefix = bc.ConfigPrefix + "-" + issue.IDPrefix
		} else if IsWisp(issue) {
			prefix = bc.ConfigPrefix + "-wisp"
		}
		var err error
		issue.ID, err = GenerateIssueIDInTable(ctx, tx, issueTable, prefix, issue, actor)
		if err != nil {
			return result, fmt.Errorf("failed to generate issue ID: %w", err)
		}
	} else if !bc.Opts.SkipPrefixValidation {
		if err := ValidateIssueIDPrefix(issue.ID, bc.ConfigPrefix, bc.AllowedPrefixes); err != nil {
			return result, fmt.Errorf("prefix validation failed for %s: %w", issue.ID, err)
		}
	}

	if skip, err := CheckOrphan(ctx, tx, issue, issueTable, bc.Opts.OrphanHandling); err != nil {
		return result, err
	} else if skip {
		return result, nil
	}

	isNew, staleRejected, err := InsertIssueIfNew(ctx, tx, issueTable, issue, bc.Opts)
	if err != nil {
		return result, err
	}
	if staleRejected {
		// The stored row is strictly newer than this snapshot: nothing was
		// written, and the snapshot's labels/comments belong to the older
		// version, so they must not merge in either (bd-578h9.8).
		result.StaleRejected = true
		if bc.Opts.OnStaleRejected != nil {
			bc.Opts.OnStaleRejected(issue.ID)
		}
		return result, nil
	}
	result.markChanged(issueTable)

	if isNew {
		if err := RecordEventInTable(ctx, tx, eventTable, issue.ID, types.EventCreated, actor, ""); err != nil {
			return result, fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
		result.markChanged(eventTable)
	}

	labelResult, err := PersistLabels(ctx, tx, issue, actor, eventTable)
	if err != nil {
		return result, err
	}
	result.ChangedTables = mergeChangedTables(result.ChangedTables, labelResult.ChangedTables)
	commentResult, err := PersistComments(ctx, tx, issue)
	if err != nil {
		return result, err
	}
	result.ChangedTables = mergeChangedTables(result.ChangedTables, commentResult.ChangedTables)
	return result, nil
}

// CreateIssuesResult reports side effects that callers need for selective
// Dolt staging after CreateIssuesInTxWithResult returns.
type CreateIssuesResult struct {
	ChangedTables             map[string]bool
	ChangedChildCounterTables map[string]bool
}

func (r *CreateIssuesResult) markChanged(table string) {
	if table == "" {
		return
	}
	if r.ChangedTables == nil {
		r.ChangedTables = map[string]bool{}
	}
	r.ChangedTables[table] = true
}

func (r *CreateIssuesResult) merge(changed map[string]bool) {
	r.ChangedTables = mergeChangedTables(r.ChangedTables, changed)
}

func CreateIssuesInTx(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	_, err := CreateIssuesInTxWithResult(ctx, tx, issues, actor, opts)
	return err
}

// CreateIssuesInTxWithResult creates issues and reports tables whose writes are
// only knowable after SQL reconciliation, such as child counter advances.
func CreateIssuesInTxWithResult(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) (CreateIssuesResult, error) {
	filteredIssues, err := filterCreateIssuesMixedBucketDependencies(issues, opts)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	issues = filteredIssues

	bc, err := NewBatchContext(ctx, tx, opts)
	if err != nil {
		return CreateIssuesResult{}, err
	}

	result := CreateIssuesResult{}
	accepted := issues[:0:0]
	for _, issue := range issues {
		issueResult, err := CreateIssueInTxWithResult(ctx, tx, bc, issue, actor)
		if err != nil {
			return CreateIssuesResult{}, err
		}
		result.merge(issueResult.ChangedTables)
		if issueResult.StaleRejected {
			continue // stale snapshot: keep its deps out of the batch too
		}
		accepted = append(accepted, issue)
	}
	issues = accepted

	depResult, err := PersistDependenciesWithOptionsResult(ctx, tx, issues, actor, opts)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	result.merge(depResult.ChangedTables)

	changedCounters, err := ReconcileChildCounters(ctx, tx, issues)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	result.ChangedChildCounterTables = changedCounters
	for table := range changedCounters {
		result.markChanged(table)
	}
	issueIDs, wispIDs := createBlockedRecomputeIDs(issues)
	if err := RecomputeIsBlockedInTx(ctx, tx, issueIDs, wispIDs); err != nil {
		return CreateIssuesResult{}, err
	}
	if len(issueIDs) > 0 {
		result.markChanged("issues")
	}
	if len(wispIDs) > 0 {
		result.markChanged("wisps")
	}
	return result, nil
}

// CreateIssueDirtyTables returns the regular Dolt tables CreateIssueInTx may
// dirty for the given issue. Wisp tables are intentionally omitted because they
// are Dolt-ignored and cannot be staged.
func CreateIssueDirtyTables(ctx context.Context, issue *types.Issue, result CreateIssueResult) map[string]bool {
	dirty := stageableChangedTables(result.ChangedTables)
	if issue == nil {
		return dirty
	}
	if parentID, childNum, ok := ParseHierarchicalID(issue.ID); ok &&
		storage.HasReservedChildCounter(ctx, parentID, childNum) {
		dirty["child_counters"] = true
	}
	return dirty
}

// CreateIssuesDirtyTables returns the regular Dolt tables CreateIssuesInTx may
// dirty, including child counters that reconciliation actually advanced.
func CreateIssuesDirtyTables(ctx context.Context, issues []*types.Issue, result CreateIssuesResult) map[string]bool {
	dirty := stageableChangedTables(result.ChangedTables)
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if parentID, childNum, ok := ParseHierarchicalID(issue.ID); ok &&
			storage.HasReservedChildCounter(ctx, parentID, childNum) {
			dirty["child_counters"] = true
		}
	}
	return dirty
}

func stageableChangedTables(changed map[string]bool) map[string]bool {
	dirty := map[string]bool{}
	for table := range changed {
		if table == "wisps" || strings.HasPrefix(table, "wisp_") {
			continue
		}
		dirty[table] = true
	}
	return dirty
}

// ValidateCreateIssuesMixedBucketDependencies rejects same-batch dependency
// edges between regular issues and wisps. Dependencies are stored in separate
// backing tables per bucket, so a batch cannot create both ends atomically when
// the edge crosses buckets.
func ValidateCreateIssuesMixedBucketDependencies(issues []*types.Issue) error {
	_, err := filterCreateIssuesMixedBucketDependencies(issues, storage.BatchCreateOptions{})
	return err
}

func filterCreateIssuesMixedBucketDependencies(issues []*types.Issue, opts storage.BatchCreateOptions) ([]*types.Issue, error) {
	batchWispByID := make(map[string]bool, len(issues))
	hasRegular := false
	hasWisp := false
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		isWisp := IsWisp(issue)
		if isWisp {
			hasWisp = true
		} else {
			hasRegular = true
		}
		if issue.ID != "" {
			batchWispByID[issue.ID] = isWisp
		}
	}
	if !hasRegular || !hasWisp {
		return issues, nil
	}

	var filteredIssues []*types.Issue
	for issueIndex, issue := range issues {
		if issue == nil {
			continue
		}
		var keptDeps []*types.Dependency
		filteredDeps := false
		for depIndex, dep := range issue.Dependencies {
			if dep == nil {
				if filteredDeps {
					keptDeps = append(keptDeps, dep)
				}
				continue
			}
			sourceID := issue.ID
			sourceIsWisp := IsWisp(issue)
			if dep.IssueID != "" {
				sourceID = dep.IssueID
				if isWisp, ok := batchWispByID[sourceID]; ok {
					sourceIsWisp = isWisp
				}
			}
			targetIsWisp, targetInBatch := batchWispByID[dep.DependsOnID]
			if targetInBatch && sourceIsWisp != targetIsWisp {
				if !opts.SkipDependencyValidationErrors {
					return nil, fmt.Errorf("mixed regular/wisp CreateIssues batch cannot include cross-bucket dependency %s -> %s; create the issues first, then add the in-batch dependency after both issues exist", sourceID, dep.DependsOnID)
				}
				if !filteredDeps {
					keptDeps = append([]*types.Dependency(nil), issue.Dependencies[:depIndex]...)
					filteredDeps = true
				}
				recordSkippedDependencyEdge(opts, sourceID, dep.DependsOnID, "cross-bucket dependency between regular issue and wisp in the same batch")
				continue
			}
			if filteredDeps {
				keptDeps = append(keptDeps, dep)
			}
		}
		if filteredDeps {
			if filteredIssues == nil {
				filteredIssues = append([]*types.Issue(nil), issues...)
			}
			issueCopy := *issue
			issueCopy.Dependencies = keptDeps
			filteredIssues[issueIndex] = &issueCopy
		}
	}
	if filteredIssues != nil {
		return filteredIssues, nil
	}
	return issues, nil
}

func createBlockedRecomputeIDs(issues []*types.Issue) ([]string, []string) {
	issueSeen := make(map[string]bool, len(issues))
	wispSeen := make(map[string]bool, len(issues))
	issueIDs := make([]string, 0, len(issues))
	wispIDs := make([]string, 0, len(issues))
	add := func(id string, isWisp bool) {
		if id == "" {
			return
		}
		if isWisp {
			if !wispSeen[id] {
				wispSeen[id] = true
				wispIDs = append(wispIDs, id)
			}
			return
		}
		if !issueSeen[id] {
			issueSeen[id] = true
			issueIDs = append(issueIDs, id)
		}
	}
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		isWisp := IsWisp(issue)
		add(issue.ID, isWisp)
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			src := dep.IssueID
			if src == "" {
				src = issue.ID
			}
			add(src, isWisp)
		}
	}
	return issueIDs, wispIDs
}

// PrepareIssueForInsert normalizes timestamps, validates, and computes the content hash.
func PrepareIssueForInsert(issue *types.Issue, customStatuses, customTypes []string) error {
	if err := ValidateMetadataIfConfigured(issue.Metadata); err != nil {
		return fmt.Errorf("metadata validation failed for issue %s: %w", issue.ID, err)
	}

	// Normalize timestamps to UTC, defaulting to now.
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

	// Ensure closed issues have a closed_at timestamp.
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed for issue %s: %w", issue.ID, err)
	}
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}
	return nil
}

// ValidateIssueIDPrefix validates that the issue ID matches the configured prefix
// or any of the allowed_prefixes.
func ValidateIssueIDPrefix(id, prefix, allowedPrefixes string) error {
	if strings.HasPrefix(id, prefix+"-") {
		return nil
	}
	if allowedPrefixes != "" {
		for _, allowed := range strings.Split(allowedPrefixes, ",") {
			allowed = strings.TrimSpace(allowed)
			if allowed != "" && strings.HasPrefix(id, allowed+"-") {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: issue ID %s does not match configured prefix %s", storage.ErrPrefixMismatch, id, prefix)
}

// ParseHierarchicalID checks if an ID is hierarchical (e.g., "bd-abc.1")
// and returns the parent ID and child number.
func ParseHierarchicalID(id string) (parentID string, childNum int, ok bool) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot == -1 {
		return "", 0, false
	}
	parentID = id[:lastDot]
	var num int
	if _, err := fmt.Sscanf(id[lastDot+1:], "%d", &num); err != nil {
		return "", 0, false
	}
	return parentID, num, true
}

// AllWisps returns true if every issue in the slice should be routed to the
// wisps table (i.e., is ephemeral or no-history). Used to gate the fast path
// that skips Dolt versioning in batch creates.
func AllWisps(issues []*types.Issue) bool {
	for _, issue := range issues {
		if !issue.Ephemeral && !issue.NoHistory {
			return false
		}
	}
	return true
}

// CheckOrphan handles orphan detection for hierarchical IDs.
// Returns (skip=true, nil) if the issue should be skipped.
//
//nolint:gosec // G201: table is a hardcoded constant
func CheckOrphan(ctx context.Context, tx *sql.Tx, issue *types.Issue, issueTable string, handling storage.OrphanHandling) (skip bool, err error) {
	if issue.ID == "" {
		return false, nil
	}
	parentID, _, ok := ParseHierarchicalID(issue.ID)
	if !ok {
		return false, nil
	}

	var parentCount int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, issueTable), parentID).Scan(&parentCount); err != nil {
		return false, fmt.Errorf("failed to check parent existence: %w", err)
	}
	if parentCount > 0 {
		return false, nil
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

// InsertIssueIfNew inserts the issue and returns whether it was genuinely new,
// and whether the RejectStaleUpserts guard rejected it.
//
// When opts.ConflictSkip is true and an issue with the same ID already exists,
// the row is left untouched (no UPSERT) and isNew is false. This is the
// auto-import upgrade-recovery guarantee (GH#3955): even if the emptiness
// guard in maybeAutoImportJSONL regresses, a stale issues.jsonl can never
// overwrite live rows — worst case is a no-op. Otherwise the INSERT … ON
// DUPLICATE KEY UPDATE runs, so explicit `bd import` keeps UPSERT semantics;
// with opts.RejectStaleUpserts the update half is conditional on the incoming
// row being strictly newer than the stored one (bd-pkim8, bd-hj85c).
// Staleness is decided by an explicit in-transaction read (stored updated_at
// strictly newer ⇒ rejected) so callers can skip aux persistence and count
// the row as skipped instead of created (bd-578h9.8). Equal-timestamp rows
// are deliberately NOT rejected here, even though the ODKU's
// VALUES(updated_at) > updated_at condition keeps every stored column for
// them: updated_at has second granularity, so a tie may be two distinct
// same-second updates — the local row must win the tie (an incoming row with
// an empty notes field must not wipe local notes), but its aux data
// (labels/comments/deps, which never bump updated_at) still merges
// additively (bd-hj85c).
//
//nolint:gosec // G201: table is a hardcoded constant
func InsertIssueIfNew(ctx context.Context, tx *sql.Tx, issueTable string, issue *types.Issue, opts storage.BatchCreateOptions) (isNew bool, staleRejected bool, err error) {
	var existingCount int
	if issue.ID != "" {
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, issueTable), issue.ID).Scan(&existingCount); err != nil {
			return false, false, fmt.Errorf("failed to check issue existence for %s: %w", issue.ID, err)
		}
	}
	if opts.ConflictSkip && existingCount > 0 {
		return false, false, nil // issue already exists — skip, never overwrite
	}
	if opts.RejectStaleUpserts && existingCount > 0 {
		var storedNewer int
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ? AND updated_at > ?`, issueTable), issue.ID, issue.UpdatedAt).Scan(&storedNewer); err != nil {
			return false, false, fmt.Errorf("failed to check issue staleness for %s: %w", issue.ID, err)
		}
		if storedNewer > 0 {
			// The conditional ODKU would keep every stored column anyway;
			// skipping the no-op insert makes the rejection observable.
			return false, true, nil
		}
	}
	if err := insertIssueIntoTable(ctx, tx, issueTable, issue, opts.RejectStaleUpserts); err != nil {
		return false, false, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
	}
	return existingCount == 0, false, nil
}

func PersistLabels(ctx context.Context, tx *sql.Tx, issue *types.Issue, actor, eventTable string) (CreateIssueResult, error) {
	var result CreateIssueResult
	if len(issue.Labels) == 0 {
		return result, nil
	}
	labelTable := "labels"
	if IsWisp(issue) {
		labelTable = "wisp_labels"
	}
	seen := make(map[string]struct{}, len(issue.Labels))
	for _, label := range issue.Labels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		//nolint:gosec // G201: table is determined by ephemeral flag
		sqlResult, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT IGNORE INTO %s (issue_id, label)
			VALUES (?, ?)
		`, labelTable), issue.ID, label)
		if err != nil {
			return result, fmt.Errorf("failed to insert label %q for %s: %w", label, issue.ID, err)
		}
		rowsAffected, err := sqlResult.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("failed to check label insert result for %q on %s: %w", label, issue.ID, err)
		}
		if rowsAffected == 0 {
			continue
		}
		result.markChanged(labelTable)
		comment := "Added label: " + label
		//nolint:gosec // G201: eventTable is determined by ephemeral flag
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (id, issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?, ?)
		`, eventTable), NewEventID(), issue.ID, types.EventLabelAdded, actor, comment); err != nil {
			return result, fmt.Errorf("failed to record label event %q for %s: %w", label, issue.ID, err)
		}
		result.markChanged(eventTable)
	}
	return result, nil
}

func PersistComments(ctx context.Context, tx *sql.Tx, issue *types.Issue) (CreateIssueResult, error) {
	var result CreateIssueResult
	if len(issue.Comments) == 0 {
		return result, nil
	}
	commentTable := "comments"
	if IsWisp(issue) {
		commentTable = "wisp_comments"
	}
	for _, comment := range issue.Comments {
		createdAt := comment.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		// Check for existing identical comment to prevent duplicates on re-import.
		// The UUID PK means ON DUPLICATE KEY UPDATE would never fire,
		// so we do an explicit existence check instead.
		var exists int
		//nolint:gosec // G201: table is determined by ephemeral flag
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
				SELECT COUNT(*) FROM %s
				WHERE issue_id = ? AND author = ? AND created_at = ? AND text = ?
			`, commentTable), issue.ID, comment.Author, createdAt, comment.Text).Scan(&exists); err != nil {
			return result, fmt.Errorf("failed to check comment existence for %s: %w", issue.ID, err)
		}
		if exists > 0 {
			continue
		}
		commentID := comment.ID
		if commentID == "" {
			commentID = uuid.Must(uuid.NewV7()).String()
			comment.ID = commentID
		}
		//nolint:gosec // G201: table is determined by ephemeral flag
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (id, issue_id, author, text, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, commentTable), commentID, issue.ID, comment.Author, comment.Text, createdAt)
		if err != nil {
			return result, fmt.Errorf("failed to insert comment for %s: %w", issue.ID, err)
		}
		result.markChanged(commentTable)
	}
	return result, nil
}

func PersistDependencies(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string) error {
	_, err := PersistDependenciesWithResult(ctx, tx, issues, actor)
	return err
}

func PersistDependenciesWithResult(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string) (CreateIssueResult, error) {
	return PersistDependenciesWithOptionsResult(ctx, tx, issues, actor, storage.BatchCreateOptions{})
}

func PersistDependenciesWithOptionsResult(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) (CreateIssueResult, error) {
	var result CreateIssueResult
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}
		depTable := "dependencies"
		if IsWisp(issue) {
			depTable = "wisp_dependencies"
		}
		for _, dep := range issue.Dependencies {
			// Default IssueID to the owning issue when not pre-set (e.g.,
			// markdown bulk create where the ID is auto-generated).
			if dep.IssueID == "" {
				dep.IssueID = issue.ID
			}

			kind := ClassifyDepTarget(ctx, tx, dep, false)

			if kind != DepTargetExternal {
				lookupTable := "issues"
				if kind == DepTargetWisp {
					lookupTable = "wisps"
				}
				var exists int
				//nolint:gosec // G201: lookupTable is one of two hardcoded constants
				if err := tx.QueryRowContext(ctx,
					fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", lookupTable),
					dep.DependsOnID).Scan(&exists); err != nil {
					if err == sql.ErrNoRows {
						recordSkippedDependency(opts, dep, "target not found")
						continue
					}
					return result, fmt.Errorf("failed to check dependency target %s for %s: %w", dep.DependsOnID, dep.IssueID, err)
				}
			}

			if err := CheckDependencyCycleInTx(ctx, tx, dep, nil); err != nil {
				if opts.SkipDependencyValidationErrors {
					recordSkippedDependency(opts, dep, err.Error())
					continue
				}
				return result, fmt.Errorf("invalid dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}

			createdAt := dep.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			// Deterministic id from (issue_id, target) keeps bulk-imported edges
			// merge-safe across clones — two clones importing the same JSONL get the
			// same primary key, not two random UUIDs that collide on uk_dep_* (#4259).
			createdBy := dependencyCreatedBy(dep, actor)
			//nolint:gosec // G201: depTable is one of two hardcoded constants; target column from DepTargetKind.Column()
			sqlResult, err := tx.ExecContext(ctx, fmt.Sprintf(`
					INSERT INTO %s (id, issue_id, %s, type, created_by, created_at)
					VALUES (?, ?, ?, ?, ?, ?)
					ON DUPLICATE KEY UPDATE type = type
				`, depTable, kind.Column()), depid.New(dep.IssueID, dep.DependsOnID), dep.IssueID, dep.DependsOnID, dep.Type, createdBy, createdAt)
			if err != nil {
				return result, fmt.Errorf("failed to insert dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
			rowsAffected, err := sqlResult.RowsAffected()
			if err != nil {
				return result, fmt.Errorf("failed to check dependency insert result for %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
			if rowsAffected > 0 {
				result.markChanged(depTable)
			}
		}
	}
	return result, nil
}

// dependencyCreatedBy returns the author stamped on a dependency edge.
// Import/restore paths populate dep.CreatedBy from JSONL; interactive
// creation leaves it empty and falls back to the current actor.
func dependencyCreatedBy(dep *types.Dependency, actor string) string {
	if dep != nil && dep.CreatedBy != "" {
		return dep.CreatedBy
	}
	return actor
}

func recordSkippedDependency(opts storage.BatchCreateOptions, dep *types.Dependency, reason string) {
	if dep == nil {
		return
	}
	recordSkippedDependencyEdge(opts, dep.IssueID, dep.DependsOnID, reason)
}

func recordSkippedDependencyEdge(opts storage.BatchCreateOptions, issueID, dependsOnID, reason string) {
	if opts.OnSkippedDependency == nil {
		return
	}
	opts.OnSkippedDependency(issueID, dependsOnID, reason)
}

func ReconcileChildCounters(ctx context.Context, tx *sql.Tx, issues []*types.Issue) (map[string]bool, error) {
	type bucket struct {
		maxChild int
		isWisp   bool
		known    bool
	}
	parents := make(map[string]*bucket)
	var changed map[string]bool

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if IsWisp(issue) {
			if b, ok := parents[issue.ID]; ok {
				b.isWisp, b.known = true, true
			} else {
				parents[issue.ID] = &bucket{isWisp: true, known: true}
			}
		}
	}

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		parentID, childNum, ok := ParseHierarchicalID(issue.ID)
		if !ok {
			continue
		}
		b, exists := parents[parentID]
		if !exists {
			b = &bucket{}
			parents[parentID] = b
		}
		if childNum > b.maxChild {
			b.maxChild = childNum
		}
	}

	for parentID, b := range parents {
		if b.maxChild == 0 {
			continue
		}
		if !b.known {
			b.isWisp = IsActiveWispInTx(ctx, tx, parentID)
		}
		table := "child_counters"
		if b.isWisp {
			table = "wisp_child_counters"
		}
		var current int
		//nolint:gosec // G201: table is one of two hardcoded constants.
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT last_child FROM %s WHERE parent_id = ?
		`, table), parentID).Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("failed to read child counter for %s: %w", parentID, err)
		}
		if err == nil && current >= b.maxChild {
			continue
		}
		//nolint:gosec // G201: table is one of two hardcoded constants.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (parent_id, last_child) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, ?)
		`, table), parentID, b.maxChild, b.maxChild); err != nil {
			return nil, fmt.Errorf("failed to reconcile child counter for %s: %w", parentID, err)
		}
		if changed == nil {
			changed = map[string]bool{}
		}
		changed[table] = true
	}
	return changed, nil
}
