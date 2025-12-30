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
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// DefaultCleanupAgeDays is the default age threshold for cleanup suggestions
const DefaultCleanupAgeDays = 30

// CheckStaleClosedIssues detects closed issues that could be cleaned up.
// This consolidates the cleanup command into doctor checks.
func CheckStaleClosedIssues(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryMaintenance,
		}
	}

	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryMaintenance,
		}
	}
	defer func() { _ = store.Close() }()

	// Find closed issues older than threshold
	cutoff := time.Now().AddDate(0, 0, -DefaultCleanupAgeDays)
	statusClosed := types.StatusClosed
	filter := types.IssueFilter{
		Status:       &statusClosed,
		ClosedBefore: &cutoff,
	}

	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return DoctorCheck{
			Name:     "Stale Closed Issues",
			Status:   StatusOK,
			Message:  "N/A (query failed)",
			Category: CategoryMaintenance,
		}
	}

	// Filter out pinned issues
	var cleanable int
	for _, issue := range issues {
		if !issue.Pinned {
			cleanable++
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
		Message:  fmt.Sprintf("%d closed issue(s) older than %d days", cleanable, DefaultCleanupAgeDays),
		Detail:   "These issues can be cleaned up to reduce database size",
		Fix:      "Run 'bd doctor --fix' to cleanup, or 'bd cleanup --force' for more options",
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
		Fix:      "Run 'bd doctor --fix' to prune, or 'bd cleanup --force' for more options",
		Category: CategoryMaintenance,
	}
}

// CheckStaleMolecules detects complete-but-unclosed molecules.
// A molecule is stale if all children are closed but the root is still open.
func CheckStaleMolecules(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Stale Molecules",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryMaintenance,
		}
	}

	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
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
func CheckCompactionCandidates(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryMaintenance,
		}
	}

	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return DoctorCheck{
			Name:     "Compaction Candidates",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryMaintenance,
		}
	}
	defer func() { _ = store.Close() }()

	tier1, err := store.GetTier1Candidates(ctx)
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
