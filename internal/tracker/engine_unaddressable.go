package tracker

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// resolveTarget gives a TargetResolver every local issue's external_ref, so
// it learns each name its configured target answers under before the sync
// reads a ref.
func (e *Engine) resolveTarget(ctx context.Context) error {
	resolver, ok := e.Tracker.(TargetResolver)
	if !ok {
		return nil
	}
	issues, err := e.Store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("searching local issues: %w", err)
	}
	refs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if ref := strings.TrimSpace(derefStr(issue.ExternalRef)); ref != "" {
			refs = append(refs, ref)
		}
	}
	return resolver.ResolveTarget(ctx, refs)
}

// unaddressable reports whether ref is one of the tracker's refs that it
// cannot address in its configured target: IsExternalRef recognizes it and
// ExtractIdentifier yields "" (GitHub: another repository's issue URL).
func (e *Engine) unaddressable(ref string) bool {
	return ref != "" && e.Tracker.IsExternalRef(ref) && e.Tracker.ExtractIdentifier(ref) == ""
}

// configuredTarget names the tracker's configured target in warnings about
// refs outside it.
func (e *Engine) configuredTarget() string {
	if d, ok := e.Tracker.(TargetDescriber); ok {
		if target := d.DescribeTarget(); target != "" {
			return fmt.Sprintf("the configured %s %s", e.Tracker.DisplayName(), target)
		}
	}
	return fmt.Sprintf("the configured %s repository or project", e.Tracker.DisplayName())
}

// pullSkipsUnaddressable reports whether a pull skips extIssue, the i-th
// issue it fetched, whose own ref the tracker cannot address and no local
// issue holds. An issue fetched on its own, for a pull by ID or a pre-linked
// hydration, left the configured target (GitHub: an issue transferred to
// another repository), so the pull always skips it. An issue before index
// listed came from the target's issue list, so it is the target's own issue,
// served under a name the tracker has not learned (GitHub: a repository
// renamed after ResolveTarget asked): importing it would duplicate the local
// issue that links it under the ref the tracker builds in its configured
// target (GitHub: github:{number}), so the pull skips it then and imports it
// otherwise, rather than let the next pull's cursor pass it by.
func (e *Engine) pullSkipsUnaddressable(fallback pullFallbackIndex, extIssue *TrackerIssue, i, listed int) bool {
	if i >= listed {
		return true
	}
	return fallback.match(extIssue, e.Tracker.BuildExternalRef(&TrackerIssue{Identifier: extIssue.Identifier})) != nil
}

// warnUnaddressablePulls warns once about the fetched issues no local issue
// holds whose own refs the tracker cannot address (outside), listing every
// one the pull skipped (skipped, a subset). The tracker fetched them for its
// configured target, but serves them outside it.
func (e *Engine) warnUnaddressablePulls(outside, skipped []string) {
	name := e.Tracker.DisplayName()
	switch {
	case len(outside) == 0:
	case len(skipped) == len(outside):
		e.warn("Skipped %s %s %s: %s outside %s",
			name, plural(len(skipped), "issue", "issues"), sanitizedList(skipped),
			plural(len(skipped), "its URL is", "their URLs are"), e.configuredTarget())
	case len(outside) == 1:
		e.warn("%s issue %s came back under a URL outside %s",
			name, ui.SanitizeForTerminal(outside[0]), e.configuredTarget())
	default:
		var alsoSkipped string
		if len(skipped) > 0 {
			alsoSkipped = "; skipped " + sanitizedList(skipped)
		}
		e.warn("%d %s issues came back under URLs outside %s, e.g. %s%s",
			len(outside), name, e.configuredTarget(), ui.SanitizeForTerminal(outside[0]), alsoSkipped)
	}
}

// warnMovedPushes warns once about the local issues a push skipped because
// the tracker answered their refs with an issue whose own ref it cannot
// address (refs): the issue left the configured target, and a pull skips it.
func (e *Engine) warnMovedPushes(ids, refs []string) {
	switch len(ids) {
	case 0:
	case 1:
		e.warn("Skipped pushing %s: %s serves its issue as %s, outside %s",
			ids[0], e.Tracker.DisplayName(), ui.SanitizeForTerminal(refs[0]), e.configuredTarget())
	default:
		e.warn("Skipped pushing %s: %s serves their issues under URLs outside %s, e.g. %s",
			strings.Join(ids, ", "), e.Tracker.DisplayName(), e.configuredTarget(), ui.SanitizeForTerminal(refs[0]))
	}
}

// warnUnaddressablePushes warns about the beads a push skipped because the
// tracker cannot address their refs: once per bead the push was asked for by
// ID, and in one line for a bulk push, so a database linked to several
// repositories is not flooded on every sync.
func (e *Engine) warnUnaddressablePushes(opts SyncOptions, ids []string) {
	switch {
	case len(ids) == 0:
	case len(opts.IssueIDs) > 0:
		for _, id := range ids {
			e.warn("Local issue %s is linked outside %s, skipping push", id, e.configuredTarget())
		}
	case len(ids) == 1:
		e.warn("Skipped local issue %s: it is linked outside %s", ids[0], e.configuredTarget())
	default:
		e.warn("Skipped %d local issues linked outside %s, e.g. %s", len(ids), e.configuredTarget(), ids[0])
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func sanitizedList(refs []string) string {
	clean := make([]string, len(refs))
	for i, ref := range refs {
		clean[i] = ui.SanitizeForTerminal(ref)
	}
	return strings.Join(clean, ", ")
}
