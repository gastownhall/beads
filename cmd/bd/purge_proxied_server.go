package main

import (
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// proxiedSweeper hands back the guarded bulk-clearance surface for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedCounter performs, and for the same reason: the
// accessor is where each layer is added.
func proxiedSweeper() (issueops.Sweeper, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.SweeperSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the bulk-clearance surface", uowProvider)
	}
	return src.Sweeper()
}
