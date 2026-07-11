package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func reconcileVersionProxiedServer(ctx context.Context) {
	if !versionUpgradeDetected || uowProvider == nil {
		return
	}

	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		debug.Logf("reconcile-version: open uow: %v", err)
		return
	}
	defer uw.Close(ctx)

	res, err := uw.ConfigUseCase().ReconcileVersion(ctx, Version)
	if err != nil {
		debug.Logf("reconcile-version: %v", err)
		return
	}

	if err := uow.CommitWithRetries(ctx, uw, fmt.Sprintf("bd: reconcile version -> %s", res.Current)); err != nil && !isDoltNothingToCommit(err) {
		debug.Logf("reconcile-version: commit: %v", err)
		return
	}

	switch {
	case res.Downgrade:
		debug.Logf("reconcile-version: refused downgrade to %s (db at %s)", Version, res.Previous)
	case res.Migrated:
		debug.Logf("reconcile-version: migrated %s -> %s", res.Previous, res.Current)
	}
}
