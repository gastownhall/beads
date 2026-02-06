//go:build cgo
package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	storagefactory "github.com/steveyegge/beads/internal/storage/factory"
)

// CheckFederationRemotesAPI checks if the remotesapi port is accessible for federation.
// This is the port used for peer-to-peer sync operations.
func CheckFederationRemotesAPI(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	// Check if server PID file exists (indicates server mode might be running)
	pidFile := filepath.Join(doltPath, "dolt-server.pid")
	serverPID := dolt.GetRunningServerPID(doltPath)

	if serverPID == 0 {
		// No server running - check if we have remotes configured
		ctx := context.Background()
		store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
		if err != nil {
			return DoctorCheck{
				Name:     "Federation remotesapi",
				Status:   StatusOK,
				Message:  "N/A (embedded mode, no remotes check needed)",
				Category: CategoryFederation,
			}
		}
		defer func() { _ = store.Close() }()

		fedStore, ok := storage.AsFederated(store)
		if !ok {
			return DoctorCheck{
				Name:     "Federation remotesapi",
				Status:   StatusOK,
				Message:  "N/A (storage does not support federation)",
				Category: CategoryFederation,
			}
		}

		remotes, err := fedStore.ListRemotes(ctx)
		if err != nil || len(remotes) == 0 {
			return DoctorCheck{
				Name:     "Federation remotesapi",
				Status:   StatusOK,
				Message:  "N/A (no peers configured)",
				Category: CategoryFederation,
			}
		}

		// Has remotes but no server running - suggest starting in federation mode
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Server not running (%d peers configured)", len(remotes)),
			Detail:   "Federation requires dolt sql-server for peer sync",
			Fix:      "Run 'bd daemon start --federation' to enable peer-to-peer sync",
			Category: CategoryFederation,
		}
	}

	// Server is running - check if remotesapi port is accessible
	// Default remotesapi port is 8080
	remotesAPIPort := dolt.DefaultRemotesAPIPort
	host := "127.0.0.1"

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", remotesAPIPort))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusError,
			Message:  fmt.Sprintf("remotesapi port %d not accessible", remotesAPIPort),
			Detail:   fmt.Sprintf("Server PID %d found in %s but port unreachable: %v", serverPID, pidFile, err),
			Fix:      "Check if dolt sql-server is running with --remotesapi-port flag",
			Category: CategoryFederation,
		}
	}
	_ = conn.Close()

	return DoctorCheck{
		Name:     "Federation remotesapi",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Port %d accessible", remotesAPIPort),
		Category: CategoryFederation,
	}
}

// CheckFederationPeerConnectivity checks if configured peer remotes are reachable.
func CheckFederationPeerConnectivity(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	fedStore, ok := storage.AsFederated(store)
	if !ok {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "N/A (storage does not support federation)",
			Category: CategoryFederation,
		}
	}

	remotes, err := fedStore.ListRemotes(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusWarning,
			Message:  "Unable to list remotes",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	if len(remotes) == 0 {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "No peers configured",
			Category: CategoryFederation,
		}
	}

	// Try to get sync status for each peer (this doesn't require network for cached data)
	var reachable, unreachable []string
	var statusDetails []string

	for _, remote := range remotes {
		// Skip origin - it's typically the DoltHub remote, not a peer
		if remote.Name == "origin" {
			continue
		}

		status, err := fedStore.SyncStatus(ctx, remote.Name)
		if err != nil {
			unreachable = append(unreachable, remote.Name)
			statusDetails = append(statusDetails, fmt.Sprintf("%s: %v", remote.Name, err))
		} else {
			reachable = append(reachable, remote.Name)
			if status.LocalAhead > 0 || status.LocalBehind > 0 {
				statusDetails = append(statusDetails, fmt.Sprintf("%s: %d ahead, %d behind",
					remote.Name, status.LocalAhead, status.LocalBehind))
			}
		}
	}

	// If no peers (only origin), report as OK
	if len(reachable) == 0 && len(unreachable) == 0 {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "No federation peers configured (only origin remote)",
			Category: CategoryFederation,
		}
	}

	if len(unreachable) > 0 {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d/%d peers unreachable", len(unreachable), len(reachable)+len(unreachable)),
			Detail:   strings.Join(statusDetails, "\n"),
			Fix:      "Check peer URLs and network connectivity",
			Category: CategoryFederation,
		}
	}

	msg := fmt.Sprintf("%d peers reachable", len(reachable))
	detail := ""
	if len(statusDetails) > 0 {
		detail = strings.Join(statusDetails, "\n")
	}

	return DoctorCheck{
		Name:     "Peer Connectivity",
		Status:   StatusOK,
		Message:  msg,
		Detail:   detail,
		Category: CategoryFederation,
	}
}

