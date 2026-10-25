package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// exportSource abstracts the storage reads `bd export` performs, so the one
// export body (runExportFromSource) serves both storage stacks:
//
//   - storeExportSource reads through the classic `store` global
//     (embedded / direct / server modes), preserving the exact pre-seam
//     call pattern.
//   - uowExportSource reads through a proxied-server unit of work, whose
//     domain use cases are plane-pinned (issues vs wisps tables) where the
//     classic bulk loaders partition internally — the impl queries both
//     planes with the full ID set and merges, so both modes read the same
//     rows regardless of which table an issue lives in.
//
// Everything downstream of these reads is shared code; the acceptance bar for
// the seam is byte-identical JSONL output across modes.
type exportSource interface {
	// GetInfraTypes returns the resolved infra-type set (config, YAML, or
	// hardcoded defaults). A nil/empty result makes the caller fall back to
	// domain.DefaultInfraTypes(), mirroring the pre-seam behavior.
	GetInfraTypes(ctx context.Context) map[string]bool
	// ExportIssues returns the full, untruncated export set for req.
	// Implementations MUST fail rather than return a truncated or shortened
	// set — export is a data-integrity path.
	ExportIssues(ctx context.Context, req issueops.ExportRequest) ([]*types.Issue, error)
	// GetConfig reads one config key from the database (used for the
	// export.exclude_owners owner-filter keys).
	GetConfig(ctx context.Context, key string) (string, error)
	// ExportMemories returns every persistent memory (`bd remember`), keyed
	// by the user key without the kv.memory. prefix. Like ExportIssues, it
	// MUST fail rather than return a partial set.
	ExportMemories(ctx context.Context) (map[string]string, error)
	// LoadExportRelations bulk-loads labels, dependency records, comments,
	// comment counts, and dependency counts for the searched issues.
	LoadExportRelations(ctx context.Context, issues []*types.Issue) (exportRelations, error)
	// WispPlaneIDs reports which of ids currently live in the WISPS table.
	// Export uses it to stamp the explicit "wisp_plane" marker on records
	// whose row flags are ambiguous: a no_history=true row is either an
	// unpromoted no-history wisp (wisps table) or a promoted one (durable
	// issues-table row that may still carry the stray flag), and only table
	// membership can tell them apart — import routes by the marker, so
	// mis-stamping a durable row would re-plane it and drop its relations
	// (bd-r9uce). Implementations must classify by table membership, never
	// by flags, and both modes must agree (byte-identity oracle).
	WispPlaneIDs(ctx context.Context, ids []string) (map[string]bool, error)
}

// storeWispPartitioner is the optional store capability WispPlaneIDs uses in
// classic mode. Both real stores (DoltStore, EmbeddedDoltStore) implement it;
// a store that does not simply gets no plane markers, which degrades to the
// data-safe side (import then routes bare no_history rows to the durable
// plane, where nothing is ever excluded from export).
type storeWispPartitioner interface {
	PartitionWispIDs(ctx context.Context, ids []string) (wispIDs, permIDs []string, err error)
}

// exportRelations carries the bulk-loaded relational data for the export set,
// keyed by issue ID.
type exportRelations struct {
	labels        map[string][]string
	deps          map[string][]*types.Dependency
	comments      map[string][]*types.Comment
	commentCounts map[string]int
	depCounts     map[string]*types.DependencyCounts
}

// errExportNoStore reports a classic-source read attempted with no store
// open. Callers of exportSource.GetConfig treat any error as "key unset",
// which reproduces the pre-seam `if store == nil` early return.
var errExportNoStore = errors.New("no store available")

// storeExportSource is the classic-mode exportSource over the `store` global.
type storeExportSource struct{}

func (storeExportSource) GetInfraTypes(ctx context.Context) map[string]bool {
	if store == nil {
		return nil
	}
	return store.GetInfraTypes(ctx)
}

func (src storeExportSource) ExportIssues(ctx context.Context, req issueops.ExportRequest) ([]*types.Issue, error) {
	return store.SearchIssues(ctx, "", exportIssueFilter(ctx, src, req))
}

func (storeExportSource) GetConfig(ctx context.Context, key string) (string, error) {
	if store == nil {
		return "", errExportNoStore
	}
	return store.GetConfig(ctx, key)
}

