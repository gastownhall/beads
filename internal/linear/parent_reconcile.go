package linear

import (
	"context"
	"errors"
	"fmt"
)

// fetchIssueAcrossTeams locates an issue by its Linear identifier across
// all configured team clients. Single-team setups hit the primary client
// directly; multi-team setups fall through each client in order until one
// returns a non-nil issue. Returns (issue, hostClient, nil) on success;
// the hostClient is the team's client that owned the issue and should be
// reused for subsequent mutations (so the update path doesn't re-probe
// and silently fall back to the wrong client on transient probe errors —
// see clientForExternalID's fallback behavior in tracker.go).
//
// Returns (nil, nil, nil) when no team has the issue.
func (t *Tracker) fetchIssueAcrossTeams(ctx context.Context, identifier string) (*Issue, *Client, error) {
	if identifier == "" {
		return nil, nil, nil
	}
	if len(t.teamIDs) <= 1 {
		client := t.primaryClient()
		if client == nil {
			return nil, nil, errors.New("no Linear client available")
		}
		li, err := client.FetchIssueByIdentifier(ctx, identifier)
		if err != nil {
			return nil, nil, err
		}
		if li == nil {
			return nil, nil, nil
		}
		return li, client, nil
	}
	// Multi-team: try each client. First non-nil result wins. Rate-limit
	// errors abort immediately (the cross-team probe shouldn't burn
	// through quota when the circuit breaker has already tripped).
	for _, teamID := range t.teamIDs {
		client := t.clients[teamID]
		if client == nil {
			continue
		}
		li, err := client.FetchIssueByIdentifier(ctx, identifier)
		if err != nil {
			if isRateLimitExhausted(err) {
				return nil, nil, err
			}
			continue
		}
		if li != nil {
			return li, client, nil
		}
	}
	return nil, nil, nil
}

// isRateLimitExhausted returns true when err (or any error it wraps)
// signals that Linear's rate-limit circuit breaker has tripped.
// Mirrors internal/tracker/engine.go isRateLimitExhausted, duplicated
// here to avoid an import cycle.
func isRateLimitExhausted(err error) bool {
	if err == nil {
		return false
	}
	type rle interface{ RateLimitExhausted() bool }
	var r rle
	return errors.As(err, &r) && r.RateLimitExhausted()
}

// ParentLink describes a desired parent-child relationship to wire up
// on the Linear side. Both fields are Linear identifiers (e.g. "HOU-159")
// — typically extracted from each bead's external_ref via
// Tracker.ExtractIdentifier.
type ParentLink struct {
	ChildIdentifier  string
	ParentIdentifier string
}

// ParentReconcileStats summarizes a ReconcileParents run.
type ParentReconcileStats struct {
	// Updated is the count of Linear issues whose parent field was changed
	// (set, cleared, or rewired) by this pass. Zero in dry-run mode — see
	// WouldUpdate for the dry-run counterpart.
	Updated int
	// WouldUpdate is the count of mutations the pass WOULD have issued
	// in wet-run. Populated only in dry-run mode; zero in wet-run.
	WouldUpdate int
	// Mutations is the (child, parent) link list that was applied (wet-run)
	// or would have been applied (dry-run). In wet-run, an entry is appended
	// only after the IssueUpdate API call succeeds — so this list reflects
	// state actually propagated to Linear, not attempted. In dry-run, all
	// candidates that pass the idempotency check appear. Lets callers print
	// per-link detail without having to re-derive it.
	Mutations []ParentLink
	// Skipped is the count of links where Linear's parent already matched
	// the desired value — no API mutation was issued.
	Skipped int
	// NotFound is the list of identifiers (child or parent) that didn't
	// resolve to a Linear issue (typically because their bead has no
	// external_ref yet, or the Linear issue was deleted out-of-band).
	// Their links are silently skipped; the next sync will retry.
	NotFound []string
	// Errors collects per-link failures that did not abort the pass.
	Errors []error
}

