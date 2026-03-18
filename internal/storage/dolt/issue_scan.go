package dolt

import (
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// issueSelectColumns re-exports the canonical column list from issueops
// for use in dolt-specific query construction.
const issueSelectColumns = issueops.IssueSelectColumns

// scanIssueFrom delegates to the shared issueops scanner. Both *sql.Row
// and *sql.Rows implement issueops.IssueScanner, so this works for
// single-row and multi-row query results alike.
var scanIssueFrom = issueops.ScanIssueFrom