func (storeExportSource) ExportMemories(ctx context.Context) (map[string]string, error) {
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return nil, err
	}
	return memoriesFromConfig(allConfig), nil
}

// memoriesFromConfig extracts the persistent memories from a whole config
// table: its kv.memory.* rows, keyed by the user key.
func memoriesFromConfig(allConfig map[string]string) map[string]string {
	memories := map[string]string{}
	for k, v := range allConfig {
		if userKey, ok := strings.CutPrefix(k, kvPrefix+memoryPrefix); ok {
			memories[userKey] = v
		}
	}
	return memories
}

func (storeExportSource) LoadExportRelations(ctx context.Context, issues []*types.Issue) (exportRelations, error) {
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}

	// Individual bulk-load failures deliberately degrade to empty maps rather
	// than aborting the export — unchanged from the pre-seam classic behavior.
	labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
	allDeps, _ := store.GetDependencyRecordsForIssues(ctx, issueIDs)
	commentsMap, _ := store.GetCommentsForIssues(ctx, issueIDs)
	commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)
	depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

	return exportRelations{
		labels:        labelsMap,
		deps:          allDeps,
		comments:      commentsMap,
		commentCounts: commentCounts,
		depCounts:     depCounts,
	}, nil
}

func (storeExportSource) WispPlaneIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) == 0 || store == nil {
		return nil, nil
	}
	// The store global is decorator-wrapped (telemetry, hook firing); walk
	// Unwrap() down to the store that carries the partition capability.
	s := store
	var p storeWispPartitioner
	for {
		if partitioner, ok := s.(storeWispPartitioner); ok {
			p = partitioner
			break
		}
		u, ok := s.(interface{ Unwrap() storage.DoltStorage })
		if !ok {
			return nil, nil
		}
		s = u.Unwrap()
		if s == nil {
			return nil, nil
		}
	}
	wispIDs, _, err := p.PartitionWispIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(wispIDs))
	for _, id := range wispIDs {
		set[id] = true
	}
	return set, nil
}

// uowExportSource is the proxied-server exportSource over a unit of work. All
// reads run inside the single read transaction runExport opened, so the
// export is one consistent snapshot.
type uowExportSource struct {
	uw uow.UnitOfWork
}

func (s *uowExportSource) GetInfraTypes(ctx context.Context) map[string]bool {
	infra, err := s.uw.ConfigUseCase().GetInfraTypes(ctx)
	if err != nil {
		// Match the classic source's error-free signature: an unreadable
		// config degrades to the caller's DefaultInfraTypes() fallback.
		return nil
	}
	return infra
}

func (s *uowExportSource) ExportIssues(ctx context.Context, req issueops.ExportRequest) ([]*types.Issue, error) {
	filter := exportIssueFilter(ctx, s, req)
	page, err := s.uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}
	// Export builds its filter with Limit=0/MaxRows=0, so the whole result
	// arrives as one page and HasMore can only be true if a limit creeps back
	// in. Guard anyway: an export must fail loudly, never truncate silently
	// (domain.SearchPage carries no continuation cursor to loop with).
	if page.HasMore {
		return nil, fmt.Errorf("issue search returned a truncated page (limit=%d); export must never truncate", filter.Limit)
	}
	return page.Items, nil
}

func (s *uowExportSource) GetConfig(ctx context.Context, key string) (string, error) {
	return s.uw.ConfigUseCase().GetConfig(ctx, key)
}

func (s *uowExportSource) ExportMemories(ctx context.Context) (map[string]string, error) {
	allConfig, err := s.uw.ConfigUseCase().GetAllConfig(ctx)
	if err != nil {
		return nil, err
	}
	return memoriesFromConfig(allConfig), nil
}

