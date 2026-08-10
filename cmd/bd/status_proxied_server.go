package main

import (
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// proxiedStatsReporter hands back the guarded summary-statistics surface for
// the proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedCounter performs, and for the same reason: the
// accessor is where each layer is added.
//
// The role is asked for at the TOP of the command, before any work: this
// command resolves no ids, so there is nothing to look up first and no
// lookup-only provider to trip over.
func proxiedStatsReporter() (issueops.StatsReporter, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.StatsReporterSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the summary-statistics surface", uowProvider)
	}
	return src.StatsReporter()
}
