package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// importChunkSize bounds the number of issues written per transaction during
// import. One giant transaction holds the store's write lock for the whole
// batch (a 26k-issue import measured ~2 minutes of full-store outage on
// SQLite); bounded chunks release the lock between commits so concurrent
// readers and writers interleave.
//
// 250 was picked by measurement (8k-issue import, SQLite backend): 250- and
// 500-issue chunks both cost ~13% total time over one big transaction, but
// 250 holds the write lock ~1.4s per transaction versus ~2.8s at 500 and ~6s
// at 1000 — and the SQLite connection's busy_timeout is 5s, so per-chunk lock
// holds must stay comfortably below that for concurrent bd operations to wait
// out an import instead of failing. A var, not a const, so tests can shrink it.
var importChunkSize = 250

// importInterChunkPause is slept between import transactions. Bounding the
// transactions is not enough on its own: SQLite busy-polling has no fairness
// queue, so a loop that re-issues BEGIN IMMEDIATE microseconds after each
// COMMIT re-takes the lock before any waiter's poll fires and starves every
// concurrent bd operation for the whole import — measured as an unchanged
// 100% failure pattern with zero lock acquisitions across 20 chunk commits.
// The pause is the acquisition window that lets waiters in. 150ms costs
// ~15s on a 26k-issue import (104 chunks) against the ~2-minute write time.
var importInterChunkPause = 150 * time.Millisecond

// importPause is the sleep seam for the inter-chunk pause, swappable in tests.
var importPause = time.Sleep

// importProgress is where chunked imports report per-chunk progress.
// Swappable in tests.
var importProgress io.Writer = os.Stderr

// ImportOptions configures import behavior.
type ImportOptions struct {
	DryRun                     bool
	SkipUpdate                 bool
	Strict                     bool
	RenameOnImport             bool
	ClearDuplicateExternalRefs bool
	OrphanHandling             string
	DeletionIDs                []string
	SkipPrefixValidation       bool
	ProtectLocalExportIDs      map[string]time.Time
	// ConflictSkip makes the import insert-if-new instead of UPSERT: an
	// issue whose ID already exists is left untouched. Set only by the
	// auto-import upgrade-recovery fallback (GH#3955); explicit `bd import`
	// leaves this false and keeps UPSERT semantics.
	ConflictSkip bool
	// AllowStale imports rows even when their updated_at is older than the
	// local issue's, overwriting newer local state. Required for the
	// restore-an-older-snapshot recovery workflow, which the default stale
	// guard otherwise silently no-ops per row (bd-6dnrw.9). Only settable
	// via explicit `bd import --allow-stale`; auto-import paths never set it.
	AllowStale bool
}

// ImportResult describes what an import operation did.
type ImportResult struct {
	Created             int
	Updated             int
	Unchanged           int
	Skipped             int
	Deleted             int
	Collisions          int
	IDMapping           map[string]string
	CollisionIDs        []string
	PrefixMismatch      bool
	ExpectedPrefix      string
	MismatchPrefixes    map[string]int
	ImportedIDs         []string
	StaleSkippedIDs     []string
	SkippedDependencies []string
	// UpdatedIssues lists existing local issues whose row the import
	// rewrote (incoming strictly newer, content differs), with a
	// field-level summary, so reverts of local state are visible instead
	// of silent (bd-hj85c).
	UpdatedIssues []ImportChange
	// TieKeptLocalIDs lists incoming rows whose updated_at equals the
	// local issue's but whose content differs. The upsert keeps the local
	// row for these (second-granularity timestamp ties, bd-hj85c); their
	// aux data still merges.
	TieKeptLocalIDs []string
}

// ImportChange describes how an import row modified an existing local issue.
type ImportChange struct {
	ID      string `json:"id"`
	Changes string `json:"changes,omitempty"`
}

