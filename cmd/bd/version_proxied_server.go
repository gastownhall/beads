package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// proxiedVersionReconciler hands back the clone-local version markers for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedCounter performs.
func proxiedVersionReconciler() (issueops.VersionReconciler, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.VersionReconcilerSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the version-marker surface", uowProvider)
	}
	return src.VersionReconciler()
}

// reconcileVersionProxiedServer records this binary's version on the proxied
// route, and is the twin of autoMigrateOnVersionBump's tail.
//
// EVERY FAILURE HERE IS SWALLOWED TO A DEBUG LINE, as
// issueops.VersionReconciler says its callers do. This runs from
// PersistentPreRun before every proxied command: a workspace whose markers
// cannot be read is a workspace whose commands must still run.
func reconcileVersionProxiedServer(ctx context.Context) {
	if !versionUpgradeDetected || uowProvider == nil {
		return
	}

	reconciler, err := proxiedVersionReconciler()
	if err != nil {
		debug.Logf("reconcile-version: %v", err)
		return
	}
	res, err := reconciler.ReconcileVersion(ctx, issueops.VersionReconcileRequest{CLIVersion: Version})
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
