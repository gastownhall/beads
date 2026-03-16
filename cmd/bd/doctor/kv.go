package doctor

import (
	"context"
	"fmt"
	"strings"
)

// kvPrefix matches the prefix used in cmd/bd/kv.go
const kvPrefix = "kv."

// CheckKVSyncStatus checks if KV data exists and reports sync status.
func CheckKVSyncStatus(path string) DoctorCheck {
	ctx := context.Background()
	store, err := openDoltStoreForRepoPath(ctx, path)
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

	if kvCount == 0 {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "No KV data stored",
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "KV Store Sync",
		Status:   StatusOK,
		Message:  formatKVCount(kvCount) + " (syncs via Dolt)",
		Category: CategoryData,
	}
}

func formatKVCount(count int) string {
	if count == 1 {
		return "1 KV pair stored"
	}
	return fmt.Sprintf("%d KV pairs stored", count)
}