// LoadExportRelations reproduces the classic bulk loaders over the
// plane-pinned domain use cases:
//
//   - Labels, comments, and comment counts: the classic loaders partition IDs
//     by wisps-table membership (issueops.PartitionWispIDsInTx). Row flags are
//     NOT a substitute for that partition — a promoted no-history wisp is a
//     durable issues-table row that (as wild data from the pre-bd-r9uce
//     promote) still carries NoHistory=true — so membership is read from the
//     wisps table itself (WispPlaneIDs), never inferred. The durable plane is
//     then queried with the FULL ID set and the wisp plane only with that
//     membership subset (wispReaderIDs), and the two are merged. Unreadable
//     membership fails OPEN to the full id list, so a nil set is never
//     mistaken for "no wisps" (wy-237yfi). The planes are ID-disjoint
//     (GH#4455 cross-table guard), so at most one plane returns rows for any
//     id and the merge cannot collide.
//   - Dependency records: domain GetIssueDependencyRecords already partitions
//     internally (same issueops helper), so one call covers both planes.
//   - Dependency counts: classic GetDependencyCountsInTx queries BOTH dep
//     tables for EVERY id and sums (a wisp blocking a durable issue
//     contributes to the durable issue's dependent_count), so both planes are
//     queried for the full ID set and summed here too.
//
// Wisp-plane reads tolerate a rig without the wisp tables (IsTableNotExist),
// mirroring the dependency-counts leg. The three wisp-relation readers below
// keep that blanket check: each is a single-table read, so blanket and
// table-specific are the same test there. The membership probe is the
// exception — WispPlaneIDs renders FROM wisps LEFT JOIN leases, so its
// tolerance is pinned to a missing `wisps` table and a broken database
// surfaces as an error instead of an empty wisp plane.
func (s *uowExportSource) LoadExportRelations(ctx context.Context, issues []*types.Issue) (exportRelations, error) {
	rel := exportRelations{
		labels:        map[string][]string{},
		deps:          map[string][]*types.Dependency{},
		comments:      map[string][]*types.Comment{},
		commentCounts: map[string]int{},
		depCounts:     map[string]*types.DependencyCounts{},
	}

	allIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		allIDs = append(allIDs, issue.ID)
	}
	if len(allIDs) == 0 {
		return rel, nil
	}

	// BATCH READS, and the batch is what keeps them here. Export hydrates every
	// relation of N issues at once — labels, comments, dependencies, counts,
	// both planes — and no role answers "the labels of these N ids": Reader.Get
	// is one detail view per call, so a role-routed export would be N round
	// trips over a whole workspace. A bulk relation reader is the follow-up
	// (ga-2ltro.12). Every LabelUseCase call in this file is one of these.
	labels, err := s.uw.LabelUseCase().GetLabelsForIssues(ctx, allIDs) //nolint:forbidigo // bulk relation load; no role answers labels-for-N-ids
	if err != nil {
		return exportRelations{}, fmt.Errorf("load labels: %w", err)
	}
	mergeExportMap(rel.labels, labels)
	comments, err := s.uw.CommentUseCase().GetCommentsForIssues(ctx, allIDs)
	if err != nil {
		return exportRelations{}, fmt.Errorf("load comments: %w", err)
	}
	mergeExportMap(rel.comments, comments)
	counts, err := s.uw.CommentUseCase().GetCommentCounts(ctx, allIDs)
	if err != nil {
		return exportRelations{}, fmt.Errorf("load comment counts: %w", err)
	}
	mergeExportMap(rel.commentCounts, counts)

	// The wisp_labels / wisp_comments rows of a wisp are keyed by an issue_id
	// that lives in the WISPS table (promotion moves them to the durable
	// tables, issueops.MoveIssuePersistenceInTx), so the three wisp readers
	// below only need the ids that are wisps right now. Handing them the
	// whole workspace was pure waste — on a 19k-issue rig with no wisps it
	// was three 19k-placeholder statements answering nothing (wy-237yfi).
	// CountsByWispIDs is NOT narrowed: its inbound leg counts wisp -> durable
	// edges under the durable target's id.
	// A nil set means membership could NOT be read (the wisps table is
	// absent, the tolerated case) — never "no wisps": fail open to the full
	// id list so the readers below keep their own table-not-exist tolerance.
	wispSet, err := s.WispPlaneIDs(ctx, allIDs)
	if err != nil {
		return exportRelations{}, err
	}
	wispIDs := wispReaderIDs(allIDs, wispSet)

	wispLabels, err := s.uw.LabelUseCase().GetLabelsForWisps(ctx, wispIDs) //nolint:forbidigo // bulk relation load; see GetLabelsForIssues above
	if err != nil {
		if !dberrors.IsTableNotExist(err) {
			return exportRelations{}, fmt.Errorf("load wisp labels: %w", err)
		}
	} else {
		mergeExportMap(rel.labels, wispLabels)
	}
	wispComments, err := s.uw.CommentUseCase().GetCommentsForWisps(ctx, wispIDs)
	if err != nil {
		if !dberrors.IsTableNotExist(err) {
			return exportRelations{}, fmt.Errorf("load wisp comments: %w", err)
		}
	} else {
		mergeExportMap(rel.comments, wispComments)
	}
	wispCounts, err := s.uw.CommentUseCase().GetWispCommentCounts(ctx, wispIDs)
	if err != nil {
		if !dberrors.IsTableNotExist(err) {
			return exportRelations{}, fmt.Errorf("load wisp comment counts: %w", err)
		}
	} else {
		mergeExportMap(rel.commentCounts, wispCounts)
	}

	deps, err := s.uw.DependencyUseCase().GetIssueDependencyRecords(ctx, allIDs)
	if err != nil {
		return exportRelations{}, fmt.Errorf("load dependency records: %w", err)
	}
	rel.deps = deps

	depCounts, err := s.uw.DependencyUseCase().CountsByIssueIDs(ctx, allIDs)
	if err != nil {
		return exportRelations{}, fmt.Errorf("load dependency counts: %w", err)
	}
	rel.depCounts = depCounts
	wispDepCounts, err := s.uw.DependencyUseCase().CountsByWispIDs(ctx, allIDs)
	if err != nil {
		// A rig without the wisp tables simply has no wisp-plane edges.
		if !dberrors.IsTableNotExist(err) {
			return exportRelations{}, fmt.Errorf("load wisp dependency counts: %w", err)
		}
	} else {
		for id, wc := range wispDepCounts {
			if wc == nil || (wc.DependencyCount == 0 && wc.DependentCount == 0) {
				continue
			}
			c := rel.depCounts[id]
			if c == nil {
				c = &types.DependencyCounts{}
				rel.depCounts[id] = c
			}
			c.DependencyCount += wc.DependencyCount
			c.DependentCount += wc.DependentCount
		}
	}

	return rel, nil
}