// importIssuesCore imports issues into the Dolt store.
// This is a bridge function that delegates to the Dolt store's batch creation.
func importIssuesCore(ctx context.Context, _ string, store storage.DoltStorage, issues []*types.Issue, opts ImportOptions) (*ImportResult, error) {
	if opts.DryRun || len(issues) == 0 {
		return &ImportResult{Skipped: len(issues)}, nil
	}

	// The stale guard has two halves (bd-pkim8). This pre-filter reports the
	// rows that are already known stale (StaleSkippedIDs) and keeps their
	// labels/comments/dependencies out of the batch entirely. It is a separate
	// read, though, so a local update that commits between it and the batch
	// write would slip through — RejectStaleUpserts below closes that race by
	// re-checking updated_at inside the upsert itself.
	var staleSkippedIDs []string
	var changePlan importChangePlan
	if !opts.AllowStale {
		filtered, skipped, plan, err := filterStaleImportIssues(ctx, store, issues)
		if err != nil {
			return nil, err
		}
		issues = filtered
		staleSkippedIDs = skipped
		changePlan = plan
		if len(issues) == 0 {
			return &ImportResult{Skipped: len(staleSkippedIDs), StaleSkippedIDs: staleSkippedIDs}, nil
		}
	}

	var skippedDependencies []string
	skippedDependencySet := make(map[string]struct{})
	// In-txn half of the stale guard: rows the conditional upsert rejected
	// (local update committed between the pre-filter read and the batch
	// write). The transaction may retry, so dedup by ID.
	staleRejectedSet := make(map[string]struct{})
	actor := getActorWithGit()
	batchOpts := storage.BatchCreateOptions{
		OrphanHandling:                 storage.OrphanAllow,
		SkipPrefixValidation:           opts.SkipPrefixValidation,
		ConflictSkip:                   opts.ConflictSkip,
		RejectStaleUpserts:             !opts.AllowStale,
		SkipDependencyValidationErrors: true,
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			skipped := fmt.Sprintf("%s -> %s: %s", issueID, dependsOnID, reason)
			if _, ok := skippedDependencySet[skipped]; ok {
				return
			}
			skippedDependencySet[skipped] = struct{}{}
			skippedDependencies = append(skippedDependencies, skipped)
		},
		OnStaleRejected: func(issueID string) {
			staleRejectedSet[issueID] = struct{}{}
		},
	}
	var err error
	if len(issues) <= importChunkSize {
		// Small import: one transaction, dependencies inline — exactly the
		// pre-chunking behavior.
		err = store.CreateIssuesWithFullOptions(ctx, issues, actor, batchOpts)
	} else {
		err = importIssuesChunked(ctx, store, issues, actor, batchOpts)
	}
	if err != nil {
		return nil, err
	}

	importedIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if _, rejected := staleRejectedSet[issue.ID]; rejected {
			staleSkippedIDs = append(staleSkippedIDs, issue.ID)
			continue
		}
		importedIDs = append(importedIDs, issue.ID)
	}
	// Drop planned updates the in-txn guard rejected (a local update raced
	// in between the pre-filter read and the batch write).
	updatedIssues := make([]ImportChange, 0, len(changePlan.Updates))
	updatedCount := 0
	for _, change := range changePlan.Updates {
		if _, rejected := staleRejectedSet[change.ID]; rejected {
			continue
		}
		updatedIssues = append(updatedIssues, change)
		updatedCount++
	}
	return &ImportResult{
		Created:             len(importedIDs),
		Updated:             updatedCount,
		Skipped:             len(staleSkippedIDs),
		ImportedIDs:         importedIDs,
		StaleSkippedIDs:     staleSkippedIDs,
		SkippedDependencies: skippedDependencies,
		UpdatedIssues:       updatedIssues,
		TieKeptLocalIDs:     changePlan.TieKeptLocal,
	}, nil
}

