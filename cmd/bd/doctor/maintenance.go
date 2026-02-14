package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/types"
)

// CheckStaleClosedIssues detects closed issues that could be cleaned up.
//
// Design note: Time-based thresholds are a crude proxy for the real concern,
// which is database size. A repo with 100 closed issues from 5 years ago
// doesn't need cleanup, while 50,000 issues from yesterday might.
// The actual threshold should be based on acceptable maximum database size.
//
// This check is DISABLED by default (stale_closed_issues_days=0). Users who
// want time-based pruning must explicitly enable it in metadata.json.
// Future: Consider adding max_database_size_mb for size-based thresholds.

// largeClosedIssuesThreshold triggers a warning to enable stale cleanup
const largeClosedIssuesThreshold = 10000

func CheckStaleClosedIssues(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	// Load config and check if this check is enabled
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "N/A (config error)",
			Category: CategoryMaintenance,
		}
	}

	// If config is nil, use defaults (check disabled)
	var thresholdDays int
	if cfg != nil {
		thresholdDays = cfg.GetStaleClosedIssuesDays()
	}

	// Use SQL COUNT queries instead of loading all issues into memory.
	// The old approach loaded every closed issue via SearchIssues which was
	// catastrophically slow on large databases (57 seconds for ~23k issues
	// over MySQL wire protocol).
	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryMaintenance,
		}
	}
	defer func() { _ = store.Close() }()

	// If disabled (0), check for large closed issue count and warn if appropriate
	if thresholdDays == 0 {
		var closedCount int
		err := db.QueryRow("SELECT COUNT(*) FROM issues WHERE status = 'closed'").Scan(&closedCount)
		if err != nil || closedCount < largeClosedIssuesThreshold {
			return DoctorCheck{
				Name:     "Stale Closed Issues",
				Status:   StatusOK,
				Message:  "Disabled (set stale_closed_issues_days to enable)",
				Category: CategoryMaintenance,
			}
		}
		// Large number of closed issues - recommend enabling cleanup
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Disabled but %d closed issues exist", closedCount),
			Detail:   "Consider enabling stale_closed_issues_days to manage database size",
			Fix:      "Add \"stale_closed_issues_days\": 30 to .beads/metadata.json",
			Category: CategoryMaintenance,
		}
	}

	// Find closed issues older than configured threshold, excluding pinned
	cutoff := time.Now().AddDate(0, 0, -thresholdDays).Format(time.RFC3339)
	var cleanable int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM issues WHERE status = 'closed' AND closed_at < ? AND (pinned = 0 OR pinned IS NULL)",
		cutoff,
	).Scan(&cleanable)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "N/A (query failed)",
			Category: CategoryMaintenance,
		}
	}

	if cleanable == 0 {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "No stale closed issues",
			Category: CategoryMaintenance,
		}
	}

	return DoctorCheck{
		Name:     "Stale Closed Issues",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d closed issue(s) older than %d days", cleanable, thresholdDays),
		Detail:   "These issues can be cleaned up to reduce database size",
		Fix:      "Run 'bd doctor --fix' to cleanup, or 'bd admin cleanup --force' for more options",
		Category: CategoryMaintenance,
	}
}

// CheckExpiredTombstones detects tombstones that have exceeded their TTL.
func CheckExpiredTombstones(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Expired Tombstones",
			Status:   StatusOK,
			Message:  "N/A (no JSONL file)",
			Category: CategoryMaintenance,
		}
	}

	// Read JSONL and count expired tombstones
	file, err := os.Open(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return DoctorCheck{
			Name:     "Expired Tombstones",
			Status:   StatusOK,
			Message:  "N/A (unable to read JSONL)",
			Category: CategoryMaintenance,
		}
	}
	defer file.Close()

	var expiredCount int
	decoder := json.NewDecoder(file)
	ttl := types.DefaultTombstoneTTL

	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}
		issue.SetDefaults()
		if issue.IsExpired(ttl) {
			expiredCount++
		}
	}

	if expiredCount == 0 {
		return DoctorCheck{
			Name:     "Expired Tombstones",
			Status:   StatusOK,
			Message:  "No expired tombstones",
			Category: CategoryMaintenance,
		}
	}

	ttlDays := int(ttl.Hours() / 24)
	return DoctorCheck{
		Name:     "Expired Tombstones",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d tombstone(s) older than %d days", expiredCount, ttlDays),
		Detail:   "Expired tombstones can be pruned to reduce JSONL file size",
		Fix:      "Run 'bd doctor --fix' to prune, or 'bd admin cleanup --force' for more options",
		Category: CategoryMaintenance,
	}
}