// wispPlaneSubset returns, in input order, the ids that wispSet marks as
// wisp-plane; none marked yields nil, and the bulk readers answer nil with no
// statement at all. The caller decides what a nil SET means (see
// LoadExportRelations: unreadable membership fails open, never empty).
func wispPlaneSubset(ids []string, wispSet map[string]bool) []string {
	var out []string
	for _, id := range ids {
		if wispSet[id] {
			out = append(out, id)
		}
	}
	return out
}

// wispReaderIDs is the caller-side policy wispPlaneSubset defers: a nil set
// means membership was UNREADABLE (WispPlaneIDs tolerated a missing wisps
// table), so every id goes to the wisp readers and they keep their own
// table-not-exist tolerance; a non-nil set — even an empty one — is the
// answer, and only the ids in it go.
func wispReaderIDs(ids []string, wispSet map[string]bool) []string {
	if wispSet == nil {
		return ids
	}
	return wispPlaneSubset(ids, wispSet)
}

// WispPlaneIDs classifies by wisps-table membership through the plane-pinned
// GetWispsByIDs read (row flags are NOT a substitute — see the exportSource
// interface comment). A rig without the wisps table has no wisp-plane rows
// (nil set), mirroring the tolerance of the wisp-plane legs in
// LoadExportRelations. Only the WISPS table is optional: the read joins
// leases, and a blanket table-not-exist check would report a broken
// database as an empty wisp plane (the issue_search.go missingOptionalWispTable
// lesson), so the tolerance is pinned to that one table name.
func (s *uowExportSource) WispPlaneIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	wisps, err := s.uw.IssueUseCase().GetWispsByIDs(ctx, ids)
	if err != nil {
		if dberrors.IsMissingTable(err, "wisps") {
			return nil, nil
		}
		return nil, fmt.Errorf("wisp plane membership: %w", err)
	}
	set := make(map[string]bool, len(wisps))
	for _, w := range wisps {
		set[w.ID] = true
	}
	return set, nil
}

