package main

import (
	"context"
)

// requireFreshDB is a no-op now that JSONL sync has been removed.
// Dolt-native mode uses Dolt as the source of truth — no JSONL staleness check needed.
// Retained as a no-op to avoid touching all read command call sites.
func requireFreshDB(_ context.Context) {}

// ensureDatabaseFresh is a no-op now that JSONL sync has been removed.
func ensureDatabaseFresh(_ context.Context) error {
	return nil
}
