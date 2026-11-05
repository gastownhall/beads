package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// CurrentIssueStatuses is the order FindCurrentIssue looks in: the actor's
// in-progress work first, then work hooked to it.
var CurrentIssueStatuses = []types.Status{types.StatusInProgress, types.StatusHooked}

// FindCurrentIssue answers `bd show --current`'s question — which issue is
// this actor working on — through the Querier role, which every backend
// serves (a remote one as queryIssues). It returns the id of the first issue
// in the actor's in-progress work, else the first hooked to it, in the store's
// default order; "" when both reads answered and found nothing.
//
// A FAILED READ IS AN ERROR, never a miss. A caller falls back (to the
// last-touched issue, say) only on "", which only two reads that answered can
// produce. Treating a refusal as "nothing in progress" is what made the http
// backend print the wrong issue with exit 0.
//
// Each read is exactly the filter `status=<s>`, `assignee=<actor>` and nothing
// else, one row. The actor travels as a quoted query value. The query language
// reads `assignee=none` and `assignee=null` (any case) as "no assignee", so an
// actor by one of those names, or an empty actor, cannot be asked about and
// gets "".
func FindCurrentIssue(ctx context.Context, querier Querier, actor string) (string, error) {
	if querier == nil || actor == "" || strings.EqualFold(actor, "none") || strings.EqualFold(actor, "null") {
		return "", nil
	}
	for _, status := range CurrentIssueStatuses {
		page, err := querier.Query(ctx, currentIssueQuery(status, actor))
		if err != nil {
			return "", fmt.Errorf("finding the current issue (%s, assigned to %s): %w", status, actor, err)
		}
		for _, row := range page.Items {
			if row != nil && row.Issue != nil {
				return row.ID, nil
			}
		}
	}
	return "", nil
}

// currentIssueQuery is the role request for one status. No display order:
// every backend answers in its default order, and a sort would change which
// row is "current".
func currentIssueQuery(status types.Status, actor string) QueryRequest {
	one := 1
	return QueryRequest{
		Expression: fmt.Sprintf("status=%s AND assignee=%s", status, quoteQueryValue(actor)),
		Limit:      &one,
	}
}

// quoteQueryValue renders v as a double-quoted query-language string, escaping
// the two characters the lexer gives meaning inside one.
func quoteQueryValue(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}
