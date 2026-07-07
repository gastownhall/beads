package fix

// Keep in sync with issueops.DepTargetExpr.
const fixDependencyTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

func fixDependencyUnionSQL() string {
	return `
		SELECT 'dependencies' AS dep_table, issue_id, ` + fixDependencyTargetExpr + ` AS depends_on_id, type
		FROM dependencies
		UNION ALL
		SELECT 'wisp_dependencies' AS dep_table, issue_id, ` + fixDependencyTargetExpr + ` AS depends_on_id, type
		FROM wisp_dependencies`
}