// importIssuesChunked writes a large import in bounded transactions of
// importChunkSize rows instead of one batch-wide transaction, sleeping
// importInterChunkPause between commits. One giant transaction holds the
// store's write lock for the whole import, taking every concurrent bd
// operation down with it; bounded chunks cap the per-transaction lock hold,
// and the pause between commits is the fairness window that actually lets
// waiters acquire (see importInterChunkPause).
//
// Rows are written in dependency order (orderImportIssuesForChunking): every
// readiness-affecting edge owned by a non-cycle row points at a row in the same
// or an earlier chunk — Kahn's order for the acyclic majority, plus a
// cycle-breaking fallback that still emits each cycle member before the rows it
// blocks — so that edge rides inline with its row and both commit in one
// transaction. Only a force-emitted cycle member can carry a readiness edge into
// the deferred pass, and that is the already-tolerated corrupt/legacy-cycle
// window. A concurrent reader therefore never observes a non-cycle imported bead
// without the blocking edges its import file declares —
// `bd ready` mid-import cannot offer blocked work for dispatch, and a crash
// mid-import cannot freeze a bead in a spuriously-ready state.
//
// Only edges that cannot be satisfied when their row commits are deferred to
// a final dependency pass: edges into an intra-batch dependency cycle
// (invalid for blocking types; still broken and skip-reported at wire time,
// though for a cycle of length >=3 the specific edge dropped — and thus which
// member is left spuriously ready — can differ from the single-transaction
// import, which checks the whole cycle in file order) and non-readiness edges
// (related, discovered-from) that point at a later chunk. The dependency pass submits
// row copies stripped to those deferred edges with ConflictSkip set, so an
// existing row is never rewritten: a concurrent update landing between a
// row's chunk and the dependency pass cannot stale-reject the pass and drop
// the edges (the resubmit-the-full-row alternative did exactly that,
// silently and unrecoverably). Rows whose phase-1 write was itself
// stale-rejected are excluded from the pass: a stale snapshot keeps its
// labels, comments, AND dependencies out (bd-578h9.8).
//
// Every per-row application is an idempotent upsert (conditional-update row
// write, INSERT IGNORE labels, existence-checked comments, deterministic-id
// dependency edges, created-events only for genuinely new rows), so a failure
// mid-import leaves a committed, durable prefix and re-running the same import
// converges on the full set — subject to the import's standing stale policy:
// a row a rival updated since its chunk committed is locally newer on the
// re-run, so the pre-filter stale-skips that snapshot wholesale (row and any
// still-unwired deferred edges), reported in StaleSkippedIDs. That is the
// same local-wins outcome the single-transaction import gave a rival update
// racing a crashed import, and it can only affect deferred edges (non-readiness
// edges, plus the readiness edges of force-emitted cycle members); every
// non-cycle row's readiness edges commit with their row.
func importIssuesChunked(ctx context.Context, store storage.DoltStorage, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	// Apply the cross-bucket dependency policy over the full set up front:
	// the engine's per-batch filter only sees one chunk, so it could no
	// longer detect an edge whose endpoints land in different chunks.
	issues, err := issueops.FilterCreateIssuesMixedBucketDependencies(issues, opts)
	if err != nil {
		return err
	}
	ordered := orderImportIssuesForChunking(issues)

	// Partitioning narrows each issue's dependency slice to its inline subset in
	// place; always restore the full slices so the caller's issues stay intact
	// for retries and reporting.
	fullDeps := make([][]*types.Dependency, len(ordered))
	for i, issue := range ordered {
		fullDeps[i] = issue.Dependencies
	}
	defer func() {
		for i, issue := range ordered {
			issue.Dependencies = fullDeps[i]
		}
	}()
	deferred := partitionChunkedImportDeps(ordered)

	// Record phase-1 stale rejections locally (as well as forwarding them to
	// the caller): the dependency pass must skip those rows.
	phase1Stale := make(map[string]struct{})
	rowOpts := opts
	rowOpts.OnStaleRejected = func(issueID string) {
		phase1Stale[issueID] = struct{}{}
		if opts.OnStaleRejected != nil {
			opts.OnStaleRejected(issueID)
		}
	}

	pacer := &importChunkPacer{}
	if err := writeImportRowChunks(ctx, store, ordered, actor, rowOpts, pacer); err != nil {
		return err
	}
	return wireDeferredImportDeps(ctx, store, deferred, phase1Stale, len(ordered), actor, opts, pacer)
}

// importChunkPacer sleeps importInterChunkPause between import transactions. One
// shared pacer spans phase 1 and the dependency pass, so the fairness pause is
// issued before every transaction except the very first: SQLite busy-polling has
// no fairness queue, and a back-to-back BEGIN IMMEDIATE re-takes the lock before
// a waiter's poll fires (see importInterChunkPause).
type importChunkPacer struct{ transactions int }

func (p *importChunkPacer) beforeTx() {
	if p.transactions > 0 {
		importPause(importInterChunkPause)
	}
	p.transactions++
}

// deferredImportEdges carries the dependencies of one row that could not be
// wired inline with its phase-1 write and must be applied in the dependency
// pass.
type deferredImportEdges struct {
	issue *types.Issue
	deps  []*types.Dependency
}

// partitionChunkedImportDeps splits each ordered row's dependencies into edges
// wired inline with the row (target outside the batch, or first written in the
// same or an earlier chunk) and edges deferred to the dependency pass. It
// narrows each issue's dependency slice to the inline subset in place and
// returns the deferred edges. orderImportIssuesForChunking guarantees every
// readiness edge owned by a non-cycle row points at the same or an earlier
// chunk, so only non-readiness edges (related, discovered-from) into a later
// chunk and the readiness edges of force-emitted cycle members (their own cyclic
// edges, plus any acyclic blocker they were emitted ahead of) are ever deferred
// here.
func partitionChunkedImportDeps(ordered []*types.Issue) []deferredImportEdges {
	firstChunkOf := make(map[string]int, len(ordered))
	for pos, issue := range ordered {
		if issue.ID == "" {
			continue // ID assigned by the engine at insert; nothing can reference it yet
		}
		if _, ok := firstChunkOf[issue.ID]; !ok {
			firstChunkOf[issue.ID] = pos / importChunkSize
		}
	}
	var deferred []deferredImportEdges
	for pos, issue := range ordered {
		if len(issue.Dependencies) == 0 {
			continue
		}
		inline, later := splitDepsByChunk(issue.Dependencies, pos/importChunkSize, firstChunkOf)
		if len(later) == 0 {
			continue
		}
		issue.Dependencies = inline
		deferred = append(deferred, deferredImportEdges{issue: issue, deps: later})
	}
	return deferred
}

