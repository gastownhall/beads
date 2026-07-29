package doctor

// Keep in sync with issueops.DepTargetExpr.
const doctorDependencyTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

func doctorDependencyUnionSQL() string {
	return `
		SELECT 'dependencies' AS dep_table, issue_id, ` + doctorDependencyTargetExpr + ` AS depends_on_id, type
		FROM dependencies
		UNION ALL
		SELECT 'wisp_dependencies' AS dep_table, issue_id, ` + doctorDependencyTargetExpr + ` AS depends_on_id, type
		FROM wisp_dependencies`
}

func doctorDependencyUnionWithThreadSQL() string {
	return `
		SELECT 'dependencies' AS dep_table, issue_id, ` + doctorDependencyTargetExpr + ` AS depends_on_id, type, thread_id
		FROM dependencies
		UNION ALL
		SELECT 'wisp_dependencies' AS dep_table, issue_id, ` + doctorDependencyTargetExpr + ` AS depends_on_id, type, thread_id
		FROM wisp_dependencies`
}
