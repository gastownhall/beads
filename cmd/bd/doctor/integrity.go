package doctor

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// CheckIDFormat checks whether issues use hash-based or sequential IDs.
// Opens its own store; prefer CheckIDFormatWithStore when a shared store is available.
func CheckIDFormat(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusError,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer func() { _ = store.Close() }()

	return checkIDFormatWithStore(store)
}

// CheckIDFormatWithStore checks ID format using a shared store (GH#2636).
func CheckIDFormatWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}
	return checkIDFormatWithStore(store)
}

func checkIDFormatWithStore(store *dolt.DoltStore) DoctorCheck {
	ctx := context.Background()
	db := store.UnderlyingDB()

	// Get sample of issues to check ID format (up to 10 for pattern analysis)
	rows, err := db.QueryContext(ctx, "SELECT id FROM issues ORDER BY created_at LIMIT 10")
	if err != nil {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusError,
			Message: "Unable to query issues",
			Detail:  err.Error(),
		}
	}
	defer rows.Close()

	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			issueIDs = append(issueIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusWarning,
			Message: "Row iteration error",
			Detail:  err.Error(),
		}
	}

	if len(issueIDs) == 0 {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}

	// Detect ID format using robust heuristic
	if DetectHashBasedIDs(db, issueIDs) {
		return DoctorCheck{
			Name:    "Issue IDs",
			Status:  StatusOK,
			Message: "hash-based ✓",
		}
	}

	return DoctorCheck{
		Name:    "Issue IDs",
		Status:  StatusWarning,
		Message: "sequential IDs detected — consider migrating to hash-based IDs",
	}
}

// CheckDependencyCycles checks for circular dependencies in the issue graph.
// Opens its own store; prefer CheckDependencyCyclesWithStore when a shared store is available.
func CheckDependencyCycles(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer func() { _ = store.Close() }()

	return checkDependencyCyclesWithStore(store)
}

// CheckDependencyCyclesWithStore checks for cycles using a shared store (GH#2636).
func CheckDependencyCyclesWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}
	return checkDependencyCyclesWithStore(store)
}

// dependencyCyclePageSize bounds the rows fetched per query while loading the
// dependency graph. Shared dolt sql-servers enforce per-query read timeouts
// (read_timeout_millis) that kill long-running streaming queries mid-stream
// ("invalid connection"); keyset-paginated pages keep every query small.
const dependencyCyclePageSize = 1000

// dependencyCycleMaxEdges bounds the in-memory graph. Beyond this the check
// degrades to a warning instead of risking excessive memory use.
const dependencyCycleMaxEdges = 1_000_000

// dependencyCycleTables are the tables cycle detection traverses, matching
// issueops.DetectCyclesInTx and doctorDependencyUnionSQL: wisp edges
// participate in the same blocking graph as durable ones.
var dependencyCycleTables = []string{"dependencies", "wisp_dependencies"}

func checkDependencyCyclesWithStore(store *dolt.DoltStore) DoctorCheck {
	db := store.UnderlyingDB()

	edges, check := loadDependencyEdges(db, dependencyCyclePageSize, dependencyCycleMaxEdges)
	if check != nil {
		return *check
	}

	cycleNodes := dependencyCycleNodes(edges)

	if len(cycleNodes) == 0 {
		return DoctorCheck{
			Name:    "Dependency Cycles",
			Status:  StatusOK,
			Message: "No circular dependencies detected",
		}
	}

	return DoctorCheck{
		Name:    "Dependency Cycles",
		Status:  StatusError,
		Message: fmt.Sprintf("Found %d circular dependency cycle(s)", len(cycleNodes)),
		Detail:  fmt.Sprintf("First cycle involves: %s", cycleNodes[0]),
		Fix:     "Run 'bd dep cycles' to see full cycle paths, then 'bd dep remove' to break cycles",
	}
}