// splitDepsByChunk partitions one row's dependencies (the row lands in rowChunk)
// into edges that can be wired inline and edges whose target is first written in
// a later chunk and so must be deferred.
func splitDepsByChunk(deps []*types.Dependency, rowChunk int, firstChunkOf map[string]int) (inline, later []*types.Dependency) {
	for _, dep := range deps {
		if targetChunk, inBatch := firstChunkOf[dep.DependsOnID]; inBatch && targetChunk > rowChunk {
			later = append(later, dep)
			continue
		}
		inline = append(inline, dep)
	}
	return inline, later
}

// writeImportRowChunks writes the ordered rows (dependencies already narrowed to
// their inline subset) in bounded transactions, pausing between commits.
func writeImportRowChunks(ctx context.Context, store storage.DoltStorage, ordered []*types.Issue, actor string, rowOpts storage.BatchCreateOptions, pacer *importChunkPacer) error {
	total := len(ordered)
	chunks := (total + importChunkSize - 1) / importChunkSize
	for start, chunk := 0, 1; start < total; start, chunk = start+importChunkSize, chunk+1 {
		end := min(start+importChunkSize, total)
		pacer.beforeTx()
		if err := store.CreateIssuesWithFullOptions(ctx, ordered[start:end], actor, rowOpts); err != nil {
			return fmt.Errorf("import chunk %d/%d failed, %d issues already committed (committed rows are durable; re-run the import to resume — it converges): %w", chunk, chunks, start, err)
		}
		fmt.Fprintf(importProgress, "bd import: %d/%d issues committed\n", end, total)
	}
	return nil
}

// wireDeferredImportDeps applies the deferred edges once every target row exists,
// without rewriting the rows themselves. rowTotal is the count of phase-1 rows
// already committed, used only for the resume message.
func wireDeferredImportDeps(ctx context.Context, store storage.DoltStorage, deferred []deferredImportEdges, phase1Stale map[string]struct{}, rowTotal int, actor string, opts storage.BatchCreateOptions, pacer *importChunkPacer) error {
	depRows := make([]*types.Issue, 0, len(deferred))
	for _, d := range deferred {
		if _, stale := phase1Stale[d.issue.ID]; stale {
			continue // stale snapshot: its deps stay out too (bd-578h9.8)
		}
		cp := *d.issue
		cp.Dependencies = d.deps
		// The row landed in phase 1 and its labels/comments merged there;
		// this pass carries edges only.
		cp.Labels = nil
		cp.Comments = nil
		depRows = append(depRows, &cp)
	}
	if len(depRows) == 0 {
		return nil
	}
	depOpts := opts
	// Never rewrite an existing row here: the import's row write already
	// happened in phase 1, and a concurrent update since then must win. With
	// ConflictSkip the engine leaves the stored row untouched and still wires
	// the batch's dependencies.
	depOpts.ConflictSkip = true
	// No row write can be stale-rejected under ConflictSkip; leaving the
	// callback unset keeps a phase-2 signal from ever misreporting a row
	// whose phase-1 write committed.
	depOpts.OnStaleRejected = nil
	depTotal := len(depRows)
	depChunks := (depTotal + importChunkSize - 1) / importChunkSize
	for start, chunk := 0, 1; start < depTotal; start, chunk = start+importChunkSize, chunk+1 {
		end := min(start+importChunkSize, depTotal)
		pacer.beforeTx()
		if err := store.CreateIssuesWithFullOptions(ctx, depRows[start:end], actor, depOpts); err != nil {
			return fmt.Errorf("import dependency pass chunk %d/%d failed (all %d issue rows are committed; re-run the import to resume — it converges): %w", chunk, depChunks, rowTotal, err)
		}
		fmt.Fprintf(importProgress, "bd import: deferred dependencies wired for %d/%d issues\n", end, depTotal)
	}
	return nil
}