// CheckFederationSyncStaleness checks for stale sync status with peers.
func CheckFederationSyncStaleness(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	fedStore, ok := storage.AsFederated(store)
	if !ok {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "N/A (storage does not support federation)",
			Category: CategoryFederation,
		}
	}

	remotes, err := fedStore.ListRemotes(ctx)
	if err != nil || len(remotes) == 0 {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "No peers configured",
			Category: CategoryFederation,
		}
	}

	// Check sync status for each peer
	var staleWarnings []string
	var totalBehind int

	for _, remote := range remotes {
		// Skip origin - check only federation peers
		if remote.Name == "origin" {
			continue
		}

		status, err := fedStore.SyncStatus(ctx, remote.Name)
		if err != nil {
			continue // Already handled in peer connectivity check
		}

		// Warn if significantly behind
		if status.LocalBehind > 0 {
			totalBehind += status.LocalBehind
			staleWarnings = append(staleWarnings, fmt.Sprintf("%s: %d commits behind",
				remote.Name, status.LocalBehind))
		}

		// Note: LastSync time tracking is not yet implemented in SyncStatus
		// When it is, we can add time-based staleness warnings here
	}

	if len(staleWarnings) == 0 {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "Sync is up to date",
			Category: CategoryFederation,
		}
	}

	return DoctorCheck{
		Name:     "Sync Staleness",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d total commits behind peers", totalBehind),
		Detail:   strings.Join(staleWarnings, "\n"),
		Fix:      "Run 'bd federation sync' to synchronize with peers",
		Category: CategoryFederation,
	}
}

// CheckFederationConflicts checks for unresolved merge conflicts.
func CheckFederationConflicts(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	// Check if storage supports versioning (needed for conflict detection)
	verStore, ok := storage.AsVersioned(store)
	if !ok {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "N/A (storage does not support versioning)",
			Category: CategoryFederation,
		}
	}

	conflicts, err := verStore.GetConflicts(ctx)
	if err != nil {
		// Some errors are expected (e.g., no conflicts table)
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "doesn't exist") {
			return DoctorCheck{
				Name:     "Federation Conflicts",
				Status:   StatusOK,
				Message:  "No conflicts",
				Category: CategoryFederation,
			}
		}
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusWarning,
			Message:  "Unable to check conflicts",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	if len(conflicts) == 0 {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "No conflicts",
			Category: CategoryFederation,
		}
	}

	// Group conflicts by issue ID
	issueConflicts := make(map[string][]string)
	for _, c := range conflicts {
		issueConflicts[c.IssueID] = append(issueConflicts[c.IssueID], c.Field)
	}

	var details []string
	for issueID, fields := range issueConflicts {
		details = append(details, fmt.Sprintf("%s: %s", issueID, strings.Join(fields, ", ")))
	}

	return DoctorCheck{
		Name:     "Federation Conflicts",
		Status:   StatusError,
		Message:  fmt.Sprintf("%d unresolved conflicts in %d issues", len(conflicts), len(issueConflicts)),
		Detail:   strings.Join(details, "\n"),
		Fix:      "Run 'bd federation sync --strategy ours|theirs' to resolve conflicts",
		Category: CategoryFederation,
	}
}

// CheckDoltServerModeMismatch checks for mismatch between Dolt init and server mode.
// This detects cases where:
// - Server mode is expected but no server is running
// - Embedded mode is being used when server mode should be used (federation with peers)
func CheckDoltServerModeMismatch(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	// Check for server PID file
	serverPID := dolt.GetRunningServerPID(doltPath)

	// Open storage to check for remotes
	ctx := context.Background()
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		// If we can't open the store, check if there's a lock file indicating embedded mode
		lockFile := filepath.Join(doltPath, ".dolt", "lock")
		if _, lockErr := os.Stat(lockFile); lockErr == nil && serverPID == 0 {
			return DoctorCheck{
				Name:     "Dolt Mode",
				Status:   StatusWarning,
				Message:  "Embedded mode with lock file",
				Detail:   "Another process may be using the database in embedded mode",
				Fix:      "Close other bd processes or start daemon with --federation",
				Category: CategoryFederation,
			}
		}
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	// Check if storage supports federation
	fedStore, isFederated := storage.AsFederated(store)
	if !isFederated {
		if serverPID > 0 {
			return DoctorCheck{
				Name:     "Dolt Mode",
				Status:   StatusOK,
				Message:  "Server mode (no federation)",
				Category: CategoryFederation,
			}
		}
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  "Embedded mode",
			Category: CategoryFederation,
		}
	}

	// Check for configured remotes
	remotes, err := fedStore.ListRemotes(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusWarning,
			Message:  "Unable to list remotes",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	// Count federation peers (exclude origin)
	peerCount := 0
	for _, r := range remotes {
		if r.Name != "origin" {
			peerCount++
		}
	}

	// Determine expected vs actual mode
	if peerCount > 0 && serverPID == 0 {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Embedded mode with %d peers configured", peerCount),
			Detail:   "Federation with peers requires server mode for multi-writer support",
			Fix:      "Run 'bd daemon start --federation' to enable server mode",
			Category: CategoryFederation,
		}
	}

	if serverPID > 0 {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  fmt.Sprintf("Server mode (PID %d)", serverPID),
			Detail:   fmt.Sprintf("%d peers configured", peerCount),
			Category: CategoryFederation,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Mode",
		Status:   StatusOK,
		Message:  "Embedded mode",
		Detail:   "No federation peers configured",
		Category: CategoryFederation,
	}
}