// loadDependencyEdges reads the blocking edges of both dependency tables as
// one adjacency map, one bounded page per query. Cycle detection used to run
// as a single WITH RECURSIVE path enumeration, which is exponential in dense
// graphs and exceeded per-query read timeouts on shared dolt sql-servers.
//
// Only blocking edge types are traversed, matching issueops.DetectCyclesInTx
// and the cycle prevention on 'bd dep add': non-blocking types (tracks,
// related, discovered-from, ...) legitimately form loops and previously made
// this check disagree with the 'bd dep cycles' command its fix hint points at.
//
// All pages of both tables run inside one transaction so the whole graph is
// read from a single snapshot; on a shared server, per-statement implicit
// transactions could interleave with concurrent edge writes and paginate over
// a shifting id sequence, dropping or duplicating edges.
// Returns a non-nil DoctorCheck on failure.
func loadDependencyEdges(db *sql.DB, pageSize, maxEdges int) (map[string][]string, *DoctorCheck) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dependencyCycleWarning("Unable to check for cycles", err.Error())
	}
	defer func() { _ = tx.Rollback() }()

	edges := make(map[string][]string)
	edgeCount := 0
	for _, table := range dependencyCycleTables {
		var check *DoctorCheck
		if dependencyIDPaginationUsable(ctx, tx, table) {
			check = loadDependencyEdgePages(ctx, tx, table, pageSize, maxEdges, &edgeCount, edges)
		} else {
			check = loadDependencyEdgeScan(ctx, tx, table, maxEdges, &edgeCount, edges)
		}
		if check != nil {
			return nil, check
		}
	}
	return edges, nil
}

// dependencyIDPaginationUsable reports whether `WHERE id > ? ORDER BY id`
// keyset pagination visits every row of table exactly once. That requires an
// id column with no NULLs, and doctor cannot assume either: supported
// databases exist where dependencies.id never materialized (#4690) or is
// mid-backfill NULL (ensureDependenciesIDColumn), and doctor opens the store
// read-only without running the migration chain that repairs those shapes.
// NULL never satisfies `id > ?`, so paginating over such rows would silently
// drop their edges from the graph.
func dependencyIDPaginationUsable(ctx context.Context, tx *sql.Tx, table string) bool {
	var idColumns int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'id'`,
		table).Scan(&idColumns)
	if err != nil || idColumns == 0 {
		return false
	}
	var one int
	//nolint:gosec // G202: table is hardcoded in dependencyCycleTables
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id IS NULL LIMIT 1`).Scan(&one)
	return errors.Is(err, sql.ErrNoRows)
}

// loadDependencyEdgePages loads table via id keyset pagination, bounding the
// work of every query. dependencies.id is a deterministic primary key on the
// current schema (migration 0050); wisp_dependencies.id has been a primary
// key since the table's creation (migration 0021).
func loadDependencyEdgePages(ctx context.Context, tx *sql.Tx, table string, pageSize, maxEdges int, edgeCount *int, edges map[string][]string) *DoctorCheck {
	lastID := ""
	for {
		rowsRead, check := loadDependencyEdgePage(ctx, tx, table, pageSize, maxEdges, &lastID, edgeCount, edges)
		if check != nil {
			return check
		}
		if rowsRead < pageSize {
			return nil
		}
	}
}