// orderImportIssuesForChunking returns the issues reordered so that every valid
// readiness-affecting edge (blocks, parent-child, conditional-blocks, waits-for;
// the types GetReadyWork consults) points at a row in the same or an earlier
// chunk, which lets the import wire that edge in the same transaction as the
// row. It runs Kahn's algorithm over the intra-batch readiness edges, seeded in
// file order so unconstrained rows keep their relative order; duplicate IDs are
// chained in file order to preserve last-row-wins upsert semantics.
//
// A readiness cycle (invalid for blocking types; only ever present in the
// corrupted or legacy JSONL the import tolerates) cannot be fully ordered.
// Appending the stalled rows in plain file order — as an earlier version did —
// can place a valid dependent of a cycle before the cycle member it blocks on,
// deferring that live readiness edge and briefly exposing the dependent as ready
// without its blocker. Instead each stall is broken by emitting a row that
// genuinely lies on a cycle, so every readiness edge owned by a non-cycle row
// still rides inline; only a force-emitted cycle member may defer a readiness
// edge — its own unsatisfiable cycle edge, or a valid acyclic edge whose blocker
// it was emitted ahead of — and the engine skip-reports the unsatisfiable ones,
// still breaking the cycle. For a cycle of length
// >=3 the edge left to defer (and hence which member is left spuriously ready)
// can differ from the one the single-transaction import drops, which checks all
// cycle edges in file order against the already-persisted set; both break it.
func orderImportIssuesForChunking(issues []*types.Issue) []*types.Issue {
	if len(issues) < 2 {
		return issues
	}
	return buildImportOrderGraph(issues).topologicalFileOrder(issues)
}

// importOrderGraph is the intra-batch readiness-edge graph over import rows,
// indexed by position in the input slice. An edge target->dependent means the
// target (a readiness blocker, or an earlier duplicate of the same ID) must be
// emitted no later than the dependent.
type importOrderGraph struct {
	dependents [][]int // dependents[t]: rows released when row t is emitted
	blockers   [][]int // blockers[u]: rows that must be emitted before row u
	indegree   []int
}

func buildImportOrderGraph(issues []*types.Issue) importOrderGraph {
	n := len(issues)
	indicesByID := indexImportIssuesByID(issues)
	// A waits-for waiter's is_blocked state is gated on whether its spawner has
	// an active parent-child child, not on the spawner row itself, so those
	// child rows are readiness inputs to the waiter (see addRowReadinessEdges).
	// Index them by spawner up front so the edge pass can order them.
	childrenBySpawnerID := parentChildChildrenBySpawner(issues)
	g := importOrderGraph{
		dependents: make([][]int, n),
		blockers:   make([][]int, n),
		indegree:   make([]int, n),
	}
	for i, issue := range issues {
		g.addRowReadinessEdges(i, issue, indicesByID, childrenBySpawnerID)
	}
	// Chain duplicate IDs in file order so the last row wins the upsert.
	for _, indices := range indicesByID {
		for k := 1; k < len(indices); k++ {
			g.addEdge(indices[k-1], indices[k])
		}
	}
	return g
}

// indexImportIssuesByID maps each import row's ID to its positions in the input
// slice. ID-less rows are skipped: their ID is assigned by the engine at insert,
// so nothing in the batch can reference them yet.
func indexImportIssuesByID(issues []*types.Issue) map[string][]int {
	indicesByID := make(map[string][]int, len(issues))
	for i, issue := range issues {
		if issue.ID == "" {
			continue
		}
		indicesByID[issue.ID] = append(indicesByID[issue.ID], i)
	}
	return indicesByID
}

// parentChildChildrenBySpawner maps each spawner ID to the positions of the
// in-batch rows that declare it as their parent-child parent. A parent-child
// dependency row is stored on the child (issue_id = child, depends_on = parent),
// so the child is the row carrying the edge.
func parentChildChildrenBySpawner(issues []*types.Issue) map[string][]int {
	children := make(map[string][]int)
	for i, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type == types.DepParentChild && dep.DependsOnID != "" {
				children[dep.DependsOnID] = append(children[dep.DependsOnID], i)
			}
		}
	}
	return children
}

// addEdge records that row target must be emitted no later than row dependent.
func (g importOrderGraph) addEdge(target, dependent int) {
	g.dependents[target] = append(g.dependents[target], dependent)
	g.blockers[dependent] = append(g.blockers[dependent], target)
	g.indegree[dependent]++
}

// addEdgesBefore orders every target row no later than dependent, skipping any
// self-reference.
func (g importOrderGraph) addEdgesBefore(dependent int, targets []int) {
	for _, target := range targets {
		if target != dependent {
			g.addEdge(target, dependent)
		}
	}
}