// mergeExportMap copies src entries into dst. The label/comment planes are
// disjoint by ID (an issue lives in exactly one of issues/wisps), so a plain
// overwrite-merge reassembles the classic partitioned loaders' single map.
func mergeExportMap[V any](dst, src map[string]V) {
	for k, v := range src {
		dst[k] = v
	}
}

// roleExportSource is the export read set of a remote backend, which serves
// no raw search and no bulk relation read. The issues and every relation come
// from ONE library entry, issueops.ReadExport, composed over role reads every
// deployed server publishes (listIssues, listDependencies, getIssue). The
// JSONL is then built by the same runExportFromSource as on every other route.
type roleExportSource struct {
	rows map[string]issueops.ExportRow
}

// GetInfraTypes is unused on this route: only exportIssueFilter, the filter of
// the store-shaped sources, reads it. ReadExport's listing excludes the infra
// set the server resolves for its own workspace.
func (s *roleExportSource) GetInfraTypes(ctx context.Context) map[string]bool {
	return store.GetInfraTypes(ctx)
}

func (s *roleExportSource) ExportIssues(ctx context.Context, req issueops.ExportRequest) ([]*types.Issue, error) {
	rd, err := store.IssueReader()
	if err != nil {
		return nil, err
	}
	edges, err := store.EdgeReader()
	if err != nil {
		return nil, err
	}
	rows, err := issueops.ReadExport(ctx, rd, edges, req)
	if err != nil {
		return nil, err
	}
	s.rows = make(map[string]issueops.ExportRow, len(rows))
	issues := make([]*types.Issue, 0, len(rows))
	for _, row := range rows {
		s.rows[row.Issue.ID] = row
		issues = append(issues, row.Issue)
	}
	return issues, nil
}

func (s *roleExportSource) GetConfig(ctx context.Context, key string) (string, error) {
	return store.GetConfig(ctx, key)
}

// ExportMemories reads the served memory plane (listMemories). The config
// table cannot stand in for it on this route: the server's settings
// enumeration withholds every kv.* row, memories included, so reading it
// exported no memory at all.
func (s *roleExportSource) ExportMemories(ctx context.Context) (map[string]string, error) {
	memories, err := store.Memories()
	if err != nil {
		return nil, err
	}
	res, err := memories.List(ctx, memoryops.ListRequest{})
	if err != nil {
		return nil, err
	}
	return res.Memories, nil
}

// LoadExportRelations hands back what ReadExport already read; it issues no
// request of its own.
func (s *roleExportSource) LoadExportRelations(_ context.Context, issues []*types.Issue) (exportRelations, error) {
	rel := exportRelations{
		labels:        map[string][]string{},
		deps:          map[string][]*types.Dependency{},
		comments:      map[string][]*types.Comment{},
		commentCounts: map[string]int{},
		depCounts:     map[string]*types.DependencyCounts{},
	}
	for _, issue := range issues {
		row, ok := s.rows[issue.ID]
		if !ok {
			continue
		}
		if len(row.Issue.Labels) > 0 {
			rel.labels[issue.ID] = row.Issue.Labels
		}
		if len(row.Issue.Dependencies) > 0 {
			rel.deps[issue.ID] = row.Issue.Dependencies
		}
		if len(row.Issue.Comments) > 0 {
			rel.comments[issue.ID] = row.Issue.Comments
		}
		rel.commentCounts[issue.ID] = row.CommentCount
		rel.depCounts[issue.ID] = &types.DependencyCounts{DependencyCount: row.DependencyCount, DependentCount: row.DependentCount}
	}
	return rel, nil
}

// WispPlaneIDs answers nothing: no served read publishes plane membership
// (listIssues rows carry no plane field, however the listing is scoped), so a
// remote export stamps no row wisp_plane. A current import pins an
// unmarked no-history row to the durable plane (applyImportWispPlane). That
// is right for a durable row that still carries the flag (a legacy promotion,
// or an earlier unmarked import) and wrong for a genuine no-history wisp,
// which imports onto the Dolt-versioned issues plane. It is the same data-safe
// degradation as a store without storeWispPartitioner: a missing marker
// mis-planes a row, while a wrong one re-planes a durable row and drops its
// relations (bd-r9uce), so the marker is never inferred from flags. Serving
// the membership is bd-1lbvas.
func (s *roleExportSource) WispPlaneIDs(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}
