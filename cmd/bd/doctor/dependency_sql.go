package doctor

// Keep in sync with issueops.DepTargetExpr.
const doctorDependencyTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

// doctorExternalTargetExpr is true when an edge's target is stored in
// depends_on_external: an "external:" ref or an id whose prefix names another
// repository (issueops.IsExternalDepTarget). This database holds no row for
// either, so a missing target there is not an orphan.
const doctorExternalTargetExpr = "depends_on_external IS NOT NULL"

func doctorDependencyUnionSQL() string {
	return `
		SELECT 'dependencies' AS dep_table, issue_id, ` + doctorDependencyTargetExpr + ` AS depends_on_id, type, ` + doctorExternalTargetExpr + ` AS external_target
		FROM dependencies
		UNION ALL
		SELECT 'wisp_dependencies' AS dep_table, issue_id, ` + doctorDependencyTargetExpr + ` AS depends_on_id, type, ` + doctorExternalTargetExpr + ` AS external_target
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