// addRowReadinessEdges records the intra-batch ordering edges that row i imposes:
// every readiness blocker it names must be emitted no later than i, and for a
// waits-for edge the spawner's in-batch parent-child children must be too.
//
// The waiter's is_blocked state keys on whether its spawner has an active child,
// not on the spawner row itself, and the per-chunk is_blocked recompute only
// re-evaluates the rows in its own transaction. So a waiter whose spawner's child
// lands in a later chunk would compute is_blocked=0 against a still-childless
// spawner and never be re-blocked — spuriously ready for the rest of the import
// and after it. Ordering the spawner inline is not enough; the child must be
// committed by the time the waiter's chunk recomputes.
func (g importOrderGraph) addRowReadinessEdges(i int, issue *types.Issue, indicesByID, childrenBySpawnerID map[string][]int) {
	for _, dep := range issue.Dependencies {
		if dep == nil || dep.DependsOnID == "" || !dep.Type.AffectsReadyWork() {
			continue
		}
		g.addEdgesBefore(i, indicesByID[dep.DependsOnID])
		if dep.Type == types.DepWaitsFor {
			g.addEdgesBefore(i, childrenBySpawnerID[dep.DependsOnID])
		}
	}
}

// topologicalFileOrder emits rows in dependency order (Kahn's, file-order
// seeded), breaking each stall by force-emitting a row that lies on a readiness
// cycle, so every non-cycle row's readiness edges ride inline; only a
// force-emitted cycle member can leave a readiness edge (its unsatisfiable cycle
// edge, or a valid acyclic edge it was emitted ahead of) for the deferred
// dependency pass.
func (g importOrderGraph) topologicalFileOrder(issues []*types.Issue) []*types.Issue {
	n := len(issues)
	indegree := append([]int(nil), g.indegree...)
	emitted := make([]bool, n)
	queue := make([]int, 0, n)
	for i := range n {
		if indegree[i] == 0 {
			queue = append(queue, i)
		}
	}
	ordered := make([]*types.Issue, 0, n)
	for len(ordered) < n {
		if len(queue) == 0 {
			queue = append(queue, g.nextCycleParticipant(emitted))
		}
		i := queue[0]
		queue = queue[1:]
		if emitted[i] {
			continue
		}
		emitted[i] = true
		ordered = append(ordered, issues[i])
		queue = g.release(i, indegree, emitted, queue)
	}
	return ordered
}

// release decrements the indegree of every row that row i blocks and enqueues
// any that becomes free and has not already been emitted.
func (g importOrderGraph) release(i int, indegree []int, emitted []bool, queue []int) []int {
	for _, j := range g.dependents[i] {
		indegree[j]--
		if indegree[j] == 0 && !emitted[j] {
			queue = append(queue, j)
		}
	}
	return queue
}

// nextCycleParticipant returns an un-emitted row that lies on a readiness cycle,
// used to break a Kahn stall.
func (g importOrderGraph) nextCycleParticipant(emitted []bool) int {
	start := 0
	for start < len(emitted) && emitted[start] {
		start++
	}
	return g.cycleParticipant(start, emitted)
}

// cycleParticipant walks blocker edges from an un-emitted row until it revisits
// a row, which therefore lies on a cycle. At a stall every un-emitted row still
// has an un-emitted blocker, so the walk always closes a cycle.
func (g importOrderGraph) cycleParticipant(start int, emitted []bool) int {
	seen := make(map[int]bool)
	v := start
	for !seen[v] {
		seen[v] = true
		next := -1
		for _, b := range g.blockers[v] {
			if !emitted[b] {
				next = b
				break
			}
		}
		if next == -1 {
			return v // defensive: no un-emitted blocker (unexpected at a stall)
		}
		v = next
	}
	return v
}

// importChangePlan reports how the import batch relates to existing local
// issues, so the import can surface what it changed instead of doing it
// silently (bd-hj85c).
type importChangePlan struct {
	// Updates lists existing issues the batch will rewrite: incoming row
	// strictly newer and row content differs.
	Updates []ImportChange
	// TieKeptLocal lists incoming rows with the same updated_at as the
	// local issue but different row content. The stale-guarded upsert keeps
	// every stored column for these (second-granularity timestamp tie),
	// while their aux data still merges.
	TieKeptLocal []string
}