// CheckStaleMolecules detects complete-but-unclosed molecules.
// A molecule is stale if all children are closed but the root is still open.
func CheckStaleMolecules(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	// Open database using factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
	ctx := context.Background()
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Molecules",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryMaintenance,
		}
	}
	defer func() { _ = store.Close() }()

	// Get all epics eligible for closure (complete but unclosed)
	epicStatuses, err := store.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Molecules",
			Status:   StatusOK,
			Message:  "N/A (query failed)",
			Category: CategoryMaintenance,
		}
	}

	// Count stale molecules (eligible for close with at least 1 child)
	var staleCount int
	var staleIDs []string
	for _, es := range epicStatuses {
		if es.EligibleForClose && es.TotalChildren > 0 {
			staleCount++
			if len(staleIDs) < 3 {
				staleIDs = append(staleIDs, es.Epic.ID)
			}
		}
	}

	if staleCount == 0 {
		return DoctorCheck{
			Name:     "Stale Molecules",
			Status:   StatusOK,
			Message:  "No stale molecules",
			Category: CategoryMaintenance,
		}
	}

	detail := fmt.Sprintf("Example: %v", staleIDs)
	if staleCount > 3 {
		detail += fmt.Sprintf(" (+%d more)", staleCount-3)
	}

	return DoctorCheck{
		Name:     "Stale Molecules",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d complete-but-unclosed molecule(s)", staleCount),
		Detail:   detail,
		Fix:      "Run 'bd mol stale' to review, then 'bd close <id>' for each",
		Category: CategoryMaintenance,
	}
}

// CheckCompactionCandidates detects issues eligible for compaction.
// Note: Compaction is a SQLite-specific optimization. Dolt backends don't need compaction
// as Dolt handles data management differently.
func CheckCompactionCandidates(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Dolt backend: this check uses SQLite-specific queries, skip for now
	if backend == configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (dolt backend)",
			Category: CategoryMaintenance,
		}
	}

	// Check if backend is SQLite - compaction only applies to SQLite
	cfg, _ := configfile.Load(beadsDir)
	if cfg != nil && cfg.GetBackend() != configfile.BackendSQLite {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (compaction only applies to SQLite backend)",
			Category: CategoryMaintenance,
		}
	}

	// Open database using factory
	ctx := context.Background()
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryMaintenance,
		}
	}
	defer func() { _ = store.Close() }()

	// Type assert to CompactableStorage for compaction methods
	compactStore, ok := store.(storage.CompactableStorage)
	if !ok {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (backend does not support compaction)",
			Category: CategoryMaintenance,
		}
	}

	tier1, err := compactStore.GetTier1Candidates(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (query failed)",
			Category: CategoryMaintenance,
		}
	}

	if len(tier1) == 0 {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "No compaction candidates",
			Category: CategoryMaintenance,
		}
	}

	// Calculate total size
	var totalSize int
	for _, c := range tier1 {
		totalSize += c.OriginalSize
	}

	return DoctorCheck{
		Name:     "Compaction Candidates",
		Status:   StatusOK, // Info only, not a warning
		Message:  fmt.Sprintf("%d issue(s) eligible for compaction (%d bytes)", len(tier1), totalSize),
		Detail:   "Compaction requires agent review; not auto-fixable",
		Fix:      "Run 'bd compact --analyze' to review candidates",
		Category: CategoryMaintenance,
	}
}

// resolveBeadsDir follows a redirect file if present in the beads directory.
// This handles the redirect mechanism where .beads/redirect points to
// the actual beads directory location (used in multi-clone setups).
// This is a wrapper around beads.FollowRedirect for use within the doctor package.
func resolveBeadsDir(beadsDir string) string {
	return beads.FollowRedirect(beadsDir)
}

// CheckPersistentMolIssues detects mol- prefixed issues that should have been ephemeral.
// When users run "bd mol pour" on formulas that should use "bd mol wisp", the resulting
// issues get the "mol-" prefix but persist in JSONL. These should be cleaned up.
func CheckPersistentMolIssues(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Persistent Mol Issues",
			Status:   StatusOK,
			Message:  "N/A (no JSONL file)",
			Category: CategoryMaintenance,
		}
	}

	// Read JSONL and count mol- prefixed issues that are not ephemeral
	file, err := os.Open(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return DoctorCheck{
			Name:     "Persistent Mol Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to read JSONL)",
			Category: CategoryMaintenance,
		}
	}
	defer file.Close()

	var molCount int
	var molIDs []string
	decoder := json.NewDecoder(file)

	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}
		// Skip deleted issues (tombstones)
		if issue.DeletedAt != nil {
			continue
		}
		// Look for mol- prefix that shouldn't be in JSONL
		// (ephemeral issues have Ephemeral=true and don't get exported)
		if strings.HasPrefix(issue.ID, "mol-") && !issue.Ephemeral {
			molCount++
			if len(molIDs) < 3 {
				molIDs = append(molIDs, issue.ID)
			}
		}
	}

	if molCount == 0 {
		return DoctorCheck{
			Name:     "Persistent Mol Issues",
			Status:   StatusOK,
			Message:  "No persistent mol- issues",
			Category: CategoryMaintenance,
		}
	}

	detail := fmt.Sprintf("Example: %v", molIDs)
	if molCount > 3 {
		detail += fmt.Sprintf(" (+%d more)", molCount-3)
	}

	return DoctorCheck{
		Name:     "Persistent Mol Issues",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d mol- issue(s) in JSONL should be ephemeral", molCount),
		Detail:   detail,
		Fix:      "Run 'bd delete <id> --force' to remove, or use 'bd mol wisp' instead of 'bd mol pour'",
		Category: CategoryMaintenance,
	}
}

