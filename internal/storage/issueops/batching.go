package issueops

import "github.com/steveyegge/beads/internal/storage/dberrors"

const queryBatchSize = 200

// isTableNotExistError returns true if the error indicates a missing table
// (MySQL/Dolt error 1146).
func isTableNotExistError(err error) bool {
	return dberrors.IsTableNotExist(err)
}