func filterStaleImportIssues(ctx context.Context, store storage.DoltStorage, issues []*types.Issue) ([]*types.Issue, []string, importChangePlan, error) {
	var plan importChangePlan
	ids := make([]string, 0, len(issues))
	seen := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if issue == nil || issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		ids = append(ids, issue.ID)
	}
	if len(ids) == 0 {
		return issues, nil, plan, nil
	}

	localIssues, err := store.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, nil, plan, fmt.Errorf("check existing issues before import: %w", err)
	}
	localByID := make(map[string]*types.Issue, len(localIssues))
	for _, issue := range localIssues {
		if issue != nil && issue.ID != "" && !issue.UpdatedAt.IsZero() {
			localByID[issue.ID] = issue
		}
	}
	if len(localByID) == 0 {
		return issues, nil, plan, nil
	}

	filtered := make([]*types.Issue, 0, len(issues))
	skippedIDs := make([]string, 0)
	for _, issue := range issues {
		if issue == nil || issue.ID == "" || issue.UpdatedAt.IsZero() {
			filtered = append(filtered, issue)
			continue
		}
		local, ok := localByID[issue.ID]
		if !ok {
			filtered = append(filtered, issue)
			continue
		}
		// Compare at second granularity: updated_at is DATETIME(0) in the
		// store, so a sub-second component on the JSONL side must not turn
		// a tie into a spurious "newer" classification.
		incomingAt := issue.UpdatedAt.UTC().Truncate(time.Second)
		localAt := local.UpdatedAt.UTC().Truncate(time.Second)
		if incomingAt.Before(localAt) {
			skippedIDs = append(skippedIDs, issue.ID)
			continue
		}
		if summary := importRowChangeSummary(local, issue); summary != "" {
			if incomingAt.Equal(localAt) {
				plan.TieKeptLocal = append(plan.TieKeptLocal, issue.ID)
			} else {
				plan.Updates = append(plan.Updates, ImportChange{ID: issue.ID, Changes: summary})
			}
		}
		filtered = append(filtered, issue)
	}
	return filtered, skippedIDs, plan, nil
}