// CheckStaleMQFiles detects legacy .beads/mq/*.json files from gastown.
// These files are LOCAL ONLY (not committed) and represent stale merge queue
// entries from the old mrqueue implementation. They are safe to delete since
// gt done already creates merge-request wisps in beads.
func CheckStaleMQFiles(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	mqDir := filepath.Join(beadsDir, "mq")

	if _, err := os.Stat(mqDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Legacy MQ Files",
			Status:   StatusOK,
			Message:  "No legacy merge queue files",
			Category: CategoryMaintenance,
		}
	}

	files, err := filepath.Glob(filepath.Join(mqDir, "*.json"))
	if err != nil || len(files) == 0 {
		return DoctorCheck{
			Name:     "Legacy MQ Files",
			Status:   StatusOK,
			Message:  "No legacy merge queue files",
			Category: CategoryMaintenance,
		}
	}

	return DoctorCheck{
		Name:     "Legacy MQ Files",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d stale .beads/mq/*.json file(s)", len(files)),
		Detail:   "Legacy gastown merge queue files (local only, safe to delete)",
		Fix:      "Run 'bd doctor --fix' to delete, or 'rm -rf .beads/mq/'",
		Category: CategoryMaintenance,
	}
}

// FixStaleMQFiles removes the legacy .beads/mq/ directory and all its contents.
func FixStaleMQFiles(path string) error {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	mqDir := filepath.Join(beadsDir, "mq")

	if _, err := os.Stat(mqDir); os.IsNotExist(err) {
		return nil // Nothing to do
	}

	if err := os.RemoveAll(mqDir); err != nil {
		return fmt.Errorf("failed to remove %s: %w", mqDir, err)
	}

	return nil
}

// checkMisclassifiedWisps detects wisp-patterned issues that lack the ephemeral flag.
// Issues with IDs containing "-wisp-" should always have Ephemeral=true.
// If they're in JSONL without the ephemeral flag, they'll pollute bd ready.
func checkMisclassifiedWisps(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Misclassified Wisps",
			Status:   StatusOK,
			Message:  "N/A (no JSONL file)",
			Category: CategoryMaintenance,
		}
	}

	// Read JSONL and find wisp-patterned issues without ephemeral flag
	file, err := os.Open(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return DoctorCheck{
			Name:     "Misclassified Wisps",
			Status:   StatusOK,
			Message:  "N/A (unable to read JSONL)",
			Category: CategoryMaintenance,
		}
	}
	defer file.Close()

	var wispCount int
	var wispIDs []string
	decoder := json.NewDecoder(file)

	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}
		// Skip deleted issues (tombstones)
		if issue.DeletedAt != nil {
			continue
		}
		// Look for wisp pattern without ephemeral flag
		// These shouldn't be in JSONL at all (wisps are ephemeral)
		if strings.Contains(issue.ID, "-wisp-") && !issue.Ephemeral {
			wispCount++
			if len(wispIDs) < 3 {
				wispIDs = append(wispIDs, issue.ID)
			}
		}
	}

	if wispCount == 0 {
		return DoctorCheck{
			Name:     "Misclassified Wisps",
			Status:   StatusOK,
			Message:  "No misclassified wisps found",
			Category: CategoryMaintenance,
		}
	}

	detail := fmt.Sprintf("Example: %v", wispIDs)
	if wispCount > 3 {
		detail += fmt.Sprintf(" (+%d more)", wispCount-3)
	}

	return DoctorCheck{
		Name:     "Misclassified Wisps",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d wisp issue(s) in JSONL missing ephemeral flag", wispCount),
		Detail:   detail,
		Fix:      "Remove from JSONL: grep -v '\"id\":\"<id>\"' issues.jsonl > tmp && mv tmp issues.jsonl",
		Category: CategoryMaintenance,
	}
}

// PatrolPollutionThresholds defines when to warn about patrol pollution
const (
	PatrolDigestThreshold = 10 // Warn if patrol digests > 10
	SessionBeadThreshold  = 50 // Warn if session beads > 50
)

// patrolPollutionResult contains counts of detected pollution beads
type patrolPollutionResult struct {
	PatrolDigestCount int      // Count of "Digest: mol-*-patrol" beads
	SessionBeadCount  int      // Count of "Session ended: *" beads
	PatrolDigestIDs   []string // Sample IDs for display
	SessionBeadIDs    []string // Sample IDs for display
}

// CheckPatrolPollution detects patrol digest and session ended beads that pollute the database.
// These beads are created during patrol operations and should not persist in the database.
//
// Patterns detected:
// - Patrol digests: titles matching "Digest: mol-*-patrol"
// - Session ended beads: titles matching "Session ended: *"
func CheckPatrolPollution(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Patrol Pollution",
			Status:   StatusOK,
			Message:  "N/A (no JSONL file)",
			Category: CategoryMaintenance,
		}
	}

	// Read JSONL and count pollution beads
	file, err := os.Open(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return DoctorCheck{
			Name:     "Patrol Pollution",
			Status:   StatusOK,
			Message:  "N/A (unable to read JSONL)",
			Category: CategoryMaintenance,
		}
	}
	defer file.Close()

	result := detectPatrolPollution(file)

	// Check thresholds
	hasPatrolPollution := result.PatrolDigestCount > PatrolDigestThreshold
	hasSessionPollution := result.SessionBeadCount > SessionBeadThreshold

	if !hasPatrolPollution && !hasSessionPollution {
		return DoctorCheck{
			Name:     "Patrol Pollution",
			Status:   StatusOK,
			Message:  "No patrol pollution detected",
			Category: CategoryMaintenance,
		}
	}

	// Build warning message
	var warnings []string
	if hasPatrolPollution {
		warnings = append(warnings, fmt.Sprintf("%d patrol digest beads (should be 0)", result.PatrolDigestCount))
	}
	if hasSessionPollution {
		warnings = append(warnings, fmt.Sprintf("%d session ended beads (should be wisps)", result.SessionBeadCount))
	}

	// Build detail with sample IDs
	var details []string
	if len(result.PatrolDigestIDs) > 0 {
		details = append(details, fmt.Sprintf("Patrol digests: %v", result.PatrolDigestIDs))
	}
	if len(result.SessionBeadIDs) > 0 {
		details = append(details, fmt.Sprintf("Session beads: %v", result.SessionBeadIDs))
	}

	return DoctorCheck{
		Name:     "Patrol Pollution",
		Status:   StatusWarning,
		Message:  strings.Join(warnings, ", "),
		Detail:   strings.Join(details, "; "),
		Fix:      "Run 'bd doctor --fix' to clean up patrol pollution",
		Category: CategoryMaintenance,
	}
}

// detectPatrolPollution scans a JSONL file for patrol pollution patterns
func detectPatrolPollution(file *os.File) patrolPollutionResult {
	var result patrolPollutionResult
	decoder := json.NewDecoder(file)

	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}

		// Skip tombstones
		if issue.DeletedAt != nil {
			continue
		}

		title := issue.Title

		// Check for patrol digest pattern: "Digest: mol-*-patrol"
		if strings.HasPrefix(title, "Digest: mol-") && strings.HasSuffix(title, "-patrol") {
			result.PatrolDigestCount++
			if len(result.PatrolDigestIDs) < 3 {
				result.PatrolDigestIDs = append(result.PatrolDigestIDs, issue.ID)
			}
			continue
		}

		// Check for session ended pattern: "Session ended: *"
		if strings.HasPrefix(title, "Session ended:") {
			result.SessionBeadCount++
			if len(result.SessionBeadIDs) < 3 {
				result.SessionBeadIDs = append(result.SessionBeadIDs, issue.ID)
			}
		}
	}

	return result
}

// getPatrolPollutionIDs returns all IDs of patrol pollution beads for deletion
func getPatrolPollutionIDs(path string) ([]string, error) {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	file, err := os.Open(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return nil, fmt.Errorf("failed to open issues.jsonl: %w", err)
	}
	defer file.Close()

	var ids []string
	decoder := json.NewDecoder(file)

	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}

		// Skip tombstones
		if issue.DeletedAt != nil {
			continue
		}

		title := issue.Title

		// Check for patrol digest pattern
		if strings.HasPrefix(title, "Digest: mol-") && strings.HasSuffix(title, "-patrol") {
			ids = append(ids, issue.ID)
			continue
		}

		// Check for session ended pattern
		if strings.HasPrefix(title, "Session ended:") {
			ids = append(ids, issue.ID)
		}
	}

	return ids, nil
}
