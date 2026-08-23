package github

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/types"
)

const (
	githubLinkSubIssue  = "sub_issue"
	githubLinkBlockedBy = "blocked_by"
)

// DependencyLink is a GitHub relationship operation derived from a beads
// parent-child link or "blocks" dependency. FromNumber is the issue the
// relationship is created on (the parent for sub_issue, the blocked issue for
// blocked_by); ToNumber is the other side (the child, or the blocker).
type DependencyLink struct {
	FromNumber  int
	ToNumber    int
	LinkType    string // "sub_issue" or "blocked_by"
	FromBeadsID string
	ToBeadsID   string
}

// PushLinkOptions configures dependency link push behavior.
type PushLinkOptions struct {
	DryRun bool
	OnPlan func(DependencyLink)
}

// PushLinkResult summarizes a PushLinks pass.
type PushLinkResult struct {
	Created int
	Errors  []error
}

// LinkResolver handles GitHub relationship convergence (sub-issues and issue
// dependencies).
type LinkResolver struct {
	Client *Client
}

// NewLinkResolver creates a GitHub dependency link resolver.
func NewLinkResolver(client *Client) *LinkResolver {
	return &LinkResolver{Client: client}
}

// SubIssueLinkFromParentChild converts one beads epic/child parent-child
// dependency into a GitHub sub-issue link. parent must be the epic that
// issue's GetDependenciesWithMetadata resolved via a DepParentChild edge.
func SubIssueLinkFromParentChild(issue *types.Issue, parent *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	if issue == nil || parent == nil || issue.ExternalRef == nil || parent.ExternalRef == nil {
		return DependencyLink{}, false
	}
	childNumber, ok := IssueNumberFromRef(*issue.ExternalRef)
	if !ok {
		return DependencyLink{}, false
	}
	parentNumber, ok := IssueNumberFromRef(*parent.ExternalRef)
	if !ok || parentNumber == childNumber {
		return DependencyLink{}, false
	}
	return DependencyLink{
		FromNumber:  parentNumber,
		ToNumber:    childNumber,
		LinkType:    githubLinkSubIssue,
		FromBeadsID: parent.ID,
		ToBeadsID:   issue.ID,
	}, true
}

// BlockedByLinkFromBeadsDependency converts one beads "blocks" dependency into
// a GitHub blocked_by link. For a beads blocks edge issue -> dep (issue
// depends on / is blocked by dep), GitHub records: issue is blocked_by dep.
func BlockedByLinkFromBeadsDependency(issue *types.Issue, dep *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	if issue == nil || dep == nil || issue.ExternalRef == nil || dep.ExternalRef == nil {
		return DependencyLink{}, false
	}
	if dep.DependencyType != types.DepBlocks {
		return DependencyLink{}, false
	}
	issueNumber, ok := IssueNumberFromRef(*issue.ExternalRef)
	if !ok {
		return DependencyLink{}, false
	}
	depNumber, ok := IssueNumberFromRef(*dep.ExternalRef)
	if !ok || depNumber == issueNumber {
		return DependencyLink{}, false
	}
	return DependencyLink{
		FromNumber:  issueNumber,
		ToNumber:    depNumber,
		LinkType:    githubLinkBlockedBy,
		FromBeadsID: issue.ID,
		ToBeadsID:   dep.ID,
	}, true
}

type githubLinkKey struct {
	FromNumber int
	ToNumber   int
	LinkType   string
}

func (l DependencyLink) key() githubLinkKey {
	return githubLinkKey{FromNumber: l.FromNumber, ToNumber: l.ToNumber, LinkType: l.LinkType}
}

type githubLinkSourceKey struct {
	Number   int
	LinkType string
}

// DeduplicateLinks removes duplicate desired GitHub links and returns them in
// a deterministic order.
func DeduplicateLinks(links []DependencyLink) []DependencyLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]DependencyLink, 0, len(links))
	seen := make(map[githubLinkKey]struct{}, len(links))
	for _, link := range links {
		key := link.key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, link)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FromNumber != result[j].FromNumber {
			return result[i].FromNumber < result[j].FromNumber
		}
		if result[i].ToNumber != result[j].ToNumber {
			return result[i].ToNumber < result[j].ToNumber
		}
		return result[i].LinkType < result[j].LinkType
	})
	return result
}

// PushLinks creates missing GitHub relationships (sub-issues, issue
// dependencies) for the desired link set. It is additive and idempotent:
// each source issue's current relationships of the relevant type are fetched
// once and consulted before any create call, so re-running a sync does not
// re-POST relationships that already exist. Stale remote relationships are
// left untouched.
func (r *LinkResolver) PushLinks(ctx context.Context, desired []DependencyLink, opts PushLinkOptions) PushLinkResult {
	if r == nil || r.Client == nil {
		return PushLinkResult{Errors: []error{fmt.Errorf("GitHub link resolver has no client")}}
	}

	desired = DeduplicateLinks(desired)
	if len(desired) == 0 {
		return PushLinkResult{}
	}

	currentByKey := make(map[githubLinkSourceKey]map[int]struct{})
	idByNumber := make(map[int]int)
	var result PushLinkResult

	for _, link := range desired {
		srcKey := githubLinkSourceKey{Number: link.FromNumber, LinkType: link.LinkType}
		current, ok := currentByKey[srcKey]
		if !ok {
			var err error
			current, err = r.fetchCurrentTargets(ctx, link.FromNumber, link.LinkType)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("fetch GitHub %s for #%d: %w", link.LinkType, link.FromNumber, err))
				continue
			}
			currentByKey[srcKey] = current
		}

		// current is keyed by issue number (present on the list response), so
		// the existence check needs no extra API call. The target's internal
		// numeric ID is only resolved when we're about to actually create the
		// link.
		if _, exists := current[link.ToNumber]; exists {
			continue
		}

		if opts.DryRun {
			if opts.OnPlan != nil {
				opts.OnPlan(link)
			}
			result.Created++
			continue
		}

		targetID, ok := idByNumber[link.ToNumber]
		if !ok {
			issue, err := r.Client.FetchIssueByNumber(ctx, link.ToNumber)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("resolve GitHub issue #%d: %w", link.ToNumber, err))
				continue
			}
			targetID = issue.ID
			idByNumber[link.ToNumber] = targetID
		}

		var err error
		switch link.LinkType {
		case githubLinkSubIssue:
			err = r.Client.AddSubIssue(ctx, link.FromNumber, targetID)
		case githubLinkBlockedBy:
			err = r.Client.AddBlockedBy(ctx, link.FromNumber, targetID)
		default:
			continue
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("create GitHub %s link #%d -> #%d: %w", link.LinkType, link.FromNumber, link.ToNumber, err))
			continue
		}
		current[link.ToNumber] = struct{}{}
		result.Created++
	}

	return result
}

func (r *LinkResolver) fetchCurrentTargets(ctx context.Context, number int, linkType string) (map[int]struct{}, error) {
	var issues []Issue
	var err error
	switch linkType {
	case githubLinkSubIssue:
		issues, err = r.Client.ListSubIssues(ctx, number)
	case githubLinkBlockedBy:
		issues, err = r.Client.ListBlockedBy(ctx, number)
	default:
		return nil, fmt.Errorf("unknown GitHub link type %q", linkType)
	}
	if err != nil {
		return nil, err
	}
	result := make(map[int]struct{}, len(issues))
	for _, iss := range issues {
		result[iss.Number] = struct{}{}
	}
	return result, nil
}
