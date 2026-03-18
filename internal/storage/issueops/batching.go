package issueops

import "strings"

// queryBatchSize limits the number of IDs in a single IN (...) clause.
// Large IN clauses cause Dolt query-planner spikes and MySQL packet-size
// issues. 200 matches the value used in the dolt package historically.
const queryBatchSize = 200

// isTableNotExistError returns true if the error indicates a missing table
// (MySQL/Dolt error 1146). Uses string matching to avoid importing a
// driver-specific package.
func isTableNotExistError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "doesn't exist") || strings.Contains(s, "does not exist") ||
		strings.Contains(s, "error 1146")
}
