package fix

// Keep in sync with issueops.DepTargetExpr.
const fixDependencyTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

// fixExternalTargetExpr is true when an edge's target is stored in
// depends_on_external: an "external:" ref or an id whose prefix names another
// repository (issueops.IsExternalDepTarget). This database holds no row for
// either, so a missing target there is not an orphan to delete.
const fixExternalTargetExpr = "depends_on_external IS NOT NULL"

func fixDependencyUnionSQL() string {
	return `
		SELECT 'dependencies' AS dep_table, issue_id, ` + fixDependencyTargetExpr + ` AS depends_on_id, type, ` + fixExternalTargetExpr + ` AS external_target
		FROM dependencies
		UNION ALL
		SELECT 'wisp_dependencies' AS dep_table, issue_id, ` + fixDependencyTargetExpr + ` AS depends_on_id, type, ` + fixExternalTargetExpr + ` AS external_target
		FROM wisp_dependencies`
}