func loadDependencyEdgePage(ctx context.Context, tx *sql.Tx, table string, pageSize, maxEdges int, lastID *string, edgeCount *int, edges map[string][]string) (int, *DoctorCheck) {
	//nolint:gosec // G202: table is hardcoded in dependencyCycleTables
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, `+doctorDependencyTargetExpr+` AS depends_on_id, type
		FROM `+table+`
		WHERE id > ?
		ORDER BY id
		LIMIT ?`, *lastID, pageSize)
	if err != nil {
		return 0, dependencyCycleWarning("Unable to check for cycles", table+": "+err.Error())
	}
	defer rows.Close()

	rowsRead := 0
	for rows.Next() {
		var id, issueID, depType string
		var dependsOnID sql.NullString
		if err := rows.Scan(&id, &issueID, &dependsOnID, &depType); err != nil {
			// Must fail loudly: a skipped row would leave rowsRead short of
			// pageSize, silently ending pagination with a truncated graph.
			return 0, dependencyCycleWarning("Unable to check for cycles", "scan "+table+": "+err.Error())
		}
		rowsRead++
		*lastID = id
		if check := addBlockingDependencyEdge(edges, issueID, dependsOnID, depType, maxEdges, edgeCount); check != nil {
			return 0, check
		}
	}
	if err := rows.Err(); err != nil {
		return 0, dependencyCycleWarning("Row iteration error", err.Error())
	}
	return rowsRead, nil
}

// loadDependencyEdgeScan streams table in one query, the same single linear
// pass issueops.AppendBlockingGraphInTx makes. It is the fallback for schema
// shapes where id keyset pagination would be unsound; the exponential work
// this check must avoid was the recursive path enumeration, not the plain
// edge scan 'bd dep cycles' itself relies on.
func loadDependencyEdgeScan(ctx context.Context, tx *sql.Tx, table string, maxEdges int, edgeCount *int, edges map[string][]string) *DoctorCheck {
	//nolint:gosec // G202: table is hardcoded in dependencyCycleTables
	rows, err := tx.QueryContext(ctx, `
		SELECT issue_id, `+doctorDependencyTargetExpr+` AS depends_on_id, type
		FROM `+table)
	if err != nil {
		return dependencyCycleWarning("Unable to check for cycles", table+": "+err.Error())
	}
	defer rows.Close()

	for rows.Next() {
		var issueID, depType string
		var dependsOnID sql.NullString
		if err := rows.Scan(&issueID, &dependsOnID, &depType); err != nil {
			return dependencyCycleWarning("Unable to check for cycles", "scan "+table+": "+err.Error())
		}
		if check := addBlockingDependencyEdge(edges, issueID, dependsOnID, depType, maxEdges, edgeCount); check != nil {
			return check
		}
	}
	if err := rows.Err(); err != nil {
		return dependencyCycleWarning("Row iteration error", err.Error())
	}
	return nil
}

// addBlockingDependencyEdge adds one row to the adjacency map if it is a
// blocking edge. The type filter runs client-side like
// issueops.AppendBlockingGraphInTx: a WHERE on type would let one paginated
// page scan an unbounded id range when blocking edges are sparse, defeating
// the bounded-work-per-query guarantee. Returns the graph-too-large warning
// once the in-memory graph would exceed maxEdges.
func addBlockingDependencyEdge(edges map[string][]string, issueID string, dependsOnID sql.NullString, depType string, maxEdges int, edgeCount *int) *DoctorCheck {
	blocking := types.DependencyType(depType) == types.DepBlocks ||
		types.DependencyType(depType) == types.DepConditionalBlocks
	if !blocking || !dependsOnID.Valid {
		return nil
	}
	if *edgeCount >= maxEdges {
		return dependencyCycleWarning("Unable to check for cycles",
			fmt.Sprintf("dependency graph too large (more than %d edges)", maxEdges))
	}
	edges[issueID] = append(edges[issueID], dependsOnID.String)
	*edgeCount++
	return nil
}

func dependencyCycleWarning(message, detail string) *DoctorCheck {
	return &DoctorCheck{
		Name:    "Dependency Cycles",
		Status:  StatusWarning,
		Message: message,
		Detail:  detail,
	}
}

// dependencyCycleNodes returns the sorted ids of every node that participates
// in at least one dependency cycle: members of any strongly connected
// component of size >= 2, plus nodes with a self-edge. This matches the set
// the recursive SQL reported (nodes with some simple path back to themselves).
func dependencyCycleNodes(edges map[string][]string) []string {
	type frame struct {
		node string
		succ int
	}

	index := make(map[string]int, len(edges))
	low := make(map[string]int, len(edges))
	onStack := make(map[string]bool, len(edges))
	var sccStack []string
	next := 0
	var cycleNodes []string

	for root := range edges {
		if _, seen := index[root]; seen {
			continue
		}
		frames := []frame{{node: root}}
		for len(frames) > 0 {
			f := &frames[len(frames)-1]
			v := f.node
			if f.succ == 0 {
				index[v] = next
				low[v] = next
				next++
				sccStack = append(sccStack, v)
				onStack[v] = true
			}
			descended := false
			for f.succ < len(edges[v]) {
				w := edges[v][f.succ]
				f.succ++
				if _, seen := index[w]; !seen {
					frames = append(frames, frame{node: w})
					descended = true
					break
				}
				if onStack[w] && index[w] < low[v] {
					low[v] = index[w]
				}
			}
			if descended {
				continue
			}
			if low[v] == index[v] {
				scc := popSCC(&sccStack, onStack, v)
				if len(scc) > 1 || hasSelfEdge(edges, v) {
					cycleNodes = append(cycleNodes, scc...)
				}
			}
			frames = frames[:len(frames)-1]
			if len(frames) > 0 {
				parent := frames[len(frames)-1].node
				if low[v] < low[parent] {
					low[parent] = low[v]
				}
			}
		}
	}

	sort.Strings(cycleNodes)
	return cycleNodes
}

func popSCC(sccStack *[]string, onStack map[string]bool, v string) []string {
	var scc []string
	for {
		s := *sccStack
		w := s[len(s)-1]
		*sccStack = s[:len(s)-1]
		onStack[w] = false
		scc = append(scc, w)
		if w == v {
			return scc
		}
	}
}

func hasSelfEdge(edges map[string][]string, v string) bool {
	for _, w := range edges[v] {
		if w == v {
			return true
		}
	}
	return false
}

// CheckDeletionsManifest checks the status of the legacy deletions.jsonl file
func CheckDeletionsManifest(path string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Deletions Manifest",
			Status:  StatusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Deletions Manifest",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// Check if deletions.jsonl exists
	info, err := os.Stat(deletionsPath)
	if err == nil {
		// File exists - count entries (empty file is valid, means no deletions)
		if info.Size() == 0 {
			return DoctorCheck{
				Name:    "Deletions Manifest",
				Status:  StatusOK,
				Message: "Empty (no legacy deletions)",
			}
		}
		file, err := os.Open(deletionsPath) // #nosec G304 - controlled path
		if err == nil {
			defer file.Close()
			count := 0
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				if len(scanner.Bytes()) > 0 {
					count++
				}
			}
			if count > 0 {
				return DoctorCheck{
					Name:    "Deletions Manifest",
					Status:  StatusWarning,
					Message: fmt.Sprintf("Legacy format (%d entries)", count),
					Detail:  "deletions.jsonl is a legacy format no longer used",
					Fix:     "Safe to delete deletions.jsonl (Dolt handles delete propagation natively)",
				}
			}
			return DoctorCheck{
				Name:    "Deletions Manifest",
				Status:  StatusOK,
				Message: "Empty (no legacy deletions)",
			}
		}
	}

	// deletions.jsonl doesn't exist - this is the expected state
	// Check for .migrated file to confirm migration happened
	migratedPath := filepath.Join(beadsDir, "deletions.jsonl.migrated")
	if _, err := os.Stat(migratedPath); err == nil {
		return DoctorCheck{
			Name:    "Deletions Manifest",
			Status:  StatusOK,
			Message: "Migrated (legacy file removed)",
		}
	}

	// No deletions.jsonl - expected for Dolt-native repos
	return DoctorCheck{
		Name:    "Deletions Manifest",
		Status:  StatusOK,
		Message: "Not needed (Dolt-native)",
	}
}

// CheckRepoFingerprint validates that the database belongs to this repository.
// This detects when a .beads directory was copied from another repo or when
// the git remote URL changed. A mismatch can cause data loss during sync.
// Opens its own store; prefer CheckRepoFingerprintWithStore when a shared store is available.
func CheckRepoFingerprint(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	if info, err := os.Stat(getDatabasePath(beadsDir)); err != nil || !info.IsDir() {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer func() { _ = store.Close() }()

	return checkRepoFingerprintWithStore(store, path)
}

// CheckRepoFingerprintWithStore checks repo fingerprint using a shared store (GH#2636).
func CheckRepoFingerprintWithStore(ss *SharedStore, path string) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}
	return checkRepoFingerprintWithStore(store, path)
}

func checkRepoFingerprintWithStore(store *dolt.DoltStore, path string) DoctorCheck {
	ctx := context.Background()

	storedRepoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Unable to read repo fingerprint",
			Detail:  err.Error(),
		}
	}

	if storedRepoID == "" {
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Missing repo fingerprint metadata",
			Detail:  "Storage: Dolt",
			Fix:     "Run 'bd doctor --fix' to repair metadata",
		}
	}

	currentRepoID, currentSource, err := beads.ComputeRepoIDForPathWithSource(path)
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusOK,
				Message: "N/A (not a git repository)",
			}
		}
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusWarning,
			Message: "Unable to compute current repo ID",
			Detail:  err.Error(),
		}
	}

	return classifyRepoFingerprint(storedRepoID, currentRepoID, currentSource)
}

// classifyRepoFingerprint turns a stored-vs-current fingerprint comparison into
// a doctor check. Pure so the mismatch branches are unit-testable without a
// store.
func classifyRepoFingerprint(storedRepoID, currentRepoID string, currentSource beads.RepoIDSource) DoctorCheck {
	if storedRepoID != currentRepoID {
		// bd-46vla: with no origin remote here, the local fingerprint is a
		// path hash that can never match a remote-derived stored id — the
		// signature of a synced clone on a host without the canonical remote.
		// The stored id is the shared value; repo_id lives in the VERSIONED
		// metadata table, so 'bd migrate --update-repo-id' would stamp this
		// host's path hash into shared state and propagate it to every clone
		// on the next sync (the GH#4361 class).
		if currentSource == beads.RepoIDSourcePath {
			return DoctorCheck{
				Name:    "Repo Fingerprint",
				Status:  StatusWarning,
				Message: "Fingerprint differs, but this checkout has no origin remote",
				Detail:  fmt.Sprintf("stored: %s, current (path hash): %s — on a synced clone the stored id is the canonical shared value and this mismatch is cosmetic", truncateID(storedRepoID), truncateID(currentRepoID)),
				Fix:     "On a synced clone, leave it (or add the canonical origin remote). Only run 'bd migrate --update-repo-id' if this checkout is the canonical repository — the new id propagates to every clone on the next sync",
			}
		}
		return DoctorCheck{
			Name:    "Repo Fingerprint",
			Status:  StatusError,
			Message: "Database belongs to different repository",
			Detail:  fmt.Sprintf("stored: %s, current: %s", truncateID(storedRepoID), truncateID(currentRepoID)),
			Fix:     "Run 'bd migrate --update-repo-id' if URL changed, or 'rm -rf .beads && bd init' if wrong database",
		}
	}

	return DoctorCheck{
		Name:    "Repo Fingerprint",
		Status:  StatusOK,
		Message: fmt.Sprintf("Verified (%s)", truncateID(currentRepoID)),
	}
}

// Helper functions

// truncateID safely truncates an ID to at most 8 characters for display.
func truncateID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// DetectHashBasedIDs uses multiple heuristics to determine if the database uses hash-based IDs.
// This is more robust than checking a single ID's format, since base36 hash IDs can be all-numeric.
func DetectHashBasedIDs(db *sql.DB, sampleIDs []string) bool {
	// Heuristic 1: Check for child_counters table (added for hash ID support)
	// Use a direct query to check for the table's existence.
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM child_counters").Scan(&count)
	if err == nil {
		// child_counters table exists - this is a strong indicator of hash IDs
		return true
	}

	// Heuristic 2: Check if any sample ID clearly contains letters (a-z)
	// Hash IDs use base36 (0-9, a-z), sequential IDs are purely numeric
	for _, id := range sampleIDs {
		if isHashID(id) {
			return true
		}
	}

	// Heuristic 3: Look for patterns that indicate hash IDs
	if len(sampleIDs) >= 2 {
		// Extract suffixes (part after prefix-) for analysis
		var suffixes []string
		for _, id := range sampleIDs {
			parts := strings.SplitN(id, "-", 2)
			if len(parts) == 2 {
				// Strip hierarchical suffix like .1 or .1.2
				baseSuffix := strings.Split(parts[1], ".")[0]
				suffixes = append(suffixes, baseSuffix)
			}
		}

		if len(suffixes) >= 2 {
			// Check for variable lengths (strong indicator of adaptive hash IDs)
			// BUT: sequential IDs can also have variable length (1, 10, 100)
			// So we need to check if the length variation is natural (1→2→3 digits)
			// or random (3→8→4 chars typical of adaptive hash IDs)
			lengths := make(map[int]int) // length -> count
			for _, s := range suffixes {
				lengths[len(s)]++
			}

			// If we have 3+ different lengths, likely hash IDs (adaptive length)
			// Sequential IDs typically have 1-2 lengths (e.g., 1-9, 10-99, 100-999)
			if len(lengths) >= 3 {
				return true
			}

			// Check for leading zeros (rare in sequential IDs, common in hash IDs)
			// Sequential IDs: bd-1, bd-2, bd-10, bd-100
			// Hash IDs: bd-0088, bd-02a4, bd-05a1
			hasLeadingZero := false
			for _, s := range suffixes {
				if len(s) > 1 && s[0] == '0' {
					hasLeadingZero = true
					break
				}
			}
			if hasLeadingZero {
				return true
			}

			// Check for non-sequential ordering
			// Try to parse as integers - if they're not sequential, likely hash IDs
			allNumeric := true
			var nums []int
			for _, s := range suffixes {
				var num int
				if _, err := fmt.Sscanf(s, "%d", &num); err == nil {
					nums = append(nums, num)
				} else {
					allNumeric = false
					break
				}
			}

			if allNumeric && len(nums) >= 2 {
				// Check if they form a roughly sequential pattern (1,2,3 or 10,11,12)
				// Hash IDs would be more random (e.g., 88, 13452, 676)
				isSequentialPattern := true
				for i := 1; i < len(nums); i++ {
					diff := nums[i] - nums[i-1]
					// Allow for some gaps (deleted issues), but should be mostly sequential
					if diff < 0 || diff > 100 {
						isSequentialPattern = false
						break
					}
				}
				// If the numbers are NOT sequential, they're likely hash IDs
				if !isSequentialPattern {
					return true
				}
			}
		}
	}

	// If we can't determine for sure, default to assuming sequential IDs
	// This is conservative - better to recommend migration than miss sequential IDs
	return false
}

// isHashID checks if a single ID contains hash characteristics
// Hash IDs contain hex letters (a-f), sequential IDs are only digits
// May have hierarchical suffix like .1 or .1.2
func isHashID(id string) bool {
	lastSeperatorIndex := strings.LastIndex(id, "-")
	if lastSeperatorIndex == -1 {
		return false
	}

	suffix := id[lastSeperatorIndex+1:]
	// Strip hierarchical suffix like .1 or .1.2
	baseSuffix := strings.Split(suffix, ".")[0]

	if len(baseSuffix) == 0 {
		return false
	}

	// Must be valid Base36 (0-9, a-z)
	if !regexp.MustCompile(`^[0-9a-z]+$`).MatchString(baseSuffix) {
		return false
	}

	// If it's 5+ characters long, it's almost certainly a hash ID
	// (sequential IDs rarely exceed 9999 = 4 digits)
	if len(baseSuffix) >= 5 {
		return true
	}

	// For shorter IDs, check if it contains any letter (a-z)
	// Sequential IDs are purely numeric
	return regexp.MustCompile(`[a-z]`).MatchString(baseSuffix)
}