// importRowChangeSummary summarizes the differences between the local issue
// row and the incoming import row, restricted to the columns the import
// upsert rewrites. Returns "" when none of those fields differ. Status,
// priority, and type transitions show old → new; long-form fields are listed
// by name only.
func importRowChangeSummary(local, incoming *types.Issue) string {
	var parts []string
	if local.Status != incoming.Status {
		parts = append(parts, fmt.Sprintf("status %s → %s", local.Status, incoming.Status))
	}
	if local.Priority != incoming.Priority {
		parts = append(parts, fmt.Sprintf("priority %d → %d", local.Priority, incoming.Priority))
	}
	if local.IssueType != incoming.IssueType {
		parts = append(parts, fmt.Sprintf("type %s → %s", local.IssueType, incoming.IssueType))
	}
	if local.Assignee != incoming.Assignee {
		parts = append(parts, "assignee")
	}
	if local.Title != incoming.Title {
		parts = append(parts, "title")
	}
	if local.Description != incoming.Description {
		parts = append(parts, "description")
	}
	if local.Design != incoming.Design {
		parts = append(parts, "design")
	}
	if local.AcceptanceCriteria != incoming.AcceptanceCriteria {
		parts = append(parts, "acceptance_criteria")
	}
	if local.Notes != incoming.Notes {
		if incoming.Notes == "" {
			parts = append(parts, "notes cleared")
		} else {
			parts = append(parts, "notes")
		}
	}
	if local.CloseReason != incoming.CloseReason {
		parts = append(parts, "close_reason")
	}
	if !stringPtrEqual(local.ExternalRef, incoming.ExternalRef) {
		parts = append(parts, "external_ref")
	}
	if !intPtrEqual(local.EstimatedMinutes, incoming.EstimatedMinutes) {
		parts = append(parts, "estimate")
	}
	if string(local.Metadata) != string(incoming.Metadata) {
		parts = append(parts, "metadata")
	}
	return strings.Join(parts, ", ")
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// importLocalResult holds counts from a local JSONL import.
type importLocalResult struct {
	Issues   int
	Memories int
}

// memoryRecord represents a memory entry in the JSONL export.
type memoryRecord struct {
	Type  string `json:"_type"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// importFromLocalJSONL imports issues (and memories) from a local JSONL file on disk
// into the Dolt store. Returns the number of issues imported and any error.
// This is a convenience wrapper around importFromLocalJSONLFull.
func importFromLocalJSONL(ctx context.Context, store storage.DoltStorage, localPath string) (int, error) {
	result, err := importFromLocalJSONLFull(ctx, store, localPath)
	if err != nil {
		return 0, err
	}
	return result.Issues, nil
}

// parseJSONLFile reads a JSONL file and returns parsed issues and config
// entries (memories). Pure function — no store I/O.
func parseJSONLFile(path string) ([]*types.Issue, map[string]string, error) {
	//nolint:gosec // G304: path from user-provided CLI argument
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read JSONL file %s: %w", path, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	// Allow up to 64MB per line for large descriptions
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var issues []*types.Issue
	configEntries := make(map[string]string)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Peek at the record to check for _type field
		var peek map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			return nil, nil, fmt.Errorf("failed to parse JSONL line: %w", err)
		}

		// Skip the optional beads-jsonl metadata/header record.
		// Canonical exports produced by the stable-ordering /
		// git-merge convention prepend a schema+provenance line, e.g.
		// {"_schema":"beads-jsonl/1","_dolt_branch":"main",
		// "_dolt_commit":"...","_sort":"stable-v1"}. It carries no
		// _type and no issue fields; without this guard it falls
		// through to the issue path, unmarshals into an empty Issue,
		// and aborts the whole import with "validation failed for
		// issue : title is required". Identified by the _schema
		// sentinel, which real issue/memory records never carry.
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		// Check if this is a memory record
		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal([]byte(line), &mem); err != nil {
					return nil, nil, fmt.Errorf("failed to parse memory record: %w", err)
				}
				if mem.Key != "" && mem.Value != "" {
					configEntries[kvPrefix+memoryPrefix+mem.Key] = mem.Value
				}
				continue
			}
		}

		// Regular issue record
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, nil, fmt.Errorf("failed to parse issue from JSONL: %w", err)
		}
		// Skip tombstone entries: these are deleted issues exported by older
		// versions (pre-v0.50) with status "tombstone" and deleted_at set.
		// They are not valid for re-import since "tombstone" is not a real status.
		if issue.Status == "tombstone" {
			continue
		}

		// v0.35–v0.37 exported "wisp" (bool), renamed to "ephemeral" in v0.38+.
		// map old field name so the flag is preserved on import.
		if _, hasWisp := peek["wisp"]; hasWisp && !issue.Ephemeral {
			var wisp bool
			if err := json.Unmarshal(peek["wisp"], &wisp); err == nil && wisp {
				issue.Ephemeral = true
			}
		}

		issue.SetDefaults()
		issues = append(issues, &issue)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to scan JSONL: %w", err)
	}

	return issues, configEntries, nil
}

// importFromLocalJSONLFull imports issues and memories from a local JSONL file
// using UPSERT semantics (an existing issue row is overwritten). Used by the
// explicit recovery paths: `bd bootstrap` and `bd init --from-jsonl`.
func importFromLocalJSONLFull(ctx context.Context, store storage.DoltStorage, localPath string) (*importLocalResult, error) {
	return importFromLocalJSONLWithOpts(ctx, store, localPath, false)
}

// importFromLocalJSONLConflictSkip is the auto-import upgrade-recovery
// fallback (GH#3955; the fallbackImporter seam in auto_import_upgrade.go).
// It is identical to importFromLocalJSONLFull except that an issue whose ID
// already exists is left untouched instead of being overwritten, so a
// regressed emptiness guard can never clobber live rows — worst case is a
// no-op.
func importFromLocalJSONLConflictSkip(ctx context.Context, store storage.DoltStorage, localPath string) (*importLocalResult, error) {
	return importFromLocalJSONLWithOpts(ctx, store, localPath, true)
}

// importFromLocalJSONLWithOpts is the shared implementation. It detects
// memory records (lines with "_type":"memory") and imports them via
// SetConfig, while routing regular issue records through the normal path.
// conflictSkip selects insert-if-new (true) vs UPSERT (false) for issue rows.
func importFromLocalJSONLWithOpts(ctx context.Context, store storage.DoltStorage, localPath string, conflictSkip bool) (*importLocalResult, error) {
	issues, configEntries, err := parseJSONLFile(localPath)
	if err != nil {
		return nil, err
	}

	result := &importLocalResult{}

	// Import memories
	for key, value := range configEntries {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return nil, fmt.Errorf("failed to import config %q: %w", key, err)
		}
		result.Memories++
	}

	// Import issues
	if len(issues) > 0 {
		// Auto-detect prefix from first issue if not already configured
		configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err == nil && strings.TrimSpace(configuredPrefix) == "" {
			firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
			if firstPrefix != "" {
				if err := store.SetConfig(ctx, "issue_prefix", firstPrefix); err != nil {
					return nil, fmt.Errorf("failed to set issue_prefix from imported issues: %w", err)
				}
			}
		}

		opts := ImportOptions{
			SkipPrefixValidation: true,
			ConflictSkip:         conflictSkip,
		}
		importResult, err := importIssuesCore(ctx, "", store, issues, opts)
		if err != nil {
			return nil, err
		}
		result.Issues = importResult.Created
	}

	return result, nil
}
