package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
)

// crossPrefixBlockerRouting is the externaldeps option that lets the
// direct-CLI policy evaluate bare cross-prefix blocks / conditional-blocks
// edges (GH#4647) by following routes.jsonl to the store that owns the
// target, read-only.
//
// It is not given to bd serve's chain: openRoutedTargetStore points the
// process-wide BEADS_DOLT_SERVER_DATABASE at the target while it opens, which a
// long-running server answering concurrent requests must not do. The proxied
// CLI builds its policy through WrapUOWProvider, which takes no router, and a
// store whose roles are served remotely never evaluates these edges.
func crossPrefixBlockerRouting() externaldeps.Option {
	return externaldeps.WithIssueRouter(routeCrossPrefixBlocker)
}

// routeCrossPrefixBlocker finds the store owning issueID the way a routed
// lookup does (lookupPrefixRoute, admitForeignWorkspace, loadDoltDatabase) and
// returns a read-only opener for it. No route to follow and a target bd will
// not open as a direct store (a proxied-server rig) wrap
// externaldeps.ErrNoIssueRoute, so the edge does not block; every other
// failure is a configured route that is unavailable.
func routeCrossPrefixBlocker(_ context.Context, issueID string) (externaldeps.IssueRoute, error) {
	route, beadsDir, err := lookupPrefixRoute(issueID)
	if err != nil {
		if errors.Is(err, errRoutingUnavailable) {
			return externaldeps.IssueRoute{}, fmt.Errorf("%w: %v", externaldeps.ErrNoIssueRoute, err)
		}
		return externaldeps.IssueRoute{}, err
	}
	if _, err := admitForeignWorkspace(beadsDir); err != nil {
		return externaldeps.IssueRoute{}, fmt.Errorf("prefix route %q to %s: %w", route.Prefix, route.Path, err)
	}
	database, err := loadDoltDatabase(beadsDir)
	if err == nil && database == "" {
		err = fmt.Errorf("%s has no dolt_database field", filepath.Join(beadsDir, "metadata.json"))
	}
	if err != nil {
		return externaldeps.IssueRoute{}, fmt.Errorf("prefix route %q to %s: %w", route.Prefix, route.Path, err)
	}
	return externaldeps.IssueRoute{
		Key: beadsDir + "\x00" + database,
		Open: func(ctx context.Context) (storage.DoltStorage, error) {
			target, err := openRoutedTargetStore(ctx, beadsDir, database, false)
			if err != nil {
				var capErr *ProxyCapabilityError
				if errors.As(err, &capErr) && capErr.Code == "proxy.store.unrouted" {
					return nil, fmt.Errorf("%w: %v", externaldeps.ErrNoIssueRoute, err)
				}
				return nil, fmt.Errorf("prefix route %q to %s: %w", route.Prefix, route.Path, err)
			}
			return target, nil
		},
	}, nil
}
