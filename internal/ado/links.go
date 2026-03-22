package ado

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
)

// LinkResolver handles bidirectional link sync between beads dependencies
// and ADO work item relations.
type LinkResolver struct {
	Client *Client
}

// NewLinkResolver creates a new LinkResolver with the given client.
func NewLinkResolver(client *Client) *LinkResolver {
	return &LinkResolver{Client: client}
}

// workItemIDPattern extracts a work item ID from an ADO API URL.
// Handles URLs with query parameters (e.g. ?api-version=7.1).
var workItemIDPattern = regexp.MustCompile(`/(\d+)(?:\?|$)`)

// extractWorkItemID extracts the numeric ID from an ADO work item API URL.
func extractWorkItemID(url string) (int, error) {
	matches := workItemIDPattern.FindStringSubmatch(url)
	if len(matches) < 2 {
		return 0, fmt.Errorf("cannot extract work item ID from URL: %s", url)
	}
	id, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid work item ID in URL %s: %w", url, err)
	}
	return id, nil
}

// isLinkRelation checks if a relation type is a work item link (vs attachment, etc).
func isLinkRelation(rel string) bool {
	return strings.HasPrefix(rel, "System.LinkTypes.")
}

// discoveredFromComment is the marker attribute used to identify discovered-from links
// stored as ADO Related relations.
const discoveredFromComment = "beads:discovered-from"

// adoRelToBeadsDep maps an ADO relation type to a beads dependency type.
// Returns the dep type and whether the from/to should be swapped
// (true for reverse link types that need direction normalization).
func adoRelToBeadsDep(rel string, attributes map[string]interface{}) (depType string, swap bool) {
	switch rel {
	case RelDependsOn: // Dependency-Forward: this item is predecessor (blocks target)
		return "blocks", false
	case RelDependencyOf: // Dependency-Reverse: target blocks this item → swap
		return "blocks", true
	case RelChild: // Hierarchy-Forward: this item is parent of target
		return "parent", false
	case RelParent: // Hierarchy-Reverse: target is parent of this item → swap
		return "parent", true
	case RelRelated:
		if hasDiscoveredFromAttribute(attributes) {
			return "discovered-from", false
		}
		return "related", false
	default:
		return "", false
	}
}

// hasDiscoveredFromAttribute checks if the relation attributes contain
// the beads:discovered-from marker in the comment field.
func hasDiscoveredFromAttribute(attributes map[string]interface{}) bool {
	if attributes == nil {
		return false
	}
	comment, ok := attributes["comment"]
	if !ok {
		return false
	}
	s, ok := comment.(string)
	if !ok {
		return false
	}
	return strings.Contains(s, discoveredFromComment)
}

// beadsDepToADORel maps a beads dependency type to an ADO relation type.
func beadsDepToADORel(depType string) string {
	switch depType {
	case "blocks":
		return RelDependsOn
	case "parent":
		return RelChild
	case "related":
		return RelRelated
	case "discovered-from":
		return RelRelated
	default:
		return RelRelated
	}
}

// buildWorkItemURL constructs an ADO API URL for a work item by ID.
// Format: {baseURL}/{project}/_apis/wit/workitems/{id}
func (r *LinkResolver) buildWorkItemURL(id int) string {
	base := r.Client.apiBase()
	return fmt.Sprintf("%s/wit/workitems/%d", base, id)
}

// ExtractLinkDeps extracts beads dependency information from an ADO work item's
// relations. It normalizes link directions and maps ADO relation types to beads
// dependency types. This is the package-level function used by both PullLinks
// and the field mapper's IssueToBeads.
func ExtractLinkDeps(workItem *WorkItem) []tracker.DependencyInfo {
	if len(workItem.Relations) == 0 {
		return nil
	}

	sourceID := strconv.Itoa(workItem.ID)
	var deps []tracker.DependencyInfo

	for _, rel := range workItem.Relations {
		if !isLinkRelation(rel.Rel) {
			continue
		}

		targetID, err := extractWorkItemID(rel.URL)
		if err != nil {
			continue
		}

		depType, swap := adoRelToBeadsDep(rel.Rel, rel.Attributes)
		if depType == "" {
			continue
		}

		targetIDStr := strconv.Itoa(targetID)

		dep := tracker.DependencyInfo{
			Type: depType,
		}
		if swap {
			dep.FromExternalID = targetIDStr
			dep.ToExternalID = sourceID
		} else {
			dep.FromExternalID = sourceID
			dep.ToExternalID = targetIDStr
		}

		deps = append(deps, dep)
	}

	return deps
}

