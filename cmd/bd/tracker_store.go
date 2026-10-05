package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/tracker"
)

// trackerStoreForCommand selects the tracker synchronization seam without
// opening a second direct store in proxied mode.
func trackerStoreForCommand(ctx context.Context) (tracker.Store, error) {
	if usesProxiedServer() {
		if uowProvider == nil {
			return nil, fmt.Errorf("proxied-server provider not initialized")
		}
		store := tracker.NewUOWStore(uowProvider)
		if store == nil {
			return nil, fmt.Errorf("proxied-server provider is invalid")
		}
		return store, nil
	}
	if err := ensureStoreActiveWithContext(ctx); err != nil {
		return nil, err
	}
	return tracker.NewStore(store), nil
}