// ReconcileParents wires parent-child relationships from bead-side
// dependencies to Linear's parent issue field. Used as a post-sync pass
// to handle two cases that the per-issue create/update path can't cover:
//
//  1. New tree push: when a child is created before its parent's external
//     ref is known to the engine, the create call has no parentId to send.
//     This pass closes the loop after every issue in the tree has an
//     external ref.
//  2. Orphan repair: existing Linear issues that were created (in earlier
//     bd versions or by interrupted syncs) without parentId can be wired
//     up retroactively.
//
// Idempotent: fetches each child's current parent and only issues
// IssueUpdate when the remote parent differs from the desired value.
// Each unique parent identifier is fetched once to resolve its UUID
// (IssueUpdateInput.parentId requires the internal UUID, not the
// human-readable identifier).
//
// When dryRun is true, the read-only fetches still run (so the caller
// gets accurate Skipped / NotFound counts and a populated Mutations
// list for preview output) but the IssueUpdate mutation is skipped.
// Mutations that would have fired increment stats.WouldUpdate instead
// of stats.Updated.
//
// Returns nil error when the pass completed (even if per-link errors
// were collected in Stats.Errors). A non-nil error indicates a setup-level
// failure that prevented any work from running.
func (t *Tracker) ReconcileParents(ctx context.Context, links []ParentLink, dryRun bool) (*ParentReconcileStats, error) {
	stats := &ParentReconcileStats{}
	if len(links) == 0 {
		return stats, nil
	}
	if t.primaryClient() == nil {
		return nil, errors.New("no Linear client available")
	}

	// Cache identifier → (issue, host client) so we don't refetch when a
	// parent is shared by many children, AND so the update path uses the
	// SAME client that successfully fetched the child (avoids re-probing
	// via clientForExternalID, which silently falls back to the primary
	// client on transient probe errors and could send the update to the
	// wrong team).
	type entry struct {
		issue  *Issue
		client *Client
	}
	fetched := make(map[string]entry, len(links)*2)
	fetchIssue := func(identifier string) (entry, error) {
		if cached, ok := fetched[identifier]; ok {
			return cached, nil
		}
		issue, client, err := t.fetchIssueAcrossTeams(ctx, identifier)
		if err != nil {
			return entry{}, err
		}
		e := entry{issue: issue, client: client}
		// Cache nil too — repeated lookups are still cheap.
		fetched[identifier] = e
		return e, nil
	}

	for _, link := range links {
		if link.ChildIdentifier == "" || link.ParentIdentifier == "" {
			continue
		}

		childE, err := fetchIssue(link.ChildIdentifier)
		if err != nil {
			// Rate-limit circuit breaker tripped — stop now rather than
			// hammer the API for every remaining link.
			if isRateLimitExhausted(err) {
				return stats, fmt.Errorf("fetch child %s: %w", link.ChildIdentifier, err)
			}
			stats.Errors = append(stats.Errors,
				fmt.Errorf("fetch child %s: %w", link.ChildIdentifier, err))
			continue
		}
		if childE.issue == nil {
			stats.NotFound = append(stats.NotFound, link.ChildIdentifier)
			continue
		}

		parentE, err := fetchIssue(link.ParentIdentifier)
		if err != nil {
			if isRateLimitExhausted(err) {
				return stats, fmt.Errorf("fetch parent %s: %w", link.ParentIdentifier, err)
			}
			stats.Errors = append(stats.Errors,
				fmt.Errorf("fetch parent %s: %w", link.ParentIdentifier, err))
			continue
		}
		if parentE.issue == nil {
			stats.NotFound = append(stats.NotFound, link.ParentIdentifier)
			continue
		}

		// Idempotency: skip if remote parent already matches by UUID.
		if childE.issue.Parent != nil && childE.issue.Parent.ID == parentE.issue.ID {
			stats.Skipped++
			continue
		}

		if dryRun {
			// Dry-run: record the intended mutation but skip the API call.
			// All read-only state (fetch results, idempotency check above)
			// matched wet-run, so the preview is trustworthy.
			stats.Mutations = append(stats.Mutations, link)
			stats.WouldUpdate++
			continue
		}

		// Use the child's host client (resolved during fetch) so the
		// update goes to the correct team in multi-team setups.
		updated, err := childE.client.UpdateIssue(ctx, childE.issue.ID, map[string]interface{}{
			"parentId": parentE.issue.ID,
		})
		if err != nil {
			if isRateLimitExhausted(err) {
				return stats, fmt.Errorf("set parent of %s → %s: %w",
					link.ChildIdentifier, link.ParentIdentifier, err)
			}
			stats.Errors = append(stats.Errors,
				fmt.Errorf("set parent of %s → %s: %w",
					link.ChildIdentifier, link.ParentIdentifier, err))
			continue
		}
		// Refresh cache with the post-update issue (Linear returns the
		// updated record), so a later link that references this child
		// as a parent sees the freshest state. Host client is unchanged.
		if updated != nil {
			fetched[link.ChildIdentifier] = entry{issue: updated, client: childE.client}
		}
		// Record the mutation only AFTER the API call succeeds, so
		// Mutations reflects actual state propagated to Linear (callers
		// can trust the list for accurate post-sync reporting).
		stats.Mutations = append(stats.Mutations, link)
		stats.Updated++
	}

	return stats, nil
}