// PullLinks extracts beads dependency information from an ADO work item's relations.
// It normalizes link directions and maps ADO relation types to beads dependency types.
// Returns a slice of DependencyInfo for the sync engine to process.
func (r *LinkResolver) PullLinks(workItem *WorkItem) []tracker.DependencyInfo {
	return ExtractLinkDeps(workItem)
}

// adoLinkKey is used to identify a unique link for diffing.
type adoLinkKey struct {
	Rel       string
	TargetID  int
	Commented bool // true if the link has a discovered-from comment
}

// PushLinks synchronizes beads dependencies to ADO work item relations.
// It compares the desired state (from beads deps) against current ADO relations,
// adding missing links and removing stale ones for idempotent convergence.
// Errors on individual links are collected and returned together; processing
// continues on partial failures.
func (r *LinkResolver) PushLinks(ctx context.Context, workItemID int, currentRelations []WorkItemRelation, desiredDeps []tracker.DependencyInfo) []error {
	// Build desired link set.
	desired := make(map[adoLinkKey]tracker.DependencyInfo)
	for _, dep := range desiredDeps {
		rel := beadsDepToADORel(dep.Type)
		targetID, err := strconv.Atoi(dep.ToExternalID)
		if err != nil {
			continue
		}
		key := adoLinkKey{
			Rel:       rel,
			TargetID:  targetID,
			Commented: dep.Type == "discovered-from",
		}
		desired[key] = dep
	}

	// Build current link set, tracking relation indices for removal.
	type currentLink struct {
		key   adoLinkKey
		index int
	}
	var current []currentLink
	currentSet := make(map[adoLinkKey]bool)

	for i, rel := range currentRelations {
		if !isLinkRelation(rel.Rel) {
			continue
		}
		targetID, err := extractWorkItemID(rel.URL)
		if err != nil {
			continue
		}
		key := adoLinkKey{
			Rel:       rel.Rel,
			TargetID:  targetID,
			Commented: hasDiscoveredFromAttribute(rel.Attributes),
		}
		current = append(current, currentLink{key: key, index: i})
		currentSet[key] = true
	}

	var errs []error

	// Find relations to remove (in current but not desired).
	// Collect indices and remove in reverse order to avoid index shifting.
	var removeIndices []int
	for _, cl := range current {
		if _, ok := desired[cl.key]; !ok {
			removeIndices = append(removeIndices, cl.index)
		}
	}
	// Sort descending so higher indices are removed first.
	sort.Sort(sort.Reverse(sort.IntSlice(removeIndices)))

	for _, idx := range removeIndices {
		if err := r.Client.RemoveWorkItemLink(ctx, workItemID, idx); err != nil {
			errs = append(errs, fmt.Errorf("remove relation %d: %w", idx, err))
		}
	}

	// Find relations to add (in desired but not current).
	for key, dep := range desired {
		if currentSet[key] {
			continue
		}
		targetURL := r.buildWorkItemURL(key.TargetID)
		rel := beadsDepToADORel(dep.Type)
		comment := ""
		if dep.Type == "discovered-from" {
			comment = discoveredFromComment
		}
		if err := r.Client.AddWorkItemLink(ctx, workItemID, targetURL, rel, comment); err != nil {
			errs = append(errs, fmt.Errorf("add link %s to %d: %w", rel, key.TargetID, err))
		}
	}

	return errs
}
