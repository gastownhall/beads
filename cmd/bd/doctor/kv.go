package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// kvPrefix matches the prefix used in cmd/bd/kv.go
const kvPrefix = "kv."

// CheckKVSyncStatus checks if KV data exists and warns if it won't sync.
// In git-portable mode, KV data stays local and doesn't sync via JSONL.
func CheckKVSyncStatus(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryData,
		}
	}
	defer func() { _ = store.Close() }()

	// Get all config and count kv.* entries
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "N/A (unable to read config)",
			Category: CategoryData,
		}
	}

	kvCount := 0
	for k := range allConfig {
		if strings.HasPrefix(k, kvPrefix) {
			kvCount++
		}
	}

	// No KV data - nothing to check
	if kvCount == 0 {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "No KV data stored",
			Category: CategoryData,
		}
	}

	// Check sync mode
	syncMode := config.GetSyncMode()

	// In dolt-native or belt-and-suspenders, KV data syncs via Dolt
	if syncMode == config.SyncModeDoltNative || syncMode == config.SyncModeBeltAndSuspenders {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  formatKVCount(kvCount) + " (syncs via Dolt)",
			Category: CategoryData,
		}
	}

	// In git-portable or realtime mode, KV data is local-only
	return DoctorCheck{
		Name:     "KV Store Sync",
		Status:   StatusWarning,
		Message:  formatKVCount(kvCount) + " (local only, won't sync)",
		Detail:   "KV data is stored in the config table which is not exported to JSONL. In git-portable mode, this data stays local to each clone.",
		Fix:      "Use dolt-native sync mode for KV sync, or accept local-only KV storage",
		Category: CategoryData,
	}
}

func formatKVCount(count int) string {
	if count == 1 {
		return "1 KV pair stored"
	}
	return fmt.Sprintf("%d KV pairs stored", count)
}
