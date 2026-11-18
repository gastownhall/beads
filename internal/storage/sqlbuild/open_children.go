package sqlbuild

import (
	"fmt"
	"strings"
)

// Dependency target columns a parent row is named by. A parent in issues is
// the depends_on_issue_id of its children's parent-child rows; a parent in
// wisps is their depends_on_wisp_id. The two are separate columns and stay
// separate: an id may name a row in both tables, and only the column says
// which one the edge points at.
const (
	IssueTargetColumn = "depends_on_issue_id"
	WispTargetColumn  = "depends_on_wisp_id"
)

// ParentTargetColumn is the dependency target column that names a parent row
// of the given family's main table ("issues" or "wisps").
func ParentTargetColumn(tables FilterTables) string {
	if tables.Main == WispsFilterTables.Main {
		return WispTargetColumn
	}
	return IssueTargetColumn
}

// OpenChildFamily is one child family of the open-children relationship: a
// parent-child dependency table paired with the issue table its child rows
// live in. Durable edges join durable children and wisp edges join wisp
// children; the wisp leg skips a row whose dependency id the durable table
// also holds, so a duplicated edge counts once, durable first.
type OpenChildFamily struct {
	Dependencies string
	Children     string
	// DedupeDurable is set on the wisp leg only.
	DedupeDurable bool
}

// OpenChildFamilies are the two legs of the open-children relationship, in
// the order the close gate reads them.
var OpenChildFamilies = []OpenChildFamily{
	{Dependencies: "dependencies", Children: "issues"},
	{Dependencies: "wisp_dependencies", Children: "wisps", DedupeDurable: true},
}

// OpenChildEdgesSQL is the FROM ... WHERE body that selects one child
// family's recorded parent-child edges whose child is not closed: the exact
// relationship the checked close gate refuses on (a literal status !=
// 'closed', so pinned, deferred, blocked and custom statuses all count). The
// dependency row is aliased dependency and the child row child; callers
// append their own target-column condition and projection.
func OpenChildEdgesSQL(f OpenChildFamily) string {
	var b strings.Builder
	fmt.Fprintf(&b, `FROM %s AS dependency
		JOIN %s AS child ON child.id = dependency.issue_id
		WHERE dependency.type = 'parent-child'
		  AND child.status != 'closed'`, f.Dependencies, f.Children)
	if f.DedupeDurable {
		b.WriteString(`
		  AND NOT EXISTS (
			SELECT 1 FROM dependencies AS durable WHERE durable.id = dependency.id
		  )`)
	}
	return b.String()
}

func validTargetColumn(targetColumn string) error {
	if targetColumn != IssueTargetColumn && targetColumn != WispTargetColumn {
		return fmt.Errorf("open children: unsupported target column %q", targetColumn)
	}
	return nil
}

// OpenChildCountSQL counts one child family's open children of the single
// parent bound to its one placeholder, through targetColumn. It is the close
// gate's count.
func OpenChildCountSQL(f OpenChildFamily, targetColumn string) (string, error) {
	if err := validTargetColumn(targetColumn); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT COUNT(DISTINCT dependency.issue_id)\n\t\t%s\n\t\t  AND dependency.%s = ?",
		OpenChildEdgesSQL(f), targetColumn), nil
}

// OpenChildParentIDsSQL selects, with no placeholder, every distinct parent
// named through targetColumn that has at least one open child in family f:
// exactly the parents OpenChildCountSQL would count above zero for that leg.
// A parent of the other family (a null targetColumn) is never selected, and
// grandchildren are not searched.
func OpenChildParentIDsSQL(f OpenChildFamily, targetColumn string) (string, error) {
	if err := validTargetColumn(targetColumn); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT DISTINCT dependency.%s\n\t\t%s\n\t\t  AND dependency.%s IS NOT NULL",
		targetColumn, OpenChildEdgesSQL(f), targetColumn), nil
}
