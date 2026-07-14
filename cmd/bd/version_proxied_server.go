package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func reconcileVersionProxiedServer(ctx context.Context) {
	if !versionUpgradeDetected || uowProvider == nil {
		return
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (domain.VersionReconcileResult, string, error) {
		res, err := uw.ConfigUseCase().ReconcileVersion(ctx, Version)
		if err != nil {
			return domain.VersionReconcileResult{}, "", err
		}
		return res, fmt.Sprintf("bd: reconcile version -> %s", res.Current), nil
	})
	if err != nil {
		debug.Logf("reconcile-version: %v", err)
		return
	}

	switch {
	case res.Downgrade:
		debug.Logf("reconcile-version: refused downgrade to %s (db at %s)", Version, res.Previous)
	case res.Migrated:
		debug.Logf("reconcile-version: migrated %s -> %s", res.Previous, res.Current)
	}
}
